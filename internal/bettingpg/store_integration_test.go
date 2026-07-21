// Ledger rows are immutable (an UPDATE or DELETE on ledger_transactions or
// ledger_postings raises an exception, per the reject_ledger_mutation
// trigger in migrations/000001_initial.up.sql). These tests therefore cannot
// clean up the ledger rows they create. Every fixture below uses a random
// suffix so repeated runs never collide, and cleanup only removes the
// non-ledger rows (users, events, teams, matches, markets, selections,
// wagers, market_settlements, wager_settlements, outbox rows) that FK
// RESTRICT would otherwise leave orphaned. Run this suite only against a
// disposable database that is thrown away afterward; do not point it at a
// database anyone cares about keeping clean.
package bettingpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("BETTINGPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BETTINGPG_TEST_DATABASE_URL is not set")
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

// fixture bundles a self-contained, uniquely-suffixed match market with two
// selections and two funded users, ready to place and accept wagers against.
type fixture struct {
	Suffix                     string
	UserA, UserB               string
	EventID                    string
	MatchID                    string
	SideAID, SideBID           string
	MarketID                   string
	SelectionAID, SelectionBID string
	Currency                   ledger.Currency
}

// buildFixture creates users, an event, a match with two sides, and an open
// match-type market with two selections keyed "side:<sideID>", then funds
// both users with cash via a migration_adjustment ledger transaction. All
// rows are tagged with a unique suffix in their display names/slugs so
// concurrent test runs never collide.
func buildFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, openingBalanceCents int64) fixture {
	t.Helper()
	suffix := uniqueSuffix(t, ctx, pool)
	f := fixture{Suffix: suffix, Currency: ledger.CAD}

	f.UserA = mustScanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ($1, $2) RETURNING id::text`,
		"Fixture User A "+suffix, "fixture-a-"+suffix+"@example.test")
	f.UserB = mustScanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ($1, $2) RETURNING id::text`,
		"Fixture User B "+suffix, "fixture-b-"+suffix+"@example.test")
	admin := mustScanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ($1, $2) RETURNING id::text`,
		"Fixture Admin "+suffix, "fixture-admin-"+suffix+"@example.test")

	f.EventID = mustScanID(t, ctx, pool, `
		INSERT INTO events (slug, name, season_year, venue, starts_on, ends_on, created_by)
		VALUES ($1, $2, 2026, 'Fixture Venue', '2026-07-01', '2026-07-04', $3::uuid) RETURNING id::text`,
		"fixture-event-"+suffix, "Fixture Event "+suffix, admin)

	teamA := mustScanID(t, ctx, pool, `
		INSERT INTO teams (event_id, slug, name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		f.EventID, "team-a-"+suffix, "Team A "+suffix)
	teamB := mustScanID(t, ctx, pool, `
		INSERT INTO teams (event_id, slug, name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		f.EventID, "team-b-"+suffix, "Team B "+suffix)

	f.MatchID = mustScanID(t, ctx, pool, `
		INSERT INTO matches (event_id, match_number, format, state, created_by)
		VALUES ($1::uuid, 1, 'singles', 'verified', $2::uuid) RETURNING id::text`,
		f.EventID, admin)

	f.SideAID = mustScanID(t, ctx, pool, `
		INSERT INTO match_sides (event_id, match_id, side_number, team_id) VALUES ($1::uuid, $2::uuid, 1, $3::uuid) RETURNING id::text`,
		f.EventID, f.MatchID, teamA)
	f.SideBID = mustScanID(t, ctx, pool, `
		INSERT INTO match_sides (event_id, match_id, side_number, team_id) VALUES ($1::uuid, $2::uuid, 2, $3::uuid) RETURNING id::text`,
		f.EventID, f.MatchID, teamB)

	f.MarketID = mustScanID(t, ctx, pool, `
		INSERT INTO markets (market_type, match_id, title, state, currency, closes_at, created_by)
		VALUES ('match', $1::uuid, $2, 'open', 'CAD', now() + interval '1 hour', $3::uuid) RETURNING id::text`,
		f.MatchID, "Fixture Match Winner "+suffix, admin)

	f.SelectionAID = mustScanID(t, ctx, pool, `
		INSERT INTO selections (market_id, selection_key, display_terms, offered_american_odds, semantic_result_key, active)
		VALUES ($1::uuid, 'side-a', 'Team A to win', -110, $2, true) RETURNING id::text`,
		f.MarketID, "side:"+f.SideAID)
	f.SelectionBID = mustScanID(t, ctx, pool, `
		INSERT INTO selections (market_id, selection_key, display_terms, offered_american_odds, semantic_result_key, active)
		VALUES ($1::uuid, 'side-b', 'Team B to win', -110, $2, true) RETURNING id::text`,
		f.MarketID, "side:"+f.SideBID)

	fundUser(t, ctx, pool, f.UserA, f.Currency, openingBalanceCents, "fixture-fund:"+f.UserA+":"+suffix)
	fundUser(t, ctx, pool, f.UserB, f.Currency, openingBalanceCents, "fixture-fund:"+f.UserB+":"+suffix)

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id IN (
			SELECT id FROM markets WHERE match_id = $1::uuid
			UNION SELECT id FROM wagers WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)
		)`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM wager_settlements WHERE wager_id IN (SELECT id FROM wagers WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid))`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM market_settlement_outcomes WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM market_settlements WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM wagers WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE match_id = $1::uuid`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM match_sides WHERE match_id = $1::uuid`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM verified_results WHERE match_id = $1::uuid`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM matches WHERE id = $1::uuid`, f.MatchID)
		_, _ = pool.Exec(cctx, `DELETE FROM teams WHERE event_id = $1::uuid`, f.EventID)
		_, _ = pool.Exec(cctx, `DELETE FROM events WHERE id = $1::uuid`, f.EventID)
		// ledger_accounts, ledger_transactions, and ledger_postings for the
		// fixture users are left in place: the ledger is immutable and the
		// accounts are still referenced by RESTRICT foreign keys from those
		// rows, so users cannot be deleted either. This is the accepted
		// trade-off documented at the top of this file.
	})

	return f
}

// insertVerifiedResult records a verified result for the fixture match so a
// settlement can reference it, mirroring production where the result row and
// the MatchResultVerified event are written in the same transaction.
func insertVerifiedResult(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f fixture, outcome string) string {
	t.Helper()
	var side1, side2 float64
	switch outcome {
	case "side_1":
		side1, side2 = 1, 0
	case "side_2":
		side1, side2 = 0, 1
	case "tie":
		side1, side2 = 0.5, 0.5
	default:
		t.Fatalf("unsupported fixture outcome %q", outcome)
	}
	return mustScanID(t, ctx, pool, `
		INSERT INTO verified_results (match_id, version, side_1_points, side_2_points, outcome, verification_method)
		VALUES ($1::uuid, 1, $2, $3, $4, 'opponent') RETURNING id::text`,
		f.MatchID, side1, side2, outcome)
}

func fundUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, currency ledger.Currency, cents int64, idempotencyKey string) {
	t.Helper()
	var userAccountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO ledger_accounts (owner_user_id, account_type, currency, name)
		VALUES ($1::uuid, 'user_cash', $2, $3)
		ON CONFLICT (owner_user_id, account_type, currency) WHERE owner_user_id IS NOT NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, userID, string(currency), "Fixture cash "+userID).Scan(&userAccountID); err != nil {
		t.Fatalf("ensure user account: %v", err)
	}
	var equityAccountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, name)
		VALUES ('migration_equity', $1, 'Fixture equity')
		ON CONFLICT (account_type, currency) WHERE owner_user_id IS NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, string(currency)).Scan(&equityAccountID); err != nil {
		t.Fatalf("ensure equity account: %v", err)
	}
	// The ledger posting-count trigger is deferred to commit, so the
	// transaction row and its postings must land in one database transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture funding transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var transactionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
		(transaction_type, currency, idempotency_key, source_type, source_id, reason, expected_posting_count, occurred_at)
		VALUES ('migration_adjustment', $1, $2, 'fixture', NULL, 'fixture funding', 2, now())
		ON CONFLICT (currency, idempotency_key) DO NOTHING
		RETURNING id::text`, string(currency), idempotencyKey).Scan(&transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return // already funded by a previous run reusing this key (should not happen with unique suffixes)
	}
	if err != nil {
		t.Fatalf("insert fixture funding transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_postings (transaction_id, account_id, amount_cents) VALUES ($1::uuid, $2::uuid, $3), ($1::uuid, $4::uuid, $5)`,
		transactionID, userAccountID, cents, equityAccountID, -cents); err != nil {
		t.Fatalf("insert fixture funding postings: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture funding: %v", err)
	}
}

