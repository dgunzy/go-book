package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
)

// TestSetOpeningLineMovesTheBoardAndAudits proves a hand-set line behaves like
// the engine's own move: the prior changes, the whole board is repriced from
// it, and the change is recorded against the admin who made it.
func TestSetOpeningLineMovesTheBoardAndAudits(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	changed, err := store.SetOpeningLine(ctx, f.MarketID, f.SelectionAID, -150, f.UserB, "opened too short")
	if err != nil {
		t.Fatalf("SetOpeningLine() error = %v", err)
	}
	if !changed {
		t.Fatal("setting a new line reported no change")
	}

	var opening, offered int32
	if err := pool.QueryRow(ctx,
		`SELECT opening_american_odds, offered_american_odds FROM selections WHERE id = $1::uuid`,
		f.SelectionAID).Scan(&opening, &offered); err != nil {
		t.Fatal(err)
	}
	if opening != -150 {
		t.Fatalf("opening line = %d, want -150", opening)
	}
	// No action is on the market, so the offered price follows the prior exactly.
	if offered != -150 {
		t.Fatalf("offered line = %d, want it to follow the new prior", offered)
	}

	var actor, reason string
	var oldOdds, newOdds int32
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(actor_user_id::text, ''), coalesce(reason, ''), old_american_odds, new_american_odds
		FROM selection_price_changes WHERE selection_id = $1::uuid ORDER BY created_at DESC LIMIT 1`,
		f.SelectionAID).Scan(&actor, &reason, &oldOdds, &newOdds); err != nil {
		t.Fatal(err)
	}
	if actor != f.UserB || reason != "opened too short" {
		t.Fatalf("audit row = actor %q, reason %q", actor, reason)
	}
	if oldOdds != -110 || newOdds != -150 {
		t.Fatalf("audit row = %d -> %d, want -110 -> -150", oldOdds, newOdds)
	}
}

// A hand-set line must not disturb wagers already filled, and must survive the
// next automatic reprice rather than being recomputed away.
func TestSetOpeningLineSurvivesTheNextRepriceAndLeavesFilledWagersAlone(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	filled := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 5_000, 1)

	if _, err := store.SetOpeningLine(ctx, f.MarketID, f.SelectionAID, -150, f.UserB, "balancing by hand"); err != nil {
		t.Fatalf("SetOpeningLine() error = %v", err)
	}
	if _, err := store.RepriceMarketAfterWager(ctx, f.MarketID, string(filled.ID)); err != nil {
		t.Fatalf("RepriceMarketAfterWager() error = %v", err)
	}

	var opening int32
	if err := pool.QueryRow(ctx, `SELECT opening_american_odds FROM selections WHERE id = $1::uuid`,
		f.SelectionAID).Scan(&opening); err != nil {
		t.Fatal(err)
	}
	if opening != -150 {
		t.Fatalf("opening line = %d after an automatic reprice, want the hand-set -150", opening)
	}

	// The wager keeps the price it was filled at.
	var acceptedOdds int32
	if err := pool.QueryRow(ctx, `SELECT accepted_american_odds FROM wagers WHERE id = $1::uuid`,
		string(filled.ID)).Scan(&acceptedOdds); err != nil {
		t.Fatal(err)
	}
	if acceptedOdds != int32(filled.AcceptedOdds) {
		t.Fatalf("filled wager odds = %d, want the %d it was accepted at", acceptedOdds, filled.AcceptedOdds)
	}
}

func TestSetOpeningLineRefusesClosedMarketsAndBadInput(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	if _, err := store.SetOpeningLine(ctx, f.MarketID, f.SelectionAID, -150, f.UserB, "  "); !errors.Is(err, betting.ErrReasonRequired) {
		t.Fatalf("no reason error = %v, want ErrReasonRequired", err)
	}
	if _, err := store.SetOpeningLine(ctx, f.MarketID, f.SelectionAID, 50, f.UserB, "bad odds"); err == nil {
		t.Fatal("a line inside the -100..+100 dead band was accepted")
	}

	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetOpeningLine(ctx, f.MarketID, f.SelectionAID, -150, f.UserB, "too late"); !errors.Is(err, ErrMarketNotPriceable) {
		t.Fatalf("closed market error = %v, want ErrMarketNotPriceable", err)
	}
}
