package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// secondMatchMarket builds another match market on the same event so a parlay
// has a second, uncorrelated leg to ride on.
func secondMatchMarket(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f fixture) (marketID, selA, selB string) {
	t.Helper()
	admin := mustScanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ($1, $2) RETURNING id::text`,
		"Parlay Admin "+f.Suffix, "parlay-admin-"+f.Suffix+"@example.test")
	matchID := mustScanID(t, ctx, pool, `
		INSERT INTO matches (event_id, match_number, format, state, created_by)
		VALUES ($1::uuid, 2, 'singles', 'verified', $2::uuid) RETURNING id::text`, f.EventID, admin)
	sideA := mustScanID(t, ctx, pool, `
		INSERT INTO match_sides (event_id, match_id, side_number, team_id)
		VALUES ($1::uuid, $2::uuid, 1, (SELECT team_id FROM match_sides WHERE id = $3::uuid)) RETURNING id::text`,
		f.EventID, matchID, f.SideAID)
	sideB := mustScanID(t, ctx, pool, `
		INSERT INTO match_sides (event_id, match_id, side_number, team_id)
		VALUES ($1::uuid, $2::uuid, 2, (SELECT team_id FROM match_sides WHERE id = $3::uuid)) RETURNING id::text`,
		f.EventID, matchID, f.SideBID)
	marketID = mustScanID(t, ctx, pool, `
		INSERT INTO markets (market_type, match_id, title, state, currency, closes_at, created_by)
		VALUES ('match', $1::uuid, $2, 'open', 'CAD', now() + interval '1 hour', $3::uuid) RETURNING id::text`,
		matchID, "Parlay Second Match "+f.Suffix, admin)
	selA = mustScanID(t, ctx, pool, `
		INSERT INTO selections (market_id, selection_key, display_terms, offered_american_odds, semantic_result_key, active)
		VALUES ($1::uuid, 'side-a', 'Second Team A to win', 100, $2, true) RETURNING id::text`, marketID, "side:"+sideA)
	selB = mustScanID(t, ctx, pool, `
		INSERT INTO selections (market_id, selection_key, display_terms, offered_american_odds, semantic_result_key, active)
		VALUES ($1::uuid, 'side-b', 'Second Team B to win', 100, $2, true) RETURNING id::text`, marketID, "side:"+sideB)

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM parlay_legs WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM market_settlement_outcomes WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM market_settlements WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM match_sides WHERE match_id = $1::uuid`, matchID)
		_, _ = pool.Exec(cctx, `DELETE FROM matches WHERE id = $1::uuid`, matchID)
	})
	return marketID, selA, selB
}

