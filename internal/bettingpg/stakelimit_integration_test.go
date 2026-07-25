package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// cappedMarket builds an open future market with a per-member stake cap.
func cappedMarket(t *testing.T, ctx context.Context, store Store, maxStakeCents int64, admin string) (string, [2]string) {
	t.Helper()
	marketID := mustNewUUID(t, ctx, store)
	if _, err := store.CreateMarket(ctx, CreateMarketRequest{
		MarketID: marketID, Type: betting.MarketFuture, Title: "Capped fun line " + marketID,
		Currency: ledger.CAD, ClosesAt: time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond),
		MaxStakeCents: maxStakeCents, ActorUserID: admin,
		Selections: []CreateMarketSelection{
			{Key: "yes", DisplayTerms: "Yes", OfferedAmericanOdds: 150},
			{Key: "no", DisplayTerms: "No", OfferedAmericanOdds: -180},
		},
	}); err != nil {
		t.Fatalf("CreateMarket() error = %v", err)
	}
	if err := store.OpenMarket(ctx, marketID, admin); err != nil {
		t.Fatalf("OpenMarket() error = %v", err)
	}
	var selections [2]string
	rows, err := store.DB.Query(ctx, `SELECT id::text FROM selections WHERE market_id = $1::uuid ORDER BY selection_key`, marketID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i := 0; rows.Next() && i < 2; i++ {
		if err := rows.Scan(&selections[i]); err != nil {
			t.Fatal(err)
		}
	}
	return marketID, selections
}

// TestMarketStakeCapCountsEverythingAMemberHasOnIt is the point of the cap: a
// fun line stays fun, and spreading the money over several bets does not get
// around it.
func TestMarketStakeCapCountsEverythingAMemberHasOnIt(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	marketID, selections := cappedMarket(t, ctx, store, 5_000, f.UserB)

	place := func(user, selection string, cents int64, tag string) error {
		wagerID := mustNewUUID(t, ctx, store)
		_, err := store.PlaceWager(ctx, PlaceWagerRequest{
			WagerID: wagerID, UserID: user, MarketID: marketID, SelectionID: selection,
			FundingAccountType: betting.FundingUserCash, StakeCents: cents, Currency: ledger.CAD,
			IdempotencyKey: tag + ":" + wagerID,
		})
		return err
	}

	if err := place(f.UserA, selections[1], 3_000, "cap-first"); err != nil {
		t.Fatalf("first wager under the cap error = %v", err)
	}
	// $30 on already; another $30 would be $60 against a $50 cap.
	if err := place(f.UserA, selections[1], 3_000, "cap-over"); !errors.Is(err, betting.ErrStakeAboveLimit) {
		t.Fatalf("second wager over the cap error = %v, want ErrStakeAboveLimit", err)
	}
	// The remaining $20 fits, even on the other side of the same market.
	if err := place(f.UserA, selections[0], 2_000, "cap-rest"); err != nil {
		t.Fatalf("wager filling the cap exactly error = %v", err)
	}
	// And now they are full.
	if err := place(f.UserA, selections[0], 100, "cap-full"); !errors.Is(err, betting.ErrStakeAboveLimit) {
		t.Fatalf("wager past a filled cap error = %v, want ErrStakeAboveLimit", err)
	}
	// Another member has their own allowance.
	if err := place(f.UserB, selections[0], 5_000, "cap-other"); err != nil {
		t.Fatalf("another member's first wager error = %v", err)
	}
}

// The payout ceiling is a hard cap: a store built without one still enforces
// the compiled default, so no path can place a wager that escapes it.
func TestPayoutCeilingAppliesEvenWithoutConfiguration(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool} // no MaxPayoutCents set

	f := buildFixture(t, ctx, pool, 10_000_000)
	marketID, selections := cappedMarket(t, ctx, store, 0, f.UserB)

	// The "Yes" side is +150, so $4,000 would win $6,000 — over the $5,000
	// compiled default.
	wagerID := mustNewUUID(t, ctx, store)
	_, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: f.UserA, MarketID: marketID, SelectionID: selections[1],
		FundingAccountType: betting.FundingUserCash, StakeCents: 400_000, Currency: ledger.CAD,
		IdempotencyKey: "payout-over:" + wagerID,
	})
	if !errors.Is(err, betting.ErrPayoutAboveLimit) {
		t.Fatalf("wager over the payout ceiling error = %v, want ErrPayoutAboveLimit", err)
	}

	// $3,000 wins $4,500 and is fine.
	okID := mustNewUUID(t, ctx, store)
	if _, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: okID, UserID: f.UserA, MarketID: marketID, SelectionID: selections[1],
		FundingAccountType: betting.FundingUserCash, StakeCents: 300_000, Currency: ledger.CAD,
		IdempotencyKey: "payout-under:" + okID,
	}); err != nil {
		t.Fatalf("wager under the payout ceiling error = %v", err)
	}

	// A configured ceiling replaces the default.
	tight := Store{DB: pool, MaxPayoutCents: 10_000}
	tightID := mustNewUUID(t, ctx, store)
	if _, err := tight.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: tightID, UserID: f.UserB, MarketID: marketID, SelectionID: selections[1],
		FundingAccountType: betting.FundingUserCash, StakeCents: 20_000, Currency: ledger.CAD,
		IdempotencyKey: "payout-tight:" + tightID,
	}); !errors.Is(err, betting.ErrPayoutAboveLimit) {
		t.Fatalf("wager over a configured ceiling error = %v, want ErrPayoutAboveLimit", err)
	}
}