func uniqueSuffix(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `SELECT replace(gen_random_uuid()::text, '-', '')`).Scan(&id); err != nil {
		t.Fatalf("generate suffix: %v", err)
	}
	return id[:12]
}

func mustScanID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

func accountBalanceFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, accountType string, currency ledger.Currency) int64 {
	t.Helper()
	var balance int64
	err := pool.QueryRow(ctx, `
		SELECT coalesce(balance_cents, 0) FROM ledger_account_balances
		WHERE owner_user_id = $1::uuid AND account_type = $2 AND currency = $3`, userID, accountType, string(currency)).Scan(&balance)
	if err != nil {
		t.Fatalf("read balance for %s/%s: %v", userID, accountType, err)
	}
	return balance
}

func systemAccountBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountType string, currency ledger.Currency) int64 {
	t.Helper()
	// The system account row is created lazily on first use, so a fresh
	// database legitimately has no row yet: that is a zero balance.
	var balance int64
	err := pool.QueryRow(ctx, `
		SELECT coalesce((
			SELECT balance_cents FROM ledger_account_balances
			WHERE owner_user_id IS NULL AND account_type = $1 AND currency = $2
		), 0)`, accountType, string(currency)).Scan(&balance)
	if err != nil {
		t.Fatalf("read system balance for %s: %v", accountType, err)
	}
	return balance
}

