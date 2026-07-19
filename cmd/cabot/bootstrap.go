package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/dgunzy/go-book/internal/config"
	"github.com/jackc/pgx/v5"
)

// bootstrapAdvisoryLockID serializes concurrent bootstrap-owner runs so two
// operators cannot create two owners in a race. The value is arbitrary but
// must stay stable.
const bootstrapAdvisoryLockID = 727_001

// runBootstrapOwner grants the very first owner membership, before any
// invitation or admin exists to approve members through the normal flow.
// It is idempotent: re-running for the same email is a no-op, and it refuses
// to run at all once a different owner exists. The grant is written to the
// immutable audit log with the operator-supplied reason.
func runBootstrapOwner(ctx context.Context, logger *slog.Logger, lookup lookupFunc, output io.Writer) error {
	email, err := requiredBootstrapValue(lookup, "BOOTSTRAP_OWNER_EMAIL")
	if err != nil {
		return err
	}
	email = strings.ToLower(email)
	if !strings.Contains(email, "@") {
		return errors.New("BOOTSTRAP_OWNER_EMAIL must be an email address")
	}
	displayName, err := requiredBootstrapValue(lookup, "BOOTSTRAP_OWNER_DISPLAY_NAME")
	if err != nil {
		return err
	}
	if len(displayName) > 120 {
		return errors.New("BOOTSTRAP_OWNER_DISPLAY_NAME must be 1-120 characters")
	}
	reason, err := requiredBootstrapValue(lookup, "BOOTSTRAP_OWNER_REASON")
	if err != nil {
		return err
	}

	databaseMode, databaseURL, err := config.DatabaseSelection(lookup)
	if err != nil {
		return err
	}
	if strings.TrimSpace(databaseURL) == "" {
		return errors.New("DATABASE_URL is required to bootstrap an owner")
	}
	logger.Info("bootstrap owner starting", "database_mode", databaseMode)

	connection, err := pgx.Connect(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return fmt.Errorf("connect for owner bootstrap: %w", err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	tx, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin owner bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire bootstrap lock: %w", err)
	}

	var existingOwnerEmail string
	err = tx.QueryRow(ctx, `
		SELECT lower(coalesce(u.email, ''))
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.role = 'owner' AND m.revoked_at IS NULL
		LIMIT 1`).Scan(&existingOwnerEmail)
	switch {
	case err == nil:
		if existingOwnerEmail == email {
			fmt.Fprint(output, "owner membership already exists for this email; nothing to do\n")
			return nil
		}
		return errors.New("an active owner already exists; grant further roles through the admin flow")
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check for existing owner: %w", err)
	}

	var userID, status string
	err = tx.QueryRow(ctx, `SELECT id::text, status FROM users WHERE lower(email) = $1 FOR UPDATE`, email).Scan(&userID, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (display_name, email, status) VALUES ($1, $2, 'active')
			RETURNING id::text`, displayName, email).Scan(&userID); err != nil {
			return fmt.Errorf("create owner user: %w", err)
		}
	case err != nil:
		return fmt.Errorf("look up owner user: %w", err)
	case status != "active":
		return fmt.Errorf("user with this email has status %q; resolve it before granting owner", status)
	}

	var activeRole string
	err = tx.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID).Scan(&activeRole)
	switch {
	case err == nil:
		return fmt.Errorf("user already holds an active %q membership; revoke it through the admin flow first", activeRole)
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check existing membership: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO memberships (user_id, role) VALUES ($1::uuid, 'owner')`, userID); err != nil {
		return fmt.Errorf("grant owner membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, after_data)
		VALUES (NULL, 'membership.bootstrap_owner', 'user', $1::uuid, $2, '{"role": "owner"}'::jsonb)`,
		userID, reason); err != nil {
		return fmt.Errorf("record bootstrap audit entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit owner bootstrap: %w", err)
	}
	logger.Info("owner membership bootstrapped", "user_id", userID)
	fmt.Fprint(output, "owner membership granted; sign in with Google using this email to link the account\n")
	return nil
}

func requiredBootstrapValue(lookup lookupFunc, name string) (string, error) {
	value, ok := lookup(name)
	trimmed := strings.TrimSpace(value)
	if !ok || trimmed == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return trimmed, nil
}
