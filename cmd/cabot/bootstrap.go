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

// bootstrapAdvisoryLockID serializes concurrent bootstrap runs so two
// operators cannot race a role grant. The value is arbitrary but must stay
// stable.
const bootstrapAdvisoryLockID = 727_001

// runBootstrapOwner grants the first owner membership using the BOOTSTRAP_OWNER_*
// environment. It is a thin wrapper over the generalized grant with role owner.
func runBootstrapOwner(ctx context.Context, logger *slog.Logger, lookup lookupFunc, output io.Writer) error {
	email, err := requiredBootstrapValue(lookup, "BOOTSTRAP_OWNER_EMAIL")
	if err != nil {
		return err
	}
	if !strings.Contains(email, "@") {
		return errors.New("BOOTSTRAP_OWNER_EMAIL must be an email address")
	}
	displayName, err := requiredBootstrapValue(lookup, "BOOTSTRAP_OWNER_DISPLAY_NAME")
	if err != nil {
		return err
	}
	reason, err := requiredBootstrapValue(lookup, "BOOTSTRAP_OWNER_REASON")
	if err != nil {
		return err
	}
	return grantBootstrapMembership(ctx, logger, lookup, output, "owner", email, displayName, reason)
}

// runBootstrapRole grants a chosen role (owner, admin, or member) using the
// BOOTSTRAP_ROLE/BOOTSTRAP_EMAIL/BOOTSTRAP_DISPLAY_NAME/BOOTSTRAP_REASON
// environment. It bootstraps privileged accounts before any in-app admin
// exists to invite them. Owner grants keep the single-owner guard; admin and
// member grants may be issued to any number of accounts.
func runBootstrapRole(ctx context.Context, logger *slog.Logger, lookup lookupFunc, output io.Writer) error {
	role, err := requiredBootstrapValue(lookup, "BOOTSTRAP_ROLE")
	if err != nil {
		return err
	}
	role = strings.ToLower(role)
	if role != "owner" && role != "admin" && role != "member" {
		return errors.New("BOOTSTRAP_ROLE must be owner, admin, or member")
	}
	email, err := requiredBootstrapValue(lookup, "BOOTSTRAP_EMAIL")
	if err != nil {
		return err
	}
	if !strings.Contains(email, "@") {
		return errors.New("BOOTSTRAP_EMAIL must be an email address")
	}
	displayName, err := requiredBootstrapValue(lookup, "BOOTSTRAP_DISPLAY_NAME")
	if err != nil {
		return err
	}
	reason, err := requiredBootstrapValue(lookup, "BOOTSTRAP_REASON")
	if err != nil {
		return err
	}
	return grantBootstrapMembership(ctx, logger, lookup, output, role, email, displayName, reason)
}

// grantBootstrapMembership creates (if needed) an active user for email and
// grants it role, writing the grant to the immutable audit log. It is
// idempotent: re-granting the same role to the same email is a no-op. For
// role owner it refuses when a different active owner already exists; for any
// role it refuses when the account already holds a different active role.
func grantBootstrapMembership(ctx context.Context, logger *slog.Logger, lookup lookupFunc, output io.Writer, role, email, displayName, reason string) error {
	email = strings.ToLower(email)
	if len(displayName) > 120 {
		return errors.New("display name must be 1-120 characters")
	}

	databaseMode, databaseURL, err := config.DatabaseSelection(lookup)
	if err != nil {
		return err
	}
	if strings.TrimSpace(databaseURL) == "" {
		return errors.New("DATABASE_URL is required to bootstrap a membership")
	}
	logger.Info("bootstrap membership starting", "database_mode", databaseMode, "role", role)

	connection, err := pgx.Connect(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return fmt.Errorf("connect for membership bootstrap: %w", err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	tx, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin membership bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire bootstrap lock: %w", err)
	}

	if role == "owner" {
		var existingOwnerEmail string
		err = tx.QueryRow(ctx, `
			SELECT lower(coalesce(u.email, ''))
			FROM memberships m JOIN users u ON u.id = m.user_id
			WHERE m.role = 'owner' AND m.revoked_at IS NULL
			LIMIT 1`).Scan(&existingOwnerEmail)
		switch {
		case err == nil && existingOwnerEmail != email:
			return errors.New("an active owner already exists; grant further roles through the admin flow")
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check for existing owner: %w", err)
		}
	}

	var userID, status string
	err = tx.QueryRow(ctx, `SELECT id::text, status FROM users WHERE lower(email) = $1 FOR UPDATE`, email).Scan(&userID, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (display_name, email, status) VALUES ($1, $2, 'active')
			RETURNING id::text`, displayName, email).Scan(&userID); err != nil {
			return fmt.Errorf("create bootstrap user: %w", err)
		}
	case err != nil:
		return fmt.Errorf("look up bootstrap user: %w", err)
	case status != "active":
		return fmt.Errorf("user with this email has status %q; resolve it before granting a role", status)
	}

	var activeRole string
	err = tx.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID).Scan(&activeRole)
	switch {
	case err == nil:
		if activeRole == role {
			fmt.Fprintf(output, "%s already holds an active %s membership; nothing to do\n", email, role)
			return nil
		}
		return fmt.Errorf("user already holds an active %q membership; revoke it through the admin flow first", activeRole)
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check existing membership: %w", err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, $2)`, userID, role); err != nil {
		return fmt.Errorf("grant %s membership: %w", role, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, after_data)
		VALUES (NULL, $1, 'user', $2::uuid, $3, jsonb_build_object('role', $4::text))`,
		"membership.bootstrap_"+role, userID, reason, role); err != nil {
		return fmt.Errorf("record bootstrap audit entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit membership bootstrap: %w", err)
	}
	logger.Info("membership bootstrapped", "user_id", userID, "role", role)
	fmt.Fprintf(output, "%s membership granted to %s; sign in with Google using this email to link the account\n", role, email)
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
