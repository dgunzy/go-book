package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// placeFor attempts a wager and returns the error, so a test can assert on
// refusal without caring about the wager itself.
func placeFor(t *testing.T, ctx context.Context, store Store, marketID, userID, selectionID, tag string) error {
	t.Helper()
	wagerID := mustNewUUID(t, ctx, store)
	_, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: userID, MarketID: marketID, SelectionID: selectionID,
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: tag + ":" + wagerID,
	})
	return err
}

// TestSideRestrictionBarsOneOutcomeOnly is the case that prompted this: the
// player a prop is about is kept off one side of their own line while the rest
// of the board stays open to them.
func TestSideRestrictionBarsOneOutcomeOnly(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member, admin := users[0], users[1]
	north, south := selections[0], selections[1]

	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: member, SelectionID: south,
		Reason: "the bet is about them", ActorUserID: admin,
	}); err != nil {
		t.Fatalf("RestrictMember() error = %v", err)
	}

	if err := placeFor(t, ctx, store, marketID, member, south, "restricted-side"); !errors.Is(err, betting.ErrUserRestricted) {
		t.Fatalf("wager on the restricted side error = %v, want ErrUserRestricted", err)
	}
	if err := placeFor(t, ctx, store, marketID, member, north, "open-side"); err != nil {
		t.Fatalf("wager on the open side error = %v, want it allowed", err)
	}
	// Everyone else is unaffected.
	if err := placeFor(t, ctx, store, marketID, users[1], south, "other-member"); err != nil {
		t.Fatalf("another member's wager error = %v, want it allowed", err)
	}
}

func TestWholeMarketRestrictionBarsEverySide(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member, admin := users[0], users[1]

	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: member, Reason: "sat this one out", ActorUserID: admin,
	}); err != nil {
		t.Fatalf("RestrictMember() error = %v", err)
	}
	for i, selection := range selections {
		if err := placeFor(t, ctx, store, marketID, member, selection, "whole-market"); !errors.Is(err, betting.ErrUserRestricted) {
			t.Fatalf("wager on selection %d error = %v, want ErrUserRestricted", i, err)
		}
	}
}

// A restricted market or side must not appear on the member's board at all.
func TestRestrictedMarketsAndSidesAreHiddenFromTheMember(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	hiddenMarket, users, hiddenSelections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	partialMarket, _, partialSelections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member, admin := users[0], users[1]

	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: hiddenMarket, UserID: member, Reason: "whole market", ActorUserID: admin,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: partialMarket, UserID: member, SelectionID: partialSelections[1],
		Reason: "one side", ActorUserID: admin,
	}); err != nil {
		t.Fatal(err)
	}

	board, err := store.ListOpenMarketsForUser(ctx, member)
	if err != nil {
		t.Fatalf("ListOpenMarketsForUser() error = %v", err)
	}
	for _, market := range board {
		if market.ID == hiddenMarket {
			t.Fatal("a market the member is restricted from is still on their board")
		}
		if market.ID != partialMarket {
			continue
		}
		for _, selection := range market.Selections {
			if selection.ID == partialSelections[1] {
				t.Fatal("a restricted side is still on the member's board")
			}
		}
		if len(market.Selections) == 0 {
			t.Fatal("the rest of the partially restricted market disappeared")
		}
	}

	// The admin board still shows everything, and another member is unaffected.
	adminBoard, err := store.ListOpenMarkets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawHidden bool
	for _, market := range adminBoard {
		if market.ID == hiddenMarket {
			sawHidden = true
		}
	}
	if !sawHidden {
		t.Fatal("the admin board hid a restricted market; restrictions are per member")
	}
	otherBoard, err := store.ListOpenMarketsForUser(ctx, users[1])
	if err != nil {
		t.Fatal(err)
	}
	var otherSawHidden bool
	for _, market := range otherBoard {
		if market.ID == hiddenMarket {
			otherSawHidden = true
		}
	}
	if !otherSawHidden {
		t.Fatal("another member lost sight of a market they are not restricted from")
	}
	_ = hiddenSelections
}

func TestRestrictionsAreListedLiftedAndRerecorded(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member, admin := users[0], users[1]

	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: member, SelectionID: selections[0],
		Reason: "first wording", ActorUserID: admin,
	}); err != nil {
		t.Fatal(err)
	}
	// Re-recording the same restriction updates the reason instead of failing.
	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: member, SelectionID: selections[0],
		Reason: "clearer wording", ActorUserID: admin,
	}); err != nil {
		t.Fatalf("re-recording a restriction error = %v", err)
	}

	rows, err := store.ListRestrictions(ctx)
	if err != nil {
		t.Fatalf("ListRestrictions() error = %v", err)
	}
	var found int
	for _, row := range rows {
		if row.MarketID == marketID && row.UserID == member {
			found++
			if row.Reason != "clearer wording" {
				t.Fatalf("reason = %q, want the updated wording", row.Reason)
			}
			if row.WholeMarket() {
				t.Fatal("a side restriction reported itself as whole-market")
			}
			if row.SelectionTerms == "" || row.MemberName == "" {
				t.Fatalf("row is missing display data: %+v", row)
			}
		}
	}
	if found != 1 {
		t.Fatalf("restrictions for this member = %d, want exactly 1", found)
	}

	// A whole-market ban is a separate row from a side ban.
	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: member, Reason: "and the rest", ActorUserID: admin,
	}); err != nil {
		t.Fatal(err)
	}
	// Lifting the whole-market ban leaves the side ban in place.
	if err := store.LiftRestriction(ctx, marketID, member, ""); err != nil {
		t.Fatalf("LiftRestriction(whole market) error = %v", err)
	}
	if err := placeFor(t, ctx, store, marketID, member, selections[0], "still-restricted"); !errors.Is(err, betting.ErrUserRestricted) {
		t.Fatalf("side restriction error after lifting the market ban = %v, want it still in force", err)
	}
	if err := store.LiftRestriction(ctx, marketID, member, selections[0]); err != nil {
		t.Fatalf("LiftRestriction(side) error = %v", err)
	}
	if err := placeFor(t, ctx, store, marketID, member, selections[0], "lifted"); err != nil {
		t.Fatalf("wager after every restriction was lifted error = %v", err)
	}
	// Lifting something that is not there is reported, not silently ignored.
	if err := store.LiftRestriction(ctx, marketID, member, selections[0]); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("lifting a missing restriction error = %v, want ErrNotFound", err)
	}
}

func TestRestrictMemberRefusesASelectionFromAnotherMarket(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, _ := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	_, _, otherSelections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)

	err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: users[0], SelectionID: otherSelections[0],
		Reason: "wrong market", ActorUserID: users[1],
	})
	if !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("cross-market restriction error = %v, want ErrNotFound", err)
	}
	// And nothing was recorded, so the member is still free to bet.
	if err := placeFor(t, ctx, store, marketID, users[0], otherSelections[0], "cross"); errors.Is(err, betting.ErrUserRestricted) {
		t.Fatal("a refused restriction still barred the member")
	}
}