func placeAndAccept(t *testing.T, ctx context.Context, store Store, f fixture, userID, selectionID string, stakeCents int64, wagerNumber int) betting.Wager {
	t.Helper()
	wagerID := mustNewUUID(t, ctx, store)
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID:            wagerID,
		UserID:             userID,
		MarketID:           f.MarketID,
		SelectionID:        selectionID,
		FundingAccountType: betting.FundingUserCash,
		StakeCents:         stakeCents,
		Currency:           f.Currency,
		IdempotencyKey:     fmt.Sprintf("test-place:%s:%d", f.Suffix, wagerNumber),
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	accepted, err := store.AcceptWager(ctx, string(wager.ID), adminActorFor(f))
	if err != nil {
		t.Fatalf("AcceptWager() error = %v", err)
	}
	return accepted
}

// adminActorFor returns a syntactically valid UUID actor for tests that
// don't otherwise need a real admin user row; ledger_transactions leaves
// actor_user_id NULL unless it resolves to an existing user, and the FK on
// wagers.accepted_by requires a real row, so tests use the fixture's real
// user IDs as actors instead of a synthetic one.
func adminActorFor(f fixture) string { return f.UserA }

func mustNewUUID(t *testing.T, ctx context.Context, store Store) string {
	t.Helper()
	id, err := betting.NewEventID()
	if err != nil {
		t.Fatalf("generate wager id: %v", err)
	}
	_ = ctx
	_ = store
	return string(id)
}

func TestAcceptWagerHappyPathAndRepeatIsNoOp(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	// The escrow account is a shared immutable ledger account, so other tests
	// (and earlier runs) legitimately leave balances behind: assert deltas.
	escrowBefore := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency)

	wagerID, err := betting.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID:            string(wagerID),
		UserID:             f.UserA,
		MarketID:           f.MarketID,
		SelectionID:        f.SelectionAID,
		FundingAccountType: betting.FundingUserCash,
		StakeCents:         1_000,
		Currency:           f.Currency,
		IdempotencyKey:     "accept-happy:" + f.Suffix,
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	if wager.State != betting.WagerPending {
		t.Fatalf("placed wager state = %v, want pending", wager.State)
	}

	accepted, err := store.AcceptWager(ctx, string(wager.ID), f.UserB)
	if err != nil {
		t.Fatalf("AcceptWager() error = %v", err)
	}
	if accepted.State != betting.WagerAccepted {
		t.Fatalf("accepted wager state = %v, want accepted", accepted.State)
	}

	userBalance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if userBalance != 10_000-1_000 {
		t.Fatalf("user balance after accept = %d, want %d", userBalance, 10_000-1_000)
	}
	escrowBalance := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency)
	if escrowBalance-escrowBefore != 1_000 {
		t.Fatalf("escrow delta after accept = %d, want %d", escrowBalance-escrowBefore, 1_000)
	}

	// Repeat acceptance must not debit a second time.
	acceptedAgain, err := store.AcceptWager(ctx, string(wager.ID), f.UserB)
	if err != nil {
		t.Fatalf("repeat AcceptWager() error = %v", err)
	}
	if acceptedAgain.State != betting.WagerAccepted {
		t.Fatalf("repeat accepted wager state = %v, want accepted", acceptedAgain.State)
	}
	userBalanceAfterRepeat := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if userBalanceAfterRepeat != userBalance {
		t.Fatalf("user balance after repeat accept = %d, want unchanged %d", userBalanceAfterRepeat, userBalance)
	}
}