func cleanupParlay(t *testing.T, pool *pgxpool.Pool, parlayID string) {
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM parlay_settlements WHERE parlay_id = $1::uuid`, parlayID)
		_, _ = pool.Exec(cctx, `DELETE FROM parlay_legs WHERE parlay_id = $1::uuid`, parlayID)
		_, _ = pool.Exec(cctx, `DELETE FROM parlays WHERE id = $1::uuid`, parlayID)
	})
}

// TestParlayWinsWhenEveryLegLands walks the whole life of a winning parlay:
// placed, accepted into escrow, both legs graded by their own markets, paid.
func TestParlayWinsWhenEveryLegLands(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	parlayID := mustNewUUID(t, ctx, store)
	cleanupParlay(t, pool, parlayID)
	placed, err := store.PlaceParlay(ctx, PlaceParlayRequest{
		ParlayID: parlayID, UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: market2, SelectionID: sel2A},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 2_000, Currency: ledger.CAD,
		IdempotencyKey: "parlay-win:" + parlayID,
	})
	if err != nil {
		t.Fatalf("PlaceParlay() error = %v", err)
	}
	if placed.State != betting.WagerPending || len(placed.Legs) != 2 {
		t.Fatalf("placed parlay = %+v", placed)
	}
	// -110 with +100 is shorter than either leg alone would suggest but longer
	// than even money; the exact price is the domain's business, so assert only
	// that it is a real posted line.
	if placed.AcceptedOdds < 100 {
		t.Fatalf("combined odds = %d, want a posted line of at least +100", placed.AcceptedOdds)
	}

	if _, err := store.AcceptParlay(ctx, parlayID, f.UserB); err != nil {
		t.Fatalf("AcceptParlay() error = %v", err)
	}
	escrow := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency)

	// Both legs win.
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "first leg in",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	}); err != nil {
		t.Fatalf("settle first leg: %v", err)
	}
	// Still open: one leg to come, so nothing may have paid yet.
	if state := parlayState(t, ctx, pool, parlayID); state != string(betting.WagerAccepted) {
		t.Fatalf("parlay state after one leg = %q, want accepted", state)
	}

	selections2 := selectionsFor(t, ctx, pool, market2)
	outcome2 := map[string]betting.SettlementResult{}
	for _, id := range selections2 {
		outcome2[id] = betting.ResultLoss
	}
	outcome2[sel2A] = betting.ResultWin
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: market2, ActorUserID: f.UserB, Reason: "second leg in", Outcome: outcome2,
	}); err != nil {
		t.Fatalf("settle second leg: %v", err)
	}

	if state := parlayState(t, ctx, pool, parlayID); state != "settled" {
		t.Fatalf("parlay state = %q, want settled", state)
	}
	var result string
	var profit, returned int64
	if err := pool.QueryRow(ctx,
		`SELECT result, profit_cents, returned_cents FROM parlay_settlements WHERE parlay_id = $1::uuid`,
		parlayID).Scan(&result, &profit, &returned); err != nil {
		t.Fatalf("load parlay settlement: %v", err)
	}
	if result != string(betting.ResultWin) || profit <= 0 || returned != 2_000+profit {
		t.Fatalf("settlement result=%s profit=%d returned=%d", result, profit, returned)
	}
	// The stake left escrow again, so escrow is back where it started.
	if after := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency); after != escrow-2_000 {
		t.Fatalf("escrow = %d, want %d after the stake was released", after, escrow-2_000)
	}
}

// TestParlayDiesOnTheFirstLosingLeg is the rule that makes a parlay a parlay.
// It must also not wait on the remaining legs: the bet is already dead, and
// holding the stake in escrow past that point serves nobody.
func TestParlayDiesOnTheFirstLosingLeg(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	parlayID := mustNewUUID(t, ctx, store)
	cleanupParlay(t, pool, parlayID)
	if _, err := store.PlaceParlay(ctx, PlaceParlayRequest{
		ParlayID: parlayID, UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: market2, SelectionID: sel2A},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_500, Currency: ledger.CAD,
		IdempotencyKey: "parlay-loss:" + parlayID,
	}); err != nil {
		t.Fatalf("PlaceParlay() error = %v", err)
	}
	if _, err := store.AcceptParlay(ctx, parlayID, f.UserB); err != nil {
		t.Fatalf("AcceptParlay() error = %v", err)
	}

	// The first leg loses, and the second market is never settled at all.
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "first leg out",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultLoss, f.SelectionBID: betting.ResultWin,
		},
	}); err != nil {
		t.Fatalf("settle first leg: %v", err)
	}

	if state := parlayState(t, ctx, pool, parlayID); state != "settled" {
		t.Fatalf("parlay state = %q, want settled without waiting on the open leg", state)
	}
	var result string
	var returned int64
	if err := pool.QueryRow(ctx,
		`SELECT result, returned_cents FROM parlay_settlements WHERE parlay_id = $1::uuid`,
		parlayID).Scan(&result, &returned); err != nil {
		t.Fatal(err)
	}
	if result != string(betting.ResultLoss) || returned != 0 {
		t.Fatalf("settlement result=%s returned=%d, want loss returning nothing", result, returned)
	}
}

// TestParlayRepricesWhenALegVoidsOut covers the case every book handles the
// same way: a voided leg drops out and the parlay pays as the smaller parlay it
// became, rather than losing or paying the original price.
func TestParlayRepricesWhenALegVoidsOut(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	parlayID := mustNewUUID(t, ctx, store)
	cleanupParlay(t, pool, parlayID)
	placed, err := store.PlaceParlay(ctx, PlaceParlayRequest{
		ParlayID: parlayID, UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: market2, SelectionID: sel2A},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 3_000, Currency: ledger.CAD,
		IdempotencyKey: "parlay-void:" + parlayID,
	})
	if err != nil {
		t.Fatalf("PlaceParlay() error = %v", err)
	}
	if _, err := store.AcceptParlay(ctx, parlayID, f.UserB); err != nil {
		t.Fatalf("AcceptParlay() error = %v", err)
	}

	// Leg one wins outright.
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "leg one wins",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Leg two's market is voided entirely, so that leg drops out.
	if _, err := store.VoidMarket(ctx, VoidMarketRequest{
		MarketID: market2, ActorUserID: f.UserB, Reason: "match abandoned",
	}); err != nil {
		t.Fatalf("VoidMarket() error = %v", err)
	}

	var result string
	var settledOdds int32
	var profit int64
	if err := pool.QueryRow(ctx,
		`SELECT result, settled_american_odds, profit_cents FROM parlay_settlements WHERE parlay_id = $1::uuid`,
		parlayID).Scan(&result, &settledOdds, &profit); err != nil {
		t.Fatalf("load parlay settlement: %v", err)
	}
	if result != string(betting.ResultWin) {
		t.Fatalf("result = %s, want win on the surviving leg", result)
	}
	// One surviving leg is no longer a parlay: it pays that leg's own price,
	// with no parlay juice and shorter than the two-leg price it was struck at.
	if ledger.AmericanOdds(settledOdds) != -110 {
		t.Fatalf("settled odds = %d, want the surviving leg's own -110", settledOdds)
	}
	if ledger.AmericanOdds(settledOdds) == placed.AcceptedOdds {
		t.Fatal("a voided leg did not reprice the parlay")
	}
}

// TestParlayRefusesANonMatchLeg is the scope rule: props and futures can be
// entangled with a match result, and a book that cannot see the correlation
// between two legs writes free money.
func TestParlayRefusesANonMatchLeg(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	futureMarket, futureSelections := cappedMarket(t, ctx, store, 0, f.UserB)

	parlayID := mustNewUUID(t, ctx, store)
	cleanupParlay(t, pool, parlayID)
	_, err := store.PlaceParlay(ctx, PlaceParlayRequest{
		ParlayID: parlayID, UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: futureMarket, SelectionID: futureSelections[0]},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "parlay-future:" + parlayID,
	})
	if !errors.Is(err, betting.ErrParlayMarketNotEligible) {
		t.Fatalf("error = %v, want ErrParlayMarketNotEligible", err)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM parlays WHERE id = $1::uuid`, parlayID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatal("a refused parlay was still written")
	}
}

