// Package schema applies embedded PostgreSQL migrations exactly once.
package schema

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const advisoryLockKey int64 = 0x4341424f54435550 // "CABOTCUP"

type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Begin(context.Context) (pgx.Tx, error)
}

type Report struct {
	Applied []int64
	Skipped []int64
}

// Apply serializes migration ownership, validates checksums, and commits each SQL
// migration with its version record in the same transaction.
func Apply(ctx context.Context, db DB) (report Report, returnErr error) {
	if _, err := db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version bigint PRIMARY KEY,
    name text NOT NULL,
    checksum char(64) NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return report, fmt.Errorf("create migration ledger: %w", err)
	}

	if _, err := db.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return report, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.Exec(unlockContext, "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("release migration lock: %w", err)
		}
	}()

	for _, definition := range migrations.All() {
		applied, err := applyOne(ctx, db, definition)
		if err != nil {
			return report, err
		}
		if applied {
			report.Applied = append(report.Applied, definition.Version)
		} else {
			report.Skipped = append(report.Skipped, definition.Version)
		}
	}
	return report, nil
}

func applyOne(ctx context.Context, db DB, definition migrations.Definition) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin migration %d: %w", definition.Version, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var checksum string
	err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version = $1", definition.Version).Scan(&checksum)
	switch {
	case err == nil:
		if checksum != definition.Checksum {
			return false, fmt.Errorf("migration %d checksum changed: database=%s binary=%s", definition.Version, checksum, definition.Checksum)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit migration %d check: %w", definition.Version, err)
		}
		return false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return false, fmt.Errorf("check migration %d: %w", definition.Version, err)
	}

	if _, err := tx.Exec(ctx, definition.SQL); err != nil {
		return false, fmt.Errorf("apply migration %d (%s): %w", definition.Version, definition.Name, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
		definition.Version, definition.Name, definition.Checksum,
	); err != nil {
		return false, fmt.Errorf("record migration %d: %w", definition.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit migration %d: %w", definition.Version, err)
	}
	return true, nil
}
