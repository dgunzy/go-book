package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
)

// These use the priced-market fixture rather than the match fixture: a
// hand-set line writes an append-only selection_price_changes row, which pins
// the market and everything under it in place for good. The priced fixture has
// no event or match beneath it, so the residue stays confined to rows the
// reconciliation gate already accounts for.

// TestSetOpeningLineMovesTheBoardAndAudits proves a hand-set line behaves like
// the engine's own move: the prior changes, the board is repriced from it, and
// the change is recorded against the admin who made it.
func TestSetOpeningLineMovesTheBoardAndAudits(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, _, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	north := selections[0]
	admin := makeUser(t, ctx, pool, "Set Line Admin")

	changed, err := store.SetOpeningLine(ctx, marketID, north, -150, admin, "opened too short")
	if err != nil {
		t.Fatalf("SetOpeningLine() error = %v", err)
	}
	if !changed {
		t.Fatal("setting a new line reported no change")
	}

	var opening, offered int32
	if err := pool.QueryRow(ctx,
		`SELECT opening_american_odds, offered_american_odds FROM selections WHERE id = $1::uuid`,
		north).Scan(&opening, &offered); err != nil {
		t.Fatal(err)
	}
	if opening != -150 {
		t.Fatalf("opening line = %d, want -150", opening)
	}
	// No action is on the market, so the offered price follows the new prior.
	if offered != -150 {
		t.Fatalf("offered line = %d, want it to follow the new prior", offered)
	}

	var actor, reason string
	var oldOdds, newOdds int32
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(actor_user_id::text, ''), coalesce(reason, ''), old_american_odds, new_american_odds
		FROM selection_price_changes WHERE selection_id = $1::uuid ORDER BY created_at DESC LIMIT 1`,
		north).Scan(&actor, &reason, &oldOdds, &newOdds); err != nil {
		t.Fatal(err)
	}
	if actor != admin || reason != "opened too short" {
		t.Fatalf("audit row = actor %q, reason %q", actor, reason)
	}
	if oldOdds != -110 || newOdds != -150 {
		t.Fatalf("audit row = %d -> %d, want -110 -> -150", oldOdds, newOdds)
	}
}

// A hand-set line must survive the next automatic reprice rather than being
// recomputed away, and must not disturb wagers already filled.
func TestSetOpeningLineSurvivesTheNextRepriceAndLeavesFilledWagersAlone(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	north := selections[0]
	admin := makeUser(t, ctx, pool, "Set Line Admin 2")
	filled := placeAndAcceptSelection(t, ctx, store, marketID, users[0], north, 5_000, "setline")

	if _, err := store.SetOpeningLine(ctx, marketID, north, -150, admin, "balancing by hand"); err != nil {
		t.Fatalf("SetOpeningLine() error = %v", err)
	}
	if _, err := store.RepriceMarketAfterWager(ctx, marketID, string(filled.ID)); err != nil {
		t.Fatalf("RepriceMarketAfterWager() error = %v", err)
	}

	var opening int32
	if err := pool.QueryRow(ctx, `SELECT opening_american_odds FROM selections WHERE id = $1::uuid`,
		north).Scan(&opening); err != nil {
		t.Fatal(err)
	}
	if opening != -150 {
		t.Fatalf("opening line = %d after an automatic reprice, want the hand-set -150", opening)
	}

	var acceptedOdds int32
	if err := pool.QueryRow(ctx, `SELECT accepted_american_odds FROM wagers WHERE id = $1::uuid`,
		string(filled.ID)).Scan(&acceptedOdds); err != nil {
		t.Fatal(err)
	}
	if acceptedOdds != int32(filled.AcceptedOdds) {
		t.Fatalf("filled wager odds = %d, want the %d it was accepted at", acceptedOdds, filled.AcceptedOdds)
	}
}

func TestSetOpeningLineRefusesAMarketThatIsNoLongerOpen(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, _, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	admin := makeUser(t, ctx, pool, "Set Line Admin 3")

	if err := store.CloseMarket(ctx, marketID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetOpeningLine(ctx, marketID, selections[0], -150, admin, "too late"); !errors.Is(err, ErrMarketNotPriceable) {
		t.Fatalf("closed market error = %v, want ErrMarketNotPriceable", err)
	}

	// The refusal leaves the line untouched.
	var opening int32
	if err := pool.QueryRow(ctx, `SELECT opening_american_odds FROM selections WHERE id = $1::uuid`,
		selections[0]).Scan(&opening); err != nil {
		t.Fatal(err)
	}
	if opening != -110 {
		t.Fatalf("opening line = %d after a refused change, want the original -110", opening)
	}
}

// A selection belonging to another market must not be moved through this
// market, and must not leave a half-applied change behind.
func TestSetOpeningLineRefusesASelectionFromAnotherMarket(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, _, _ := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	_, _, otherSelections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	admin := makeUser(t, ctx, pool, "Set Line Admin 4")

	if _, err := store.SetOpeningLine(ctx, marketID, otherSelections[0], -150, admin, "wrong market"); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("cross-market error = %v, want ErrNotFound", err)
	}
	var opening int32
	if err := pool.QueryRow(ctx, `SELECT opening_american_odds FROM selections WHERE id = $1::uuid`,
		otherSelections[0]).Scan(&opening); err != nil {
		t.Fatal(err)
	}
	if opening != -110 {
		t.Fatalf("the other market's line = %d, want it untouched at -110", opening)
	}
}
