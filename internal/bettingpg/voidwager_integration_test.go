package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// TestVoidWagerReturnsOneStakeAndLeavesTheRestStanding is the case this exists
// for: one bet comes off the board, everyone else's action is untouched.
func TestVoidWagerReturnsOneStakeAndLeavesTheRestStanding(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member, other := users[0], users[1]
	admin := makeUser(t, ctx, pool, "Void Admin")
	escrowBefore := systemAccountBalance(t, ctx, pool, "wager_escrow", ledger.CAD)

	doomed := placeAndAcceptSelection(t, ctx, store, marketID, member, selections[0], 5_000, "void-target")
	kept := placeAndAcceptSelection(t, ctx, store, marketID, other, selections[1], 7_000, "void-other")

	balanceBefore := accountBalanceFor(t, ctx, pool, member, "user_cash", ledger.CAD)
	otherBefore := accountBalanceFor(t, ctx, pool, other, "user_cash", ledger.CAD)

	voided, err := store.VoidWager(ctx, string(doomed.ID), admin, "struck in error")
	if err != nil {
		t.Fatalf("VoidWager() error = %v", err)
	}
	if voided.State != betting.WagerVoided {
		t.Fatalf("state = %v, want voided", voided.State)
	}
	assertWagerState(t, ctx, pool, string(doomed.ID), "voided")

	// The stake came back to the member, and escrow fell by the same amount.
	if balance := accountBalanceFor(t, ctx, pool, member, "user_cash", ledger.CAD); balance != balanceBefore+5_000 {
		t.Fatalf("member balance = %d, want %d after the refund", balance, balanceBefore+5_000)
	}
	escrowAfter := systemAccountBalance(t, ctx, pool, "wager_escrow", ledger.CAD)
	if escrowBefore+7_000 != escrowAfter {
		t.Fatalf("escrow = %d, want the other member's 7000 still held", escrowAfter-escrowBefore)
	}

	// The other member's wager is untouched — this is not a market void.
	assertWagerState(t, ctx, pool, string(kept.ID), "accepted")
	if balance := accountBalanceFor(t, ctx, pool, other, "user_cash", ledger.CAD); balance != otherBefore {
		t.Fatalf("another member's balance moved: %d, want %d", balance, otherBefore)
	}

	// The refund names the admin and carries the reason, so the member's
	// ledger explains itself.
	var reason, actor string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(reason, ''), coalesce(actor_user_id::text, '')
		FROM ledger_transactions WHERE idempotency_key = $1 AND currency = 'CAD'`,
		betting.VoidIdempotencyKey(doomed.ID)).Scan(&reason, &actor); err != nil {
		t.Fatal(err)
	}
	if reason != "struck in error" || actor != admin {
		t.Fatalf("refund entry reason %q by %q", reason, actor)
	}

	// Repeating the void does not refund twice.
	if _, err := store.VoidWager(ctx, string(doomed.ID), admin, "struck in error"); err != nil {
		t.Fatalf("repeat VoidWager() error = %v", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, member, "user_cash", ledger.CAD); balance != balanceBefore+5_000 {
		t.Fatalf("member balance after a repeated void = %d, want the single refund", balance)
	}
}

func TestVoidWagerRefusesWagersThatAreNotAccepted(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	admin := makeUser(t, ctx, pool, "Void Admin 2")

	// Pending: rejected instead, because no money has moved.
	wagerID := mustNewUUID(t, ctx, store)
	pending, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: users[0], MarketID: marketID, SelectionID: selections[0],
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "void-pending:" + wagerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.VoidWager(ctx, string(pending.ID), admin, "not yet"); !errors.Is(err, betting.ErrInvalidTransition) {
		t.Fatalf("voiding a pending wager error = %v, want ErrInvalidTransition", err)
	}
	assertWagerState(t, ctx, pool, string(pending.ID), "pending")

	// An unknown wager is reported, not silently ignored.
	if _, err := store.VoidWager(ctx, mustNewUUID(t, ctx, store), admin, "who?"); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("voiding an unknown wager error = %v, want ErrNotFound", err)
	}
	// A reason is required, and nothing moves without one.
	accepted := placeAndAcceptSelection(t, ctx, store, marketID, users[1], selections[1], 2_000, "void-noreason")
	if _, err := store.VoidWager(ctx, string(accepted.ID), admin, "   "); !errors.Is(err, betting.ErrReasonRequired) {
		t.Fatalf("voiding without a reason error = %v, want ErrReasonRequired", err)
	}
	assertWagerState(t, ctx, pool, string(accepted.ID), "accepted")
}

// A voided wager is no longer action, so the line must come back.
func TestVoidWagerRepricesTheBoard(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 100_000, 500_000)
	admin := makeUser(t, ctx, pool, "Void Admin 3")

	wager := placeAndAcceptSelection(t, ctx, store, marketID, users[0], selections[0], 200_000, "void-reprice")
	if _, err := store.RepriceMarketAfterWager(ctx, marketID, string(wager.ID)); err != nil {
		t.Fatal(err)
	}
	var moved int32
	if err := pool.QueryRow(ctx, `SELECT offered_american_odds FROM selections WHERE id = $1::uuid`,
		selections[0]).Scan(&moved); err != nil {
		t.Fatal(err)
	}
	if moved == -110 {
		t.Fatal("the line did not move; cannot show that voiding brings it back")
	}

	if _, err := store.VoidWager(ctx, string(wager.ID), admin, "struck in error"); err != nil {
		t.Fatalf("VoidWager() error = %v", err)
	}
	var restored int32
	if err := pool.QueryRow(ctx, `SELECT offered_american_odds FROM selections WHERE id = $1::uuid`,
		selections[0]).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != -110 {
		t.Fatalf("line = %d after the void, want it back at the opening -110 with no action left", restored)
	}
}
