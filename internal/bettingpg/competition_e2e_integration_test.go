package bettingpg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strconv"
)

// TestMatchResultDrivesSettlementEndToEnd is the crux of the match-driven,
// event-based model: creating a match, attaching a betting market to it,
// accepting a wager, then recording the verified result must publish
// MatchResultVerified, which the settlement consumer turns into a settled,
// paid-out wager — with no direct call to the settlement code.
func TestMatchResultDrivesSettlementEndToEnd(t *testing.T) {
	databaseURL := os.Getenv("BETTINGPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BETTINGPG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	betStore := bettingpg.Store{DB: pool}
	compStore := competitionpg.Store{Pool: pool}
	suffix := time.Now().UTC().UnixNano()

	admin := scanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ('E2E Admin', $1) RETURNING id::text`,
		email("e2e-admin", suffix))
	bettor := scanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ('E2E Bettor', $1) RETURNING id::text`,
		email("e2e-bettor", suffix))
	fundE2E(t, ctx, pool, bettor, 500_00)

	eventID, err := compStore.CreateEvent(ctx, competitionpg.CreateEventRequest{
		Name: "E2E Cup " + itoa(suffix), Venue: "Test Links", SeasonYear: 2026, CreatedBy: admin,
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	teamA, err := compStore.CreateTeam(ctx, eventID, "North "+itoa(suffix), admin)
	if err != nil {
		t.Fatal(err)
	}
	teamB, err := compStore.CreateTeam(ctx, eventID, "South "+itoa(suffix), admin)
	if err != nil {
		t.Fatal(err)
	}
	match, err := compStore.CreateMatch(ctx, eventID, "singles", teamA, teamB, admin)
	if err != nil {
		t.Fatalf("CreateMatch() error = %v", err)
	}

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid OR aggregate_id IN (SELECT id FROM markets WHERE match_id = $1::uuid) OR aggregate_id IN (SELECT id FROM wagers WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid))`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM event_receipts`)
		_, _ = pool.Exec(c, `DELETE FROM wager_settlements WHERE wager_id IN (SELECT id FROM wagers WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid))`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM market_settlement_outcomes WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM market_settlements WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM wagers WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM selections WHERE market_id IN (SELECT id FROM markets WHERE match_id = $1::uuid)`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM markets WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM verified_results WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM match_sides WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM matches WHERE id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM teams WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1::uuid`, eventID)
	})

	// A match-type market whose selections are keyed to the match sides.
	marketID := newEventID(t)
	if _, err := betStore.CreateMarket(ctx, bettingpg.CreateMarketRequest{
		MarketID: marketID, Type: betting.MarketMatch, MatchID: match.MatchID, Title: "E2E match winner",
		Currency: ledger.CAD, ClosesAt: time.Now().UTC().Add(2 * time.Hour),
		Selections: []bettingpg.CreateMarketSelection{
			{Key: "north", DisplayTerms: "North wins", OfferedAmericanOdds: 100, SemanticResultKey: "side:" + match.Side1ID},
			{Key: "south", DisplayTerms: "South wins", OfferedAmericanOdds: 100, SemanticResultKey: "side:" + match.Side2ID},
		},
		ActorUserID: admin,
	}); err != nil {
		t.Fatalf("CreateMarket() error = %v", err)
	}
	if err := betStore.OpenMarket(ctx, marketID, admin); err != nil {
		t.Fatal(err)
	}
	northSelection := scanID(t, ctx, pool, `SELECT id::text FROM selections WHERE market_id = $1::uuid AND semantic_result_key = $2`, marketID, "side:"+match.Side1ID)

	// Bettor backs North for $100 at +100 and it is accepted.
	wagerID := newEventID(t)
	if _, err := betStore.PlaceWager(ctx, bettingpg.PlaceWagerRequest{
		WagerID: wagerID, UserID: bettor, MarketID: marketID, SelectionID: northSelection,
		FundingAccountType: betting.FundingUserCash, StakeCents: 100_00, Currency: ledger.CAD,
		IdempotencyKey: "e2e-place:" + itoa(suffix),
	}); err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	if _, err := betStore.AcceptWager(ctx, wagerID, admin); err != nil {
		t.Fatalf("AcceptWager() error = %v", err)
	}

	// Recording the result publishes MatchResultVerified — no settlement call.
	if _, err := compStore.RecordAdminResult(ctx, competitionpg.RecordResultRequest{
		MatchID: match.MatchID, Winner: "side_1", Score: "3 & 2", ActorUserID: admin, Reason: "final scorecard",
	}); err != nil {
		t.Fatalf("RecordAdminResult() error = %v", err)
	}

	// Drive the published event through the real settlement consumer.
	envelope := loadOutboxEnvelope(t, ctx, pool, match.MatchID, events.MatchResultVerified)
	consumer := &bettingpg.MatchSettlementConsumer{Store: &betStore}
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("settlement consumer Handle() error = %v", err)
	}

	// North won at +100: the $100 stake returns $200 to the bettor.
	var wagerState string
	if err := pool.QueryRow(ctx, `SELECT state FROM wagers WHERE id = $1::uuid`, wagerID).Scan(&wagerState); err != nil {
		t.Fatal(err)
	}
	if wagerState != "settled" {
		t.Fatalf("wager state = %s, want settled", wagerState)
	}
	var balance int64
	if err := pool.QueryRow(ctx, `
		SELECT balance_cents FROM ledger_account_balances b JOIN users u ON u.id = b.owner_user_id
		WHERE u.id = $1::uuid AND b.account_type = 'user_cash' AND b.currency = 'CAD'`, bettor).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	// Started $500, staked $100 (to escrow), won back $200: 500 - 100 + 200 = 600.
	if balance != 600_00 {
		t.Fatalf("bettor balance = %d, want 60000 (won the match market from the verified result)", balance)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func email(prefix string, suffix int64) string {
	return fmt.Sprintf("%s-%d@example.test", prefix, suffix)
}

func newEventID(t *testing.T) string {
	t.Helper()
	id, err := betting.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	return string(id)
}

func scanID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("scan id: %v", err)
	}
	return id
}