func TestAcceptWagerInsufficientFunds(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500)
	// Remove the default credit line so a stake above the cash balance is
	// genuinely insufficient rather than covered by credit.
	if _, err := pool.Exec(ctx, `UPDATE users SET credit_limit_cents = 0 WHERE id = $1::uuid`, f.UserA); err != nil {
		t.Fatal(err)
	}

	wagerID, err := betting.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID:            string(wagerID),
		UserID:             f.UserA,
		MarketID:           f.MarketID,
		SelectionID:        f.SelectionAID,
		FundingAccountType: betting.FundingUserCash,
		StakeCents:         1_000,
		Currency:           f.Currency,
		IdempotencyKey:     "insufficient:" + f.Suffix,
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}

	if _, err := store.AcceptWager(ctx, string(wager.ID), f.UserB); err == nil {
		t.Fatalf("AcceptWager() error = nil, want ErrInsufficientFunds")
	} else if err != ErrInsufficientFunds {
		t.Fatalf("AcceptWager() error = %v, want ErrInsufficientFunds", err)
	}
}

func TestConsumerSettlesMatchAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	// Shared immutable system accounts accumulate across tests and runs, so
	// escrow and house are asserted as deltas from this snapshot.
	escrowBefore := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency)
	houseBefore := systemAccountBalance(t, ctx, pool, "house_clearing", f.Currency)

	winner := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	loser := placeAndAccept(t, ctx, store, f, f.UserB, f.SelectionBID, 1_000, 2)

	winnerProfit, err := winner.AcceptedOdds.Profit(winner.Stake)
	if err != nil {
		t.Fatal(err)
	}

	eventID, err := betting.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	envelope := events.Envelope{
		ID:               string(eventID),
		AggregateType:    "match",
		AggregateID:      f.MatchID,
		AggregateVersion: 1,
		Type:             events.MatchResultVerified,
		OccurredAt:       time.Now().UTC(),
	}
	payload := events.MatchResultVerifiedPayload{
		MatchID:        f.MatchID,
		WinningSideID:  f.SideAID,
		Outcome:        "side_win",
		VerificationID: insertVerifiedResult(t, ctx, pool, f, "side_1"),
		Score:          "2 & 1",
	}
	envelope.Payload = mustMarshal(t, payload)
	if err := envelope.Validate(); err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	if err := eventspg.Publish(ctx, pool, envelope, 5); err != nil {
		t.Fatalf("publish MatchResultVerified: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM event_receipts WHERE event_id = $1::uuid`, envelope.ID)
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE id = $1::uuid`, envelope.ID)
	})

	consumer := &MatchSettlementConsumer{Store: &store}
	if !consumer.Handles(events.MatchResultVerified) {
		t.Fatalf("Handles(MatchResultVerified) = false, want true")
	}
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	winnerBalance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if want := 10_000 - winner.Stake.Cents + winner.Stake.Cents + winnerProfit.Cents; winnerBalance != want {
		t.Fatalf("winner balance = %d, want %d", winnerBalance, want)
	}
	loserBalance := accountBalanceFor(t, ctx, pool, f.UserB, "user_cash", f.Currency)
	if want := 10_000 - loser.Stake.Cents; loserBalance != want {
		t.Fatalf("loser balance = %d, want %d", loserBalance, want)
	}
	houseBalance := systemAccountBalance(t, ctx, pool, "house_clearing", f.Currency)
	if houseBalance-houseBefore != loser.Stake.Cents-winnerProfit.Cents {
		t.Fatalf("house delta = %d, want %d", houseBalance-houseBefore, loser.Stake.Cents-winnerProfit.Cents)
	}
	escrowBalance := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency)
	if escrowBalance != escrowBefore {
		t.Fatalf("escrow delta after settlement = %d, want 0", escrowBalance-escrowBefore)
	}

	assertWagerState(t, ctx, pool, string(winner.ID), "settled")
	assertWagerState(t, ctx, pool, string(loser.ID), "settled")
	assertMarketState(t, ctx, pool, f.MarketID, "settled")

	var wagerSettlementCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_settlements ws JOIN market_settlements ms ON ms.id = ws.market_settlement_id WHERE ms.market_id = $1::uuid`, f.MarketID).Scan(&wagerSettlementCount); err != nil {
		t.Fatal(err)
	}
	if wagerSettlementCount != 2 {
		t.Fatalf("wager_settlements rows = %d, want 2", wagerSettlementCount)
	}

	assertOutboxContains(t, ctx, pool, f.MarketID, events.MarketSettled)
	assertOutboxContainsAggregate(t, ctx, pool, string(winner.ID), events.WagerSettled)
	assertOutboxContainsAggregate(t, ctx, pool, string(loser.ID), events.WagerSettled)

	// Every posting for this settlement must net to zero, in whole cents.
	var postingSum int64
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(sum(p.amount_cents), 0) FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE t.idempotency_key IN ($1, $2)`,
		fmt.Sprintf("wager:%s:settlement:v1", winner.ID), fmt.Sprintf("wager:%s:settlement:v1", loser.ID)).Scan(&postingSum); err != nil {
		t.Fatal(err)
	}
	if postingSum != 0 {
		t.Fatalf("settlement postings sum = %d, want 0", postingSum)
	}

	// Redelivery of the identical event must be fully idempotent.
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("repeat Handle() error = %v", err)
	}
	winnerBalanceAfterRepeat := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if winnerBalanceAfterRepeat != winnerBalance {
		t.Fatalf("winner balance after repeat handle = %d, want unchanged %d", winnerBalanceAfterRepeat, winnerBalance)
	}
	loserBalanceAfterRepeat := accountBalanceFor(t, ctx, pool, f.UserB, "user_cash", f.Currency)
	if loserBalanceAfterRepeat != loserBalance {
		t.Fatalf("loser balance after repeat handle = %d, want unchanged %d", loserBalanceAfterRepeat, loserBalance)
	}
}

func TestSettleMatchMarketsTieWithoutTieSelectionRefundsEveryone(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	wagerA := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	wagerB := placeAndAccept(t, ctx, store, f, f.UserB, f.SelectionBID, 1_000, 2)

	report, err := store.SettleMatchMarkets(ctx, f.MatchID, "tie", "", insertVerifiedResult(t, ctx, pool, f, "tie"), "system:settlement-worker")
	if err != nil {
		t.Fatalf("SettleMatchMarkets() error = %v", err)
	}
	if len(report.Settled) != 1 || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v, want 1 settled, 0 skipped", report)
	}

	balanceA := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if balanceA != 10_000 {
		t.Fatalf("user A balance after push = %d, want 10000 (full refund)", balanceA)
	}
	balanceB := accountBalanceFor(t, ctx, pool, f.UserB, "user_cash", f.Currency)
	if balanceB != 10_000 {
		t.Fatalf("user B balance after push = %d, want 10000 (full refund)", balanceB)
	}
	assertWagerState(t, ctx, pool, string(wagerA.ID), "settled")
	assertWagerState(t, ctx, pool, string(wagerB.ID), "settled")

	var pushCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_settlements WHERE result = 'push' AND wager_id IN ($1::uuid, $2::uuid)`, wagerA.ID, wagerB.ID).Scan(&pushCount); err != nil {
		t.Fatal(err)
	}
	if pushCount != 2 {
		t.Fatalf("push settlement count = %d, want 2", pushCount)
	}
}

