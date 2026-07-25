package privatepg

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("PRIVATEPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PRIVATEPG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The member views are pure reads, so this gate proves every query still
// parses and matches the live schema without writing a single row: it asks for
// a user that does not exist. Any renamed column or malformed SQL fails here
// instead of on the member's dashboard.
func TestReadQueriesMatchTheLiveSchema(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var missingUser string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&missingUser); err != nil {
		t.Fatalf("generate uuid: %v", err)
	}

	reader, err := NewWithSettings(pool, Settings{AutoApproveDefaultCents: 10_000})
	if err != nil {
		t.Fatal(err)
	}

	// DashboardSummary runs every member-scoped query; a user with no row
	// stops only at the credit line, after the rest have executed.
	if _, err := reader.DashboardSummary(ctx, missingUser); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("DashboardSummary for an unknown user error = %v, want %v", err, pgx.ErrNoRows)
	}

	rows, err := reader.LedgerRows(ctx, missingUser)
	if err != nil || len(rows) != 0 {
		t.Fatalf("LedgerRows = %d rows, error = %v", len(rows), err)
	}

	wagers, err := reader.WagerRows(ctx, missingUser)
	if err != nil || len(wagers) != 0 {
		t.Fatalf("WagerRows = %d rows, error = %v", len(wagers), err)
	}

	active, err := reader.activeWagerRows(ctx, missingUser, activeWagerPreview)
	if err != nil || len(active) != 0 {
		t.Fatalf("activeWagerRows = %d rows, error = %v", len(active), err)
	}

	if _, err := reader.ReconciliationSummary(ctx); err != nil {
		t.Fatalf("ReconciliationSummary error = %v", err)
	}

	// The admin dashboard is book-wide rather than user-scoped, so its four
	// queries need their own trip through the live schema.
	if _, err := reader.BookPulse(ctx); err != nil {
		t.Fatalf("BookPulse error = %v", err)
	}
}
