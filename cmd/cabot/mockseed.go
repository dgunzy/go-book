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

// mockSeedAccount is one approved account created by mock-seed. Email is the
// dev-login handle; the role is its membership.
type mockSeedAccount struct {
	Email       string
	DisplayName string
	Role        string
	// FundCents, when positive, funds the account's user_cash ledger balance
	// so a member can place wagers immediately.
	FundCents int64
}

var mockSeedAccounts = []mockSeedAccount{
	{Email: "owner@cabot.test", DisplayName: "Mock Owner", Role: "owner"},
	{Email: "admin@cabot.test", DisplayName: "Mock Admin", Role: "admin"},
	{Email: "member@cabot.test", DisplayName: "Mock Member One", Role: "member", FundCents: 50_000},
	{Email: "member2@cabot.test", DisplayName: "Mock Member Two", Role: "member", FundCents: 50_000},
}

// runMockSeed populates the connected database with approved mock accounts, a
// little member cash, and one open market so the private book can be exercised
// end-to-end with the `dev`-build dev-login. It refuses to run when
// APP_ENV=production and is idempotent: re-running does not duplicate rows.
func runMockSeed(ctx context.Context, logger *slog.Logger, lookup lookupFunc, output io.Writer) error {
	if strings.TrimSpace(valueOrDefaultLookup(lookup, "APP_ENV", "development")) == "production" {
		return errors.New("mock-seed is not allowed when APP_ENV=production")
	}
	databaseMode, databaseURL, err := config.DatabaseSelection(lookup)
	if err != nil {
		return err
	}
	if strings.TrimSpace(databaseURL) == "" {
		return errors.New("DATABASE_URL is required for mock-seed")
	}
	logger.Info("mock-seed starting", "database_mode", databaseMode)

	connection, err := pgx.Connect(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return fmt.Errorf("connect for mock-seed: %w", err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	tx, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin mock-seed: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var adminUserID string
	for _, account := range mockSeedAccounts {
		userID, err := ensureMockUser(ctx, tx, account)
		if err != nil {
			return err
		}
		if account.Role == "admin" || (adminUserID == "" && account.Role == "owner") {
			adminUserID = userID
		}
		if account.FundCents > 0 {
			if err := fundMockUser(ctx, tx, userID, account.FundCents); err != nil {
				return err
			}
		}
		fmt.Fprintf(output, "account %-20s role=%-6s\n", account.Email, account.Role)
	}

	created, err := ensureMockMarket(ctx, tx, adminUserID)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintln(output, "created open market \"Mock Cup Winner\" with two selections")
	} else {
		fmt.Fprintln(output, "open market \"Mock Cup Winner\" already present")
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mock-seed: %w", err)
	}
	fmt.Fprintln(output, "mock-seed complete; sign in at /dev/login with any account email above")
	return nil
}

func ensureMockUser(ctx context.Context, tx pgx.Tx, account mockSeedAccount) (string, error) {
	var userID string
	err := tx.QueryRow(ctx, `
		INSERT INTO users (display_name, email, status) VALUES ($1, $2, 'active')
		ON CONFLICT DO NOTHING
		RETURNING id::text`, account.DisplayName, account.Email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(email) = $1`, account.Email).Scan(&userID); err != nil {
			return "", fmt.Errorf("load mock user %s: %w", account.Email, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("insert mock user %s: %w", account.Email, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO memberships (user_id, role) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) WHERE revoked_at IS NULL DO NOTHING`, userID, account.Role); err != nil {
		return "", fmt.Errorf("grant mock membership %s: %w", account.Email, err)
	}
	return userID, nil
}

func fundMockUser(ctx context.Context, tx pgx.Tx, userID string, cents int64) error {
	var cashAccount string
	if err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (owner_user_id, account_type, currency, name)
		VALUES ($1::uuid, 'user_cash', 'CAD', $2)
		ON CONFLICT (owner_user_id, account_type, currency) WHERE owner_user_id IS NOT NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, userID, "Mock cash "+userID).Scan(&cashAccount); err != nil {
		return fmt.Errorf("ensure mock cash account: %w", err)
	}
	var equityAccount string
	if err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, name)
		VALUES ('migration_equity', 'CAD', 'Mock equity')
		ON CONFLICT (account_type, currency) WHERE owner_user_id IS NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`).Scan(&equityAccount); err != nil {
		return fmt.Errorf("ensure mock equity account: %w", err)
	}
	idempotencyKey := "mock-seed-fund:" + userID
	var transactionID string
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
		(transaction_type, currency, idempotency_key, source_type, reason, expected_posting_count, occurred_at)
		VALUES ('migration_adjustment', 'CAD', $1, 'mock_seed', 'mock seed funding', 2, now())
		ON CONFLICT (currency, idempotency_key) DO NOTHING
		RETURNING id::text`, idempotencyKey).Scan(&transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already funded on a previous run
	}
	if err != nil {
		return fmt.Errorf("insert mock funding transaction: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_postings (transaction_id, account_id, amount_cents)
		VALUES ($1::uuid, $2::uuid, $3), ($1::uuid, $4::uuid, $5)`,
		transactionID, cashAccount, cents, equityAccount, -cents); err != nil {
		return fmt.Errorf("insert mock funding postings: %w", err)
	}
	return nil
}

func ensureMockMarket(ctx context.Context, tx pgx.Tx, createdBy string) (bool, error) {
	if createdBy == "" {
		return false, errors.New("mock-seed needs an admin or owner account to own the market")
	}
	// Dynamic pricing on, liquidity $1,500, so a member's bet noticeably but
	// not wildly moves the line in the dev harness.
	var marketID string
	err := tx.QueryRow(ctx, `
		INSERT INTO markets (market_type, title, state, currency, closes_at, created_by, dynamic_pricing, pricing_liquidity_cents)
		SELECT 'future', 'Mock Cup Winner', 'open', 'CAD', now() + interval '30 days', $1::uuid, true, 150000
		WHERE NOT EXISTS (SELECT 1 FROM markets WHERE title = 'Mock Cup Winner')
		RETURNING id::text`, createdBy).Scan(&marketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert mock market: %w", err)
	}
	selections := []struct {
		key, terms string
		odds       int32
	}{
		{"team-north", "Team North wins the Mock Cup", -120},
		{"team-south", "Team South wins the Mock Cup", 140},
	}
	for _, selection := range selections {
		if _, err := tx.Exec(ctx, `
			INSERT INTO selections (market_id, selection_key, display_terms, offered_american_odds, active)
			VALUES ($1::uuid, $2, $3, $4, true)`, marketID, selection.key, selection.terms, selection.odds); err != nil {
			return false, fmt.Errorf("insert mock selection %s: %w", selection.key, err)
		}
	}
	return true, nil
}

// valueOrDefaultLookup returns the looked-up value or a fallback when unset or
// blank, mirroring config's own helper without importing an unexported symbol.
func valueOrDefaultLookup(lookup lookupFunc, key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
