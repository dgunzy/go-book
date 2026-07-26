package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Day one of the 2026 cup ended with every prop market stranded in open:
// grading them returned a 409 because settlement demanded a separate Close
// first, while match markets auto-closed and graded fine. Grading an open
// market is unambiguous — it means the result is in — so it closes the market
// itself, in the same transaction.
func TestSettleMarketGradesAMarketThatIsStillOpen(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 2_000, 1)

	if state := marketState(t, ctx, pool, f.MarketID); state != string(betting.MarketOpen) {
		t.Fatalf("fixture market state = %q, want open", state)
	}

	report, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "birdie count is in",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	})
	if err != nil {
		t.Fatalf("SettleMarket() on an open market error = %v", err)
	}
	if report.WinCount != 1 {
		t.Fatalf("win count = %d, want 1", report.WinCount)
	}
	if state := marketState(t, ctx, pool, f.MarketID); state != string(betting.MarketSettled) {
		t.Fatalf("market state after grading = %q, want settled", state)
	}
}

// Closing first stays the documented route and must still work: an admin who
// closes a market and grades it later gets exactly one close, not two.
func TestSettleMarketStillGradesAMarketClosedFirst(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 2_000, 1)
	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded after closing",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultLoss, f.SelectionBID: betting.ResultWin,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() after an explicit close error = %v", err)
	}
	if state := marketState(t, ctx, pool, f.MarketID); state != string(betting.MarketSettled) {
		t.Fatalf("market state = %q, want settled", state)
	}
}

// A market that is already decided must refuse a late acceptance. One slipped
// through on 2026-07-26: a $500 wager was accepted 40 seconds after its match
// settled, so the stake went to escrow with no settlement left to grade it and
// the member was down the money with no way to win it back.
func TestAcceptWagerRefusesAMarketAlreadySettled(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	// One wager carries the market to settlement; a second is left pending so
	// it is still waiting when the market is graded.
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	late := placePending(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, "late-accept")

	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}

	balanceBefore := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	_, err := store.AcceptWager(ctx, late, f.UserB)
	if !errors.Is(err, ErrMarketDecided) {
		t.Fatalf("AcceptWager() on a settled market error = %v, want ErrMarketDecided", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != balanceBefore {
		t.Fatalf("balance moved on a refused acceptance: %d, want unchanged %d", balance, balanceBefore)
	}
	if state := wagerState(t, ctx, pool, late); state != string(betting.WagerPending) {
		t.Fatalf("refused wager state = %q, want it left pending", state)
	}
}

// A voided market is just as final: the refunds are already posted, so a late
// acceptance would take a stake nothing will ever return.
func TestAcceptWagerRefusesAMarketAlreadyVoided(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	late := placePending(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, "late-accept-void")
	if _, err := store.VoidMarket(ctx, VoidMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "market opened in error",
	}); err != nil {
		t.Fatalf("VoidMarket() error = %v", err)
	}
	if _, err := store.AcceptWager(ctx, late, f.UserB); !errors.Is(err, ErrMarketDecided) {
		t.Fatalf("AcceptWager() on a voided market error = %v, want ErrMarketDecided", err)
	}
}

// Accepting after the market closes but before it is graded stays legal: a
// wager placed while the market was open is honoured, and grading picks it up.
func TestAcceptWagerStillWorksOnAClosedMarket(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	late := placePending(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, "accept-after-close")
	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	if _, err := store.AcceptWager(ctx, late, f.UserB); err != nil {
		t.Fatalf("AcceptWager() on a closed market error = %v, want it accepted", err)
	}
	report, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	})
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if report.WinCount != 1 {
		t.Fatalf("win count = %d, want the late acceptance graded", report.WinCount)
	}
}

// placePending places a wager and deliberately leaves it awaiting approval.
func placePending(t *testing.T, ctx context.Context, store Store, f fixture, userID, selectionID string, stakeCents int64, tag string) string {
	t.Helper()
	wagerID := mustNewUUID(t, ctx, store)
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID:            wagerID,
		UserID:             userID,
		MarketID:           f.MarketID,
		SelectionID:        selectionID,
		FundingAccountType: betting.FundingUserCash,
		StakeCents:         stakeCents,
		Currency:           f.Currency,
		IdempotencyKey:     fmt.Sprintf("test-place:%s:%s", f.Suffix, tag),
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	if wager.State != betting.WagerPending {
		t.Fatalf("placed wager state = %v, want pending", wager.State)
	}
	return string(wager.ID)
}

func marketState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marketID string) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM markets WHERE id = $1::uuid`, marketID).Scan(&state); err != nil {
		t.Fatalf("read market state: %v", err)
	}
	return state
}

func wagerState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID string) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM wagers WHERE id = $1::uuid`, wagerID).Scan(&state); err != nil {
		t.Fatalf("read wager state: %v", err)
	}
	return state
}
