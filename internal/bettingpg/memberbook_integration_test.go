package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// TestPlaceWagerForMemberRecordsTheAdminWhoPlacedIt proves the wager belongs to
// the member while the row remembers who put it on — the answer to a later
// "I never placed that".
func TestPlaceWagerForMemberRecordsTheAdminWhoPlacedIt(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member := users[0]
	admin := makeUser(t, ctx, pool, "Placing Admin")
	balanceBefore := accountBalanceFor(t, ctx, pool, member, "user_cash", ledger.CAD)

	wagerID := mustNewUUID(t, ctx, store)
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: member, MarketID: marketID, SelectionID: selections[0],
		FundingAccountType: betting.FundingUserCash, StakeCents: 5_000, Currency: ledger.CAD,
		IdempotencyKey: "admin-placed:" + wagerID, PlacedByUserID: admin,
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	if string(wager.UserID) != member {
		t.Fatalf("wager belongs to %q, want the member %q", wager.UserID, member)
	}

	var placedBy string
	if err := pool.QueryRow(ctx, `SELECT coalesce(placed_by::text, '') FROM wagers WHERE id = $1::uuid`,
		string(wager.ID)).Scan(&placedBy); err != nil {
		t.Fatal(err)
	}
	if placedBy != admin {
		t.Fatalf("placed_by = %q, want the admin %q", placedBy, admin)
	}

	// Accepting it moves the member's money, not the admin's.
	if _, err := store.AcceptWager(ctx, string(wager.ID), admin); err != nil {
		t.Fatalf("AcceptWager() error = %v", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, member, "user_cash", ledger.CAD); balance != balanceBefore-5_000 {
		t.Fatalf("member balance = %d, want %d", balance, balanceBefore-5_000)
	}
	if balance := accountBalanceFor(t, ctx, pool, admin, "user_cash", ledger.CAD); balance != 0 {
		t.Fatalf("the admin's own balance moved: %d", balance)
	}
}

// A member's own placement leaves placed_by empty, so the column distinguishes
// the two rather than being set for everything.
func TestPlaceWagerByTheMemberLeavesPlacedByEmpty(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	wagerID := mustNewUUID(t, ctx, store)
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: users[0], MarketID: marketID, SelectionID: selections[0],
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "self-placed:" + wagerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var placedBy string
	if err := pool.QueryRow(ctx, `SELECT coalesce(placed_by::text, '') FROM wagers WHERE id = $1::uuid`,
		string(wager.ID)).Scan(&placedBy); err != nil {
		t.Fatal(err)
	}
	if placedBy != "" {
		t.Fatalf("placed_by = %q, want it empty for a member's own wager", placedBy)
	}
}

// A restriction holds whoever is placing: an admin acting for the member is
// still refused.
func TestPlaceWagerForMemberStillObeysTheirRestrictions(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member := users[0]
	admin := makeUser(t, ctx, pool, "Placing Admin 2")

	if err := store.RestrictMember(ctx, RestrictRequest{
		MarketID: marketID, UserID: member, SelectionID: selections[1],
		Reason: "the prop is about them", ActorUserID: admin,
	}); err != nil {
		t.Fatal(err)
	}

	wagerID := mustNewUUID(t, ctx, store)
	_, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: member, MarketID: marketID, SelectionID: selections[1],
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "admin-restricted:" + wagerID, PlacedByUserID: admin,
	})
	if !errors.Is(err, betting.ErrUserRestricted) {
		t.Fatalf("admin placing on a restricted side error = %v, want ErrUserRestricted", err)
	}

	// The side they are not restricted from is still available to place.
	openWagerID := mustNewUUID(t, ctx, store)
	if _, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: openWagerID, UserID: member, MarketID: marketID, SelectionID: selections[0],
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "admin-open:" + openWagerID, PlacedByUserID: admin,
	}); err != nil {
		t.Fatalf("admin placing on an open side error = %v", err)
	}
}

func TestMemberBookReportsBalanceCreditAndApprovalLimit(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	_, users, _ := buildPricedMarket(t, ctx, pool, store, 500_000, 100_000)
	member := users[0]
	if _, err := pool.Exec(ctx, `UPDATE users SET credit_limit_cents = 150000 WHERE id = $1::uuid`, member); err != nil {
		t.Fatal(err)
	}

	book, err := store.MemberBook(ctx, member, 10_000)
	if err != nil {
		t.Fatalf("MemberBook() error = %v", err)
	}
	if book.Balance.Cents != 100_000 {
		t.Fatalf("balance = %d, want the funded 100000", book.Balance.Cents)
	}
	if book.CreditLimit.Cents != 150_000 {
		t.Fatalf("credit limit = %d, want 150000", book.CreditLimit.Cents)
	}
	// What they may still stake is their balance plus the credit line.
	if book.CreditAvailable.Cents != 250_000 {
		t.Fatalf("credit available = %d, want 250000", book.CreditAvailable.Cents)
	}
	// With no personal override they get the book default.
	if book.AutoApproveLimit.Cents != 10_000 || book.AutoApprovePersonal {
		t.Fatalf("auto-approve = %d (personal %v), want the 10000 default", book.AutoApproveLimit.Cents, book.AutoApprovePersonal)
	}

	// A personal override replaces it and is reported as theirs.
	if _, err := pool.Exec(ctx, `UPDATE users SET wager_auto_approve_max_cents = 50000 WHERE id = $1::uuid`, member); err != nil {
		t.Fatal(err)
	}
	book, err = store.MemberBook(ctx, member, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if book.AutoApproveLimit.Cents != 50_000 || !book.AutoApprovePersonal {
		t.Fatalf("auto-approve = %d (personal %v), want the 50000 override", book.AutoApproveLimit.Cents, book.AutoApprovePersonal)
	}

	if _, err := store.MemberBook(ctx, mustNewUUID(t, ctx, store), 10_000); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("MemberBook for an unknown user error = %v, want ErrNotFound", err)
	}
}
