package legacybook

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresSourceIntegration(t *testing.T) {
	databaseURL := os.Getenv("LEGACYBOOK_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LEGACYBOOK_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(ctx) }()

	report, err := Reconcile(ctx, PostgresSource{DB: connection})
	if err != nil {
		t.Fatal(err)
	}
	if report.UserCount == 0 || report.TransactionCount == 0 {
		t.Fatalf("archive unexpectedly empty: users=%d transactions=%d", report.UserCount, report.TransactionCount)
	}
	if !report.Promotable() {
		t.Fatalf("archive has blocking reconciliation issues: %+v", report.Issues)
	}
}

func TestPostgresPromotionIntegration(t *testing.T) {
	databaseURL := os.Getenv("LEGACYBOOK_PROMOTION_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LEGACYBOOK_PROMOTION_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	var archiveExists bool
	if err := connection.QueryRow(ctx, `SELECT to_regnamespace('legacy_cabot_book') IS NOT NULL`).Scan(&archiveExists); err != nil {
		t.Fatal(err)
	}
	if archiveExists {
		t.Skip("promotion integration requires a disposable database without a legacy archive schema")
	}
	_, err = connection.Exec(ctx, `
		CREATE SCHEMA legacy_cabot_book;
		CREATE TABLE legacy_cabot_book.users (
			user_id bigint PRIMARY KEY, username text, email text, role text,
			balance numeric, free_play_balance numeric
		);
		CREATE TABLE legacy_cabot_book.transactions (
			transaction_id bigint PRIMARY KEY, user_id bigint, amount numeric,
			transaction_type text, description text, transaction_date timestamptz
		);
		CREATE TABLE legacy_cabot_book.user_bets (
			user_bet_id bigint PRIMARY KEY, user_id bigint, amount numeric,
			bet_description text, odds integer, placed_at timestamptz, result text, approved boolean
		);
		INSERT INTO legacy_cabot_book.users VALUES
			(900001, 'Migration Member', 'migration-member@example.test', 'user', 10.00, 0),
			(900002, 'Migration Admin', 'migration-admin@example.test', 'admin', -5.00, 0);
		INSERT INTO legacy_cabot_book.transactions VALUES
			(910001, 900001, 8.00, 'credit', 'Fixture credit', '2024-01-01T12:00:00Z'),
			(910002, 900002, -5.00, 'debit', 'Fixture debit', '2024-01-02T12:00:00Z');
		INSERT INTO legacy_cabot_book.user_bets VALUES
			(920001, 900001, 2.00, 'Fixture wager', 150, '2024-01-03T12:00:00Z', 'win', true)`)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Reconcile(ctx, PostgresSource{DB: connection})
	if err != nil {
		t.Fatal(err)
	}
	var actorID, batchID string
	if err := connection.QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'legacy-public-import@cabotcup.invalid'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `
		INSERT INTO migration_batches (source_system, source_version, state)
		VALUES ($1, $2, 'validated') RETURNING id::text`, SourceSystem, strings.Repeat("a", 64)).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	options := PromoteOptions{BatchID: batchID, Currency: SourceCurrency, ActorUserID: actorID, SourceVersion: strings.Repeat("a", 64)}
	for run := 1; run <= 2; run++ {
		result, err := Promote(ctx, PostgresStore{DB: connection}, report, options)
		if err != nil {
			t.Fatalf("promotion run %d: %v", run, err)
		}
		if result.Users != 2 || result.Transactions != 2 || result.Wagers != 1 {
			t.Fatalf("promotion run %d result = %+v", run, result)
		}
	}

	var users, mappings, transactions, wagers, ledgerTransactions, postings, audits int
	var postingTotal, cashTotal, differenceTotal int64
	var batchState string
	err = connection.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM users WHERE email LIKE 'migration-%@example.test'),
			(SELECT count(*) FROM legacy_book_user_mappings WHERE currency = 'CAD'),
			(SELECT count(*) FROM legacy_book_transactions WHERE currency = 'CAD'),
			(SELECT count(*) FROM legacy_book_wagers WHERE currency = 'CAD'),
			(SELECT count(*) FROM ledger_transactions WHERE source_type = $1),
			(SELECT count(*) FROM ledger_postings p JOIN ledger_transactions t ON t.id = p.transaction_id WHERE t.source_type = $1),
			(SELECT count(*) FROM audit_entries WHERE action = 'legacy_book.promoted' AND target_id = $2::uuid),
			(SELECT coalesce(sum(p.amount_cents), 0) FROM ledger_postings p JOIN ledger_transactions t ON t.id = p.transaction_id WHERE t.source_type = $1),
			(SELECT coalesce(sum(balance_cents), 0) FROM ledger_account_balances WHERE account_type = 'user_cash' AND owner_user_id IN (SELECT user_id FROM legacy_book_user_mappings)),
			(SELECT coalesce(sum(reconciliation_difference_cents), 0) FROM legacy_book_user_mappings),
			(SELECT state FROM migration_batches WHERE id = $2::uuid)`, SourceSystem, batchID).
		Scan(&users, &mappings, &transactions, &wagers, &ledgerTransactions, &postings, &audits,
			&postingTotal, &cashTotal, &differenceTotal, &batchState)
	if err != nil {
		t.Fatal(err)
	}
	if users != 2 || mappings != 2 || transactions != 2 || wagers != 1 || ledgerTransactions != 2 || postings != 4 || audits != 1 || postingTotal != 0 || cashTotal != 500 || differenceTotal != 200 || batchState != "promoted" {
		t.Fatalf("promotion aggregates users=%d mappings=%d transactions=%d wagers=%d ledger=%d postings=%d audits=%d posting_total=%d cash=%d difference=%d state=%s",
			users, mappings, transactions, wagers, ledgerTransactions, postings, audits, postingTotal, cashTotal, differenceTotal, batchState)
	}
}
