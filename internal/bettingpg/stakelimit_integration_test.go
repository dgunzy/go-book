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

// TestPerSideLimitLetsTheShortPriceRun is the shape that prompted this: a fun
// prop where the long side is held to $50 and the short side can take real
// money.
func TestPerSideLimitLetsTheShortPriceRun(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	marketID := mustNewUUID(t, ctx, store)
	if _, err := store.CreateMarket(ctx, CreateMarketRequest{
		MarketID: marketID, Type: betting.MarketProp, Title: "Lopsided fun line " + marketID,
		Currency: ledger.CAD, ClosesAt: time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond),
		ActorUserID: f.UserB,
		Selections: []CreateMarketSelection{
			{Key: "yes", DisplayTerms: "Yes", OfferedAmericanOdds: 750, MaxStakeCents: 5_000},
			{Key: "no", DisplayTerms: "No", OfferedAmericanOdds: -1200},
		},
	}); err != nil {
		t.Fatalf("CreateMarket() error = %v", err)
	}
	if err := store.OpenMarket(ctx, marketID, f.UserB); err != nil {
		t.Fatal(err)
	}
	var longSide, shortSide string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM selections WHERE market_id = $1::uuid AND selection_key = 'yes'`,
		marketID).Scan(&longSide); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM selections WHERE market_id = $1::uuid AND selection_key = 'no'`,
		marketID).Scan(&shortSide); err != nil {
		t.Fatal(err)
	}

	place := func(selection string, cents int64, tag string) error {
		wagerID := mustNewUUID(t, ctx, store)
		_, err := store.PlaceWager(ctx, PlaceWagerRequest{
			WagerID: wagerID, UserID: f.UserA, MarketID: marketID, SelectionID: selection,
			FundingAccountType: betting.FundingUserCash, StakeCents: cents, Currency: ledger.CAD,
			IdempotencyKey: tag + ":" + wagerID,
		})
		return err
	}

	// The long side is held to $50, across however many bets.
	if err := place(longSide, 3_000, "side-first"); err != nil {
		t.Fatalf("first wager on the capped side error = %v", err)
	}
	if err := place(longSide, 3_000, "side-over"); !errors.Is(err, betting.ErrStakeAboveLimit) {
		t.Fatalf("wager over the side cap error = %v, want ErrStakeAboveLimit", err)
	}
	if err := place(longSide, 2_000, "side-fill"); err != nil {
		t.Fatalf("wager filling the side cap error = %v", err)
	}

	// The short side is untouched by that: real money is welcome there.
	if err := place(shortSide, 200_000, "short-side"); err != nil {
		t.Fatalf("wager on the uncapped short side error = %v, want it allowed", err)
	}
}

// A market whose only limit is the market-wide one must behave exactly as it
// did before per-side limits existed. This is the guarantee for the live
// market that already carries a cap.
func TestMarketOnlyLimitStillBehavesAsBefore(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	marketID, selections := cappedMarket(t, ctx, store, 5_000, f.UserB)

	place := func(selection string, cents int64, tag string) error {
		wagerID := mustNewUUID(t, ctx, store)
		_, err := store.PlaceWager(ctx, PlaceWagerRequest{
			WagerID: wagerID, UserID: f.UserA, MarketID: marketID, SelectionID: selection,
			FundingAccountType: betting.FundingUserCash, StakeCents: cents, Currency: ledger.CAD,
			IdempotencyKey: tag + ":" + wagerID,
		})
		return err
	}
	// Still counts across both sides, exactly as it did.
	if err := place(selections[1], 3_000, "legacy-a"); err != nil {
		t.Fatalf("first wager error = %v", err)
	}
	if err := place(selections[0], 3_000, "legacy-b"); !errors.Is(err, betting.ErrStakeAboveLimit) {
		t.Fatalf("second wager on the other side error = %v, want the market cap to still bite", err)
	}
}

func TestSetStakeLimitChangesAndClearsLimits(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	marketID, selections := cappedMarket(t, ctx, store, 20_000, f.UserB)

	// Move the market-wide cap, then clear it and cap one side instead —
	// exactly the change a lopsided prop needs after it is already posted.
	if err := store.SetStakeLimit(ctx, marketID, "", 10_000, f.UserB, "tightening it"); err != nil {
		t.Fatalf("SetStakeLimit(market) error = %v", err)
	}
	var marketCap *int64
	if err := pool.QueryRow(ctx, `SELECT max_stake_cents FROM markets WHERE id = $1::uuid`, marketID).Scan(&marketCap); err != nil {
		t.Fatal(err)
	}
	if marketCap == nil || *marketCap != 10_000 {
		t.Fatalf("market cap = %v, want 10000", marketCap)
	}

	if err := store.SetStakeLimit(ctx, marketID, "", 0, f.UserB, "removing the overall cap"); err != nil {
		t.Fatalf("SetStakeLimit(clear) error = %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT max_stake_cents FROM markets WHERE id = $1::uuid`, marketID).Scan(&marketCap); err != nil {
		t.Fatal(err)
	}
	if marketCap != nil {
		t.Fatalf("market cap = %v, want it cleared", *marketCap)
	}

	if err := store.SetStakeLimit(ctx, marketID, selections[0], 5_000, f.UserB, "holding the long side"); err != nil {
		t.Fatalf("SetStakeLimit(selection) error = %v", err)
	}
	var sideCap *int64
	if err := pool.QueryRow(ctx, `SELECT max_stake_cents FROM selections WHERE id = $1::uuid`, selections[0]).Scan(&sideCap); err != nil {
		t.Fatal(err)
	}
	if sideCap == nil || *sideCap != 5_000 {
		t.Fatalf("side cap = %v, want 5000", sideCap)
	}
	// The change is on the audit trail.
	var entries int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_entries
		WHERE target_id = $1::uuid AND action = 'market.stake_limit_changed'`, marketID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 3 {
		t.Fatalf("audit entries = %d, want one per change", entries)
	}

	// Refusals: no reason, unknown market, a selection from another market.
	if err := store.SetStakeLimit(ctx, marketID, "", 5_000, f.UserB, "  "); !errors.Is(err, betting.ErrReasonRequired) {
		t.Fatalf("no reason error = %v, want ErrReasonRequired", err)
	}
	otherMarket, otherSelections := cappedMarket(t, ctx, store, 0, f.UserB)
	_ = otherMarket
	if err := store.SetStakeLimit(ctx, marketID, otherSelections[0], 5_000, f.UserB, "wrong market"); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("cross-market limit error = %v, want ErrNotFound", err)
	}
	if err := store.CloseMarket(ctx, marketID, f.UserB); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStakeLimit(ctx, marketID, "", 5_000, f.UserB, "too late"); !errors.Is(err, ErrMarketNotPriceable) {
		t.Fatalf("closed market error = %v, want ErrMarketNotPriceable", err)
	}
}