func fundE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, cents int64) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var cash, equity string
	if err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (owner_user_id, account_type, currency, name)
		VALUES ($1::uuid, 'user_cash', 'CAD', $2)
		ON CONFLICT (owner_user_id, account_type, currency) WHERE owner_user_id IS NOT NULL
		DO UPDATE SET name = ledger_accounts.name RETURNING id::text`, userID, "E2E cash "+userID).Scan(&cash); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, name)
		VALUES ('migration_equity', 'CAD', 'E2E equity')
		ON CONFLICT (account_type, currency) WHERE owner_user_id IS NULL
		DO UPDATE SET name = ledger_accounts.name RETURNING id::text`).Scan(&equity); err != nil {
		t.Fatal(err)
	}
	var txID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions (transaction_type, currency, idempotency_key, source_type, reason, expected_posting_count, occurred_at)
		VALUES ('migration_adjustment', 'CAD', $1, 'e2e', 'e2e funding', 2, now()) RETURNING id::text`,
		"e2e-fund:"+userID).Scan(&txID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_postings (transaction_id, account_id, amount_cents) VALUES ($1::uuid, $2::uuid, $3), ($1::uuid, $4::uuid, $5)`,
		txID, cash, cents, equity, -cents); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func loadOutboxEnvelope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aggregateID string, eventType events.Type) events.Envelope {
	t.Helper()
	var env events.Envelope
	var payload []byte
	if err := pool.QueryRow(ctx, `
		SELECT id::text, aggregate_type, aggregate_id::text, aggregate_version, event_type, payload, occurred_at
		FROM outbox_events WHERE aggregate_id = $1::uuid AND event_type = $2
		ORDER BY occurred_at DESC LIMIT 1`, aggregateID, string(eventType)).Scan(
		&env.ID, &env.AggregateType, &env.AggregateID, &env.AggregateVersion, &env.Type, &payload, &env.OccurredAt); err != nil {
		t.Fatalf("load outbox envelope: %v", err)
	}
	env.Payload = payload
	return env
}
