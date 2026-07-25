package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
)

// TestSetMarketCloseTimeMovesTheDeadlineAndAudits covers the case that
// prompted it: the tee times moved, so the market has to stay open longer,
// with the bets already on it untouched.
func TestSetMarketCloseTimeMovesTheDeadlineAndAudits(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	admin := makeUser(t, ctx, pool, "Close Time Admin")
	filled := placeAndAcceptSelection(t, ctx, store, marketID, users[0], selections[0], 5_000, "closetime")

	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT closes_at FROM markets WHERE id = $1::uuid`, marketID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	moved := before.Add(12 * time.Hour).Truncate(time.Microsecond)

	if err := store.SetMarketCloseTime(ctx, marketID, moved, admin, "tee times moved to the afternoon"); err != nil {
		t.Fatalf("SetMarketCloseTime() error = %v", err)
	}

	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT closes_at FROM markets WHERE id = $1::uuid`, marketID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.UTC().Equal(moved.UTC()) {
		t.Fatalf("closes_at = %s, want %s", after.UTC(), moved.UTC())
	}

	// The change is on the audit trail with both times and the reason.
	var action, reason, beforeJSON, afterJSON string
	if err := pool.QueryRow(ctx, `
		SELECT action, coalesce(reason, ''), before_data::text, after_data::text
		FROM audit_entries WHERE target_id = $1::uuid AND action = 'market.close_time_changed'
		ORDER BY occurred_at DESC LIMIT 1`, marketID).Scan(&action, &reason, &beforeJSON, &afterJSON); err != nil {
		t.Fatalf("no audit entry for the close time change: %v", err)
	}
	if reason != "tee times moved to the afternoon" {
		t.Fatalf("audit reason = %q", reason)
	}
	if beforeJSON == afterJSON {
		t.Fatalf("audit recorded the same time before and after: %s", beforeJSON)
	}

	// The wager already on it is untouched: same odds, same state.
	var odds int32
	var state string
	if err := pool.QueryRow(ctx, `SELECT accepted_american_odds, state FROM wagers WHERE id = $1::uuid`,
		string(filled.ID)).Scan(&odds, &state); err != nil {
		t.Fatal(err)
	}
	if odds != int32(filled.AcceptedOdds) || state != "accepted" {
		t.Fatalf("wager changed: odds %d state %q", odds, state)
	}

	// And the market is still on the member's board, now until the later time.
	board, err := store.ListOpenMarketsForUser(ctx, users[0])
	if err != nil {
		t.Fatal(err)
	}
	var listed bool
	for _, market := range board {
		if market.ID == marketID {
			listed = true
			if !market.ClosesAt.UTC().Equal(moved.UTC()) {
				t.Fatalf("board shows %s, want the moved %s", market.ClosesAt.UTC(), moved.UTC())
			}
		}
	}
	if !listed {
		t.Fatal("the market fell off the board after its closing time moved")
	}
}

func TestSetMarketCloseTimeRefusesPastTimesAndClosedMarkets(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, _, _ := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	admin := makeUser(t, ctx, pool, "Close Time Admin 2")
	future := time.Now().Add(24 * time.Hour)

	// Backdating would hide the change from anyone reading the board; closing
	// now is what Close is for.
	if err := store.SetMarketCloseTime(ctx, marketID, time.Now().Add(-time.Hour), admin, "oops"); !errors.Is(err, ErrCloseTimeInPast) {
		t.Fatalf("past close time error = %v, want ErrCloseTimeInPast", err)
	}
	if err := store.SetMarketCloseTime(ctx, marketID, future, admin, "  "); !errors.Is(err, betting.ErrReasonRequired) {
		t.Fatalf("no reason error = %v, want ErrReasonRequired", err)
	}
	if err := store.SetMarketCloseTime(ctx, mustNewUUID(t, ctx, store), future, admin, "who?"); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("unknown market error = %v, want ErrNotFound", err)
	}

	// Once closed, action has stopped; moving the time would let money in
	// against a result people may already know.
	if err := store.CloseMarket(ctx, marketID, admin); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMarketCloseTime(ctx, marketID, future, admin, "reopen it"); !errors.Is(err, ErrMarketNotPriceable) {
		t.Fatalf("closed market error = %v, want ErrMarketNotPriceable", err)
	}
}