// TestParlayPlacementIsIdempotent matches the guarantee a single wager gives:
// a resubmitted key returns the same parlay rather than striking a second one.
func TestParlayPlacementIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	parlayID := mustNewUUID(t, ctx, store)
	cleanupParlay(t, pool, parlayID)
	request := PlaceParlayRequest{
		ParlayID: parlayID, UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: market2, SelectionID: sel2A},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "parlay-idem:" + parlayID,
	}
	first, err := store.PlaceParlay(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second := request
	second.ParlayID = mustNewUUID(t, ctx, store)
	again, err := store.PlaceParlay(ctx, second)
	if err != nil {
		t.Fatalf("repeat PlaceParlay() error = %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("repeat placed a second parlay: %s vs %s", again.ID, first.ID)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM parlays WHERE user_id = $1::uuid AND idempotency_key = $2`,
		f.UserA, request.IdempotencyKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("parlays stored = %d, want 1", count)
	}
}

func parlayState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, parlayID string) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM parlays WHERE id = $1::uuid`, parlayID).Scan(&state); err != nil {
		t.Fatalf("load parlay state: %v", err)
	}
	return state
}

func selectionsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marketID string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id::text FROM selections WHERE market_id = $1::uuid`, marketID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestParlayCannotBeAutoApproved is the rule the owner asked for: every parlay
// is looked at by a person. A small stake across several legs is where the
// book's liability runs away from it, so there must be no path that accepts one
// without a named admin — including the auto-approve marker singles use.
func TestParlayCannotBeAutoApproved(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	parlayID := mustNewUUID(t, ctx, store)
	cleanupParlay(t, pool, parlayID)
	if _, err := store.PlaceParlay(ctx, PlaceParlayRequest{
		ParlayID: parlayID, UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: market2, SelectionID: sel2A},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 100, Currency: ledger.CAD,
		IdempotencyKey: "parlay-noauto:" + parlayID,
	}); err != nil {
		t.Fatalf("PlaceParlay() error = %v", err)
	}

	for _, actor := range []string{"", AutoApproveActor, "system:anything"} {
		if _, err := store.AcceptParlay(ctx, parlayID, actor); !errors.Is(err, betting.ErrUnauthorized) {
			t.Fatalf("AcceptParlay(actor=%q) error = %v, want ErrUnauthorized", actor, err)
		}
	}
	if state := parlayState(t, ctx, pool, parlayID); state != string(betting.WagerPending) {
		t.Fatalf("parlay state = %q, want it left pending", state)
	}
	// A named admin still works.
	if _, err := store.AcceptParlay(ctx, parlayID, f.UserB); err != nil {
		t.Fatalf("AcceptParlay(admin) error = %v", err)
	}
	var acceptedBy *string
	if err := pool.QueryRow(ctx, `SELECT accepted_by::text FROM parlays WHERE id = $1::uuid`, parlayID).Scan(&acceptedBy); err != nil {
		t.Fatal(err)
	}
	if acceptedBy == nil || *acceptedBy != f.UserB {
		t.Fatal("the accepting admin was not recorded on the parlay")
	}
}