func TestSettleMatchMarketsSkipsUnrecognizedSemanticKey(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	if _, err := pool.Exec(ctx, `UPDATE selections SET semantic_result_key = 'handicap:9' WHERE id = $1::uuid`, f.SelectionAID); err != nil {
		t.Fatalf("corrupt semantic key: %v", err)
	}

	report, err := store.SettleMatchMarkets(ctx, f.MatchID, "side_win", f.SideAID, "", "system:settlement-worker")
	if err != nil {
		t.Fatalf("SettleMatchMarkets() error = %v", err)
	}
	if len(report.Settled) != 0 || len(report.Skipped) != 1 || report.Skipped[0] != f.MarketID {
		t.Fatalf("report = %+v, want market %s skipped and none settled", report, f.MarketID)
	}
	assertMarketState(t, ctx, pool, f.MarketID, "closed") // auto-closed, but not settled
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func assertWagerState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT state FROM wagers WHERE id = $1::uuid`, wagerID).Scan(&got); err != nil {
		t.Fatalf("read wager state: %v", err)
	}
	if got != want {
		t.Fatalf("wager %s state = %q, want %q", wagerID, got, want)
	}
}

func assertMarketState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marketID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT state FROM markets WHERE id = $1::uuid`, marketID).Scan(&got); err != nil {
		t.Fatalf("read market state: %v", err)
	}
	if got != want {
		t.Fatalf("market %s state = %q, want %q", marketID, got, want)
	}
}

func assertOutboxContains(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aggregateID string, eventType events.Type) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1::uuid AND event_type = $2`, aggregateID, string(eventType)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatalf("outbox has no %s event for aggregate %s", eventType, aggregateID)
	}
}

func assertOutboxContainsAggregate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aggregateID string, eventType events.Type) {
	assertOutboxContains(t, ctx, pool, aggregateID, eventType)
}
