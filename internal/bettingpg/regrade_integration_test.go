package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5/pgxpool"
)

// strandWager reproduces the state the live book ended up in on 2026-07-26: a
// wager accepted for real (its stake genuinely in escrow) that the market's
// settlement never graded, because it was accepted after grading ran.
// AcceptWager now refuses that, so the sequence is staged here: accept while
// the market is open, hide the wager from settlement, then restore it.
func strandWager(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID string, settle func()) {
	t.Helper()
	setWagerState(t, ctx, pool, wagerID, string(betting.WagerPending))
	settle()
	setWagerState(t, ctx, pool, wagerID, string(betting.WagerAccepted))
}

func setWagerState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID, state string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE wagers SET state = $2 WHERE id = $1::uuid`, wagerID, state); err != nil {
		t.Fatalf("stage wager state %s: %v", state, err)
	}
}

func profitCentsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID string) int64 {
	t.Helper()
	var profit int64
	if err := pool.QueryRow(ctx, `SELECT potential_profit_cents FROM wagers WHERE id = $1::uuid`, wagerID).Scan(&profit); err != nil {
		t.Fatalf("read potential profit: %v", err)
	}
	return profit
}

func settlementVersions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marketID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM market_settlements WHERE market_id = $1::uuid`, marketID).Scan(&count); err != nil {
		t.Fatalf("count settlements: %v", err)
	}
	return count
}

func TestRegradeGradesTheWagerLeftBehindAndPaysIt(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	// The wager that settles with the market, and the one that will be left
	// behind. Both back the winning side.
	graded := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 10_000, 1)
	stranded := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 50_000, 2)

	strandWager(t, ctx, pool, string(stranded.ID), func() {
		if _, err := store.SettleMarket(ctx, SettleMarketRequest{
			MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
			Outcome: map[string]betting.SettlementResult{
				f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
			},
		}); err != nil {
			t.Fatalf("SettleMarket() error = %v", err)
		}
	})
	rows, err := store.StrandedWagers(ctx, f.MarketID)
	if err != nil {
		t.Fatalf("StrandedWagers() error = %v", err)
	}
	if len(rows) != 1 || rows[0].WagerID != string(stranded.ID) {
		t.Fatalf("stranded wagers = %+v, want just the one left behind", rows)
	}
	if rows[0].Result != betting.ResultWin {
		t.Fatalf("stranded wager grades as %q, want win from the recorded outcome", rows[0].Result)
	}

	balanceBefore := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	profit := profitCentsFor(t, ctx, pool, string(stranded.ID))

	report, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "accepted after the market settled")
	if err != nil {
		t.Fatalf("RegradeStrandedWagers() error = %v", err)
	}
	if report.WinCount != 1 || report.LossCount != 0 {
		t.Fatalf("regrade report = %+v, want exactly one win", report)
	}
	if report.Version < 2 {
		t.Fatalf("regrade version = %d, want a new settlement version", report.Version)
	}

	// The member gets their stake back plus the profit their price earned.
	want := balanceBefore + 50_000 + profit
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != want {
		t.Fatalf("balance after regrade = %d, want %d (stake 50000 + profit %d)", balance, want, profit)
	}
	if state := wagerState(t, ctx, pool, string(stranded.ID)); state != string(betting.WagerSettled) {
		t.Fatalf("regraded wager state = %q, want settled", state)
	}
	// The wager that settled the first time round is untouched.
	if state := wagerState(t, ctx, pool, string(graded.ID)); state != string(betting.WagerSettled) {
		t.Fatalf("originally graded wager state = %q, want still settled", state)
	}
}

// The regrade must never pay a wager the original settlement already paid.
func TestRegradeDoesNotPayAnAlreadySettledWagerTwice(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 10_000, 1)
	stranded := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 5_000, 2)
	strandWager(t, ctx, pool, string(stranded.ID), func() {
		if _, err := store.SettleMarket(ctx, SettleMarketRequest{
			MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
			Outcome: map[string]betting.SettlementResult{
				f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
			},
		}); err != nil {
			t.Fatalf("SettleMarket() error = %v", err)
		}
	})
	balanceBeforeRegrade := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if _, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "first regrade"); err != nil {
		t.Fatalf("RegradeStrandedWagers() error = %v", err)
	}
	afterFirst := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if afterFirst <= balanceBeforeRegrade {
		t.Fatalf("first regrade did not pay: %d then %d", balanceBeforeRegrade, afterFirst)
	}

	// A second run has nothing left to do and must not move a cent.
	_, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "second regrade")
	if !errors.Is(err, ErrNothingToRegrade) {
		t.Fatalf("second RegradeStrandedWagers() error = %v, want ErrNothingToRegrade", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != afterFirst {
		t.Fatalf("balance moved on a repeat regrade: %d, want unchanged %d", balance, afterFirst)
	}
}

// A market with nothing left behind must refuse rather than write an empty
// settlement version.
func TestRegradeRefusesWhenNothingWasLeftBehind(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 20_000)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if _, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "nothing to do"); !errors.Is(err, ErrNothingToRegrade) {
		t.Fatalf("RegradeStrandedWagers() error = %v, want ErrNothingToRegrade", err)
	}
	if versions := settlementVersions(t, ctx, pool, f.MarketID); versions != 1 {
		t.Fatalf("settlement versions = %d, want the regrade to write none", versions)
	}
}

// The regrade replays the market's recorded result. It takes no outcome from
// the caller, so a losing side stays a losing side.
func TestRegradeReplaysTheRecordedOutcomeNotANewOne(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 10_000, 1)
	// The stranded wager backs the side that LOST.
	stranded := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionBID, 20_000, 2)
	strandWager(t, ctx, pool, string(stranded.ID), func() {
		if _, err := store.SettleMarket(ctx, SettleMarketRequest{
			MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
			Outcome: map[string]betting.SettlementResult{
				f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
			},
		}); err != nil {
			t.Fatalf("SettleMarket() error = %v", err)
		}
	})
	balanceBefore := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	report, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "left behind, backed the loser")
	if err != nil {
		t.Fatalf("RegradeStrandedWagers() error = %v", err)
	}
	if report.LossCount != 1 || report.WinCount != 0 {
		t.Fatalf("regrade report = %+v, want the loser graded as a loss", report)
	}
	// A loss returns nothing: the stake was already taken at acceptance.
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != balanceBefore {
		t.Fatalf("balance after grading a loser = %d, want unchanged %d", balance, balanceBefore)
	}
}

// A market that is not settled has no recorded outcome to replay, so the
// regrade must not be usable as a back door around normal settlement.
func TestRegradeRefusesAMarketThatIsNotSettled(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 20_000)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	if _, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "not settled yet"); !errors.Is(err, ErrNothingToRegrade) {
		t.Fatalf("RegradeStrandedWagers() on an open market error = %v, want ErrNothingToRegrade", err)
	}
	if state := marketState(t, ctx, pool, f.MarketID); state != string(betting.MarketOpen) {
		t.Fatalf("market state = %q, want it left open", state)
	}
}

// A reason is recorded with every settlement, including this one.
func TestRegradeRequiresAReason(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 20_000)
	if _, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "   "); !errors.Is(err, betting.ErrReasonRequired) {
		t.Fatalf("RegradeStrandedWagers() with a blank reason error = %v, want ErrReasonRequired", err)
	}
}