// TestQuoteParlayPricesWithoutStoringAnything backs the live slip: the member
// sees a real price as legs go in, and asking for one must not leave a parlay,
// a leg, or a ledger entry behind.
func TestQuoteParlayPricesWithoutStoringAnything(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	var parlaysBefore, legsBefore int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM parlays), (SELECT count(*) FROM parlay_legs)`).
		Scan(&parlaysBefore, &legsBefore); err != nil {
		t.Fatal(err)
	}

	quote, err := store.QuoteParlay(ctx, PlaceParlayRequest{
		UserID: f.UserA,
		Legs: []ParlayLegRequest{
			{MarketID: f.MarketID, SelectionID: f.SelectionAID},
			{MarketID: market2, SelectionID: sel2A},
		},
		FundingAccountType: betting.FundingUserCash, StakeCents: 2_500, Currency: ledger.CAD,
	})
	if err != nil {
		t.Fatalf("QuoteParlay() error = %v", err)
	}
	if len(quote.Legs) != 2 || quote.AcceptedOdds < 100 || quote.PotentialProfit <= 0 {
		t.Fatalf("quote = %+v", quote)
	}

	var parlaysAfter, legsAfter int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM parlays), (SELECT count(*) FROM parlay_legs)`).
		Scan(&parlaysAfter, &legsAfter); err != nil {
		t.Fatal(err)
	}
	if parlaysAfter != parlaysBefore || legsAfter != legsBefore {
		t.Fatalf("a quote wrote rows: parlays %d->%d legs %d->%d", parlaysBefore, parlaysAfter, legsBefore, legsAfter)
	}

	// One leg is not a parlay, and the slip must say so rather than pricing it.
	if _, err := store.QuoteParlay(ctx, PlaceParlayRequest{
		UserID:             f.UserA,
		Legs:               []ParlayLegRequest{{MarketID: f.MarketID, SelectionID: f.SelectionAID}},
		FundingAccountType: betting.FundingUserCash, StakeCents: 2_500, Currency: ledger.CAD,
	}); !errors.Is(err, betting.ErrParlayTooFewLegs) {
		t.Fatalf("single-leg quote error = %v, want ErrParlayTooFewLegs", err)
	}
}

// TestCancelParlayIsOwnerOnlyAndPendingOnly keeps one member from withdrawing
// another's bet, and keeps anybody from pulling a stake back out of escrow.
func TestCancelParlayIsOwnerOnlyAndPendingOnly(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	market2, sel2A, _ := secondMatchMarket(t, ctx, pool, f)

	place := func(tag string) string {
		id := mustNewUUID(t, ctx, store)
		cleanupParlay(t, pool, id)
		if _, err := store.PlaceParlay(ctx, PlaceParlayRequest{
			ParlayID: id, UserID: f.UserA,
			Legs: []ParlayLegRequest{
				{MarketID: f.MarketID, SelectionID: f.SelectionAID},
				{MarketID: market2, SelectionID: sel2A},
			},
			FundingAccountType: betting.FundingUserCash, StakeCents: 500, Currency: ledger.CAD,
			IdempotencyKey: tag + ":" + id,
		}); err != nil {
			t.Fatalf("PlaceParlay() error = %v", err)
		}
		return id
	}

	other := place("cancel-other")
	if err := store.CancelParlay(ctx, other, f.UserB); !errors.Is(err, betting.ErrUnauthorized) {
		t.Fatalf("another member cancelled it: %v", err)
	}

	own := place("cancel-own")
	if err := store.CancelParlay(ctx, own, f.UserA); err != nil {
		t.Fatalf("CancelParlay() error = %v", err)
	}
	if state := parlayState(t, ctx, pool, own); state != "rejected" {
		t.Fatalf("state = %q, want rejected", state)
	}

	accepted := place("cancel-accepted")
	if _, err := store.AcceptParlay(ctx, accepted, f.UserB); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelParlay(ctx, accepted, f.UserA); !errors.Is(err, betting.ErrInvalidTransition) {
		t.Fatalf("an accepted parlay was cancelled: %v", err)
	}
}
