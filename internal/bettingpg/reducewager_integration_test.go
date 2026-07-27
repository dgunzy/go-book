package bettingpg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ramy's case: $3,000 at -180 with the line since moved to -195, so voiding
// and re-betting would refill him at the worse number. The reduction keeps his
// price and hands back the difference.
func TestReduceWagerReturnsTheDifferenceAndKeepsThePrice(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	escrowBefore := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 300_000, 1)
	balanceAfterBet := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	priceBefore := wager.AcceptedOdds

	reduced, refund, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "member asked to come down to 2k")
	if err != nil {
		t.Fatalf("ReduceWager() error = %v", err)
	}
	if reduced.AcceptedOdds != priceBefore {
		t.Fatalf("price changed to %v, want %v held", reduced.AcceptedOdds, priceBefore)
	}
	if refund.Cents != 100_000 {
		t.Fatalf("refund = %d, want 100000", refund.Cents)
	}

	// The money is really back with the member and really out of escrow.
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != balanceAfterBet+100_000 {
		t.Fatalf("member balance = %d, want %d", balance, balanceAfterBet+100_000)
	}
	if escrow := systemAccountBalance(t, ctx, pool, "wager_escrow", f.Currency); escrow-escrowBefore != 200_000 {
		t.Fatalf("escrow delta = %d, want 200000 still held", escrow-escrowBefore)
	}

	stake, profit := stakeAndProfit(t, ctx, pool, string(wager.ID))
	if stake != 200_000 {
		t.Fatalf("stored stake = %d, want 200000", stake)
	}
	expected, err := priceBefore.Profit(reduced.Stake)
	if err != nil {
		t.Fatal(err)
	}
	if profit != expected.Cents {
		t.Fatalf("stored profit = %d, want %d recomputed at the held price", profit, expected.Cents)
	}
	if state := wagerState(t, ctx, pool, string(wager.ID)); state != string(betting.WagerAccepted) {
		t.Fatalf("wager state = %q, want it still accepted", state)
	}
}

// The member has to be able to see what happened, so the refund carries a
// description naming both stakes and the admin's reason.
func TestReduceWagerShowsUpOnTheMembersLedger(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 300_000, 1)
	if _, _, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "trimmed at his request"); err != nil {
		t.Fatalf("ReduceWager() error = %v", err)
	}

	var reason, txType string
	err := pool.QueryRow(ctx, `
		SELECT coalesce(t.reason, ''), t.transaction_type FROM ledger_transactions t
		WHERE t.source_id = $1::uuid AND t.idempotency_key LIKE 'wager:%:reduce:%'`, string(wager.ID)).Scan(&reason, &txType)
	if err != nil {
		t.Fatalf("no reduction transaction on the member's ledger: %v", err)
	}
	if txType != "wager_refund" {
		t.Fatalf("transaction type = %q, want wager_refund so the house result stays right", txType)
	}
	for _, want := range []string{"CA$3000.00", "CA$2000.00", "trimmed at his request"} {
		if !strings.Contains(reason, want) {
			t.Errorf("ledger description %q does not mention %q", reason, want)
		}
	}

	// And it is audited for the book's own record.
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE action = 'wager.reduced' AND target_id = $1::uuid`,
		string(wager.ID)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit entries = %d, want 1", audits)
	}
}

// A double-submitted form must not refund twice.
func TestReduceWagerRepeatIsANoOp(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 300_000, 1)
	if _, _, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "first"); err != nil {
		t.Fatalf("ReduceWager() error = %v", err)
	}
	after := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)

	if _, _, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "same again"); err != nil {
		t.Fatalf("repeat ReduceWager() error = %v", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != after {
		t.Fatalf("balance moved on a repeated reduction: %d, want unchanged %d", balance, after)
	}
	if stake, _ := stakeAndProfit(t, ctx, pool, string(wager.ID)); stake != 200_000 {
		t.Fatalf("stake after repeat = %d, want 200000", stake)
	}
}

// Reducing twice to different figures is legitimate and must land both times.
func TestReduceWagerCanBeAppliedAgainToASmallerStake(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 300_000, 1)
	if _, _, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "down to 2k"); err != nil {
		t.Fatalf("first ReduceWager() error = %v", err)
	}
	if _, refund, err := store.ReduceWager(ctx, string(wager.ID), 150_000, f.UserB, "down again to 1.5k"); err != nil {
		t.Fatalf("second ReduceWager() error = %v", err)
	} else if refund.Cents != 50_000 {
		t.Fatalf("second refund = %d, want 50000", refund.Cents)
	}
	if stake, _ := stakeAndProfit(t, ctx, pool, string(wager.ID)); stake != 150_000 {
		t.Fatalf("stake = %d, want 150000", stake)
	}
}

// Accepting is idempotent, and a reduced wager must not break that: the
// acceptance transaction is never rewritten, so the check has to allow for
// what reductions have handed back.
func TestAcceptIsStillIdempotentAfterAReduction(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 300_000, 1)
	if _, _, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "down to 2k"); err != nil {
		t.Fatalf("ReduceWager() error = %v", err)
	}
	balanceBefore := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)

	if _, err := store.AcceptWager(ctx, string(wager.ID), f.UserB); err != nil {
		t.Fatalf("AcceptWager() on a reduced wager error = %v, want it to stay a no-op", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != balanceBefore {
		t.Fatalf("balance moved on a repeat accept: %d, want unchanged %d", balance, balanceBefore)
	}
}

// Closed is fine — the request usually arrives once betting has stopped — but
// a decided market has already moved this money.
func TestReduceWagerAllowedWhenClosedAndRefusedOnceDecided(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 300_000, 1)
	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	if _, _, err := store.ReduceWager(ctx, string(wager.ID), 200_000, f.UserB, "asked after close"); err != nil {
		t.Fatalf("ReduceWager() on a closed market error = %v, want it allowed", err)
	}

	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin, f.SelectionBID: betting.ResultLoss,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	balanceAfterSettle := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	_, _, err := store.ReduceWager(ctx, string(wager.ID), 100_000, f.UserB, "too late")
	if !errors.Is(err, ErrWagerNotReducible) && !errors.Is(err, betting.ErrInvalidTransition) {
		t.Fatalf("ReduceWager() after settlement error = %v, want it refused", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != balanceAfterSettle {
		t.Fatalf("balance moved after a refused reduction: %d, want %d", balance, balanceAfterSettle)
	}
}

func stakeAndProfit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID string) (int64, int64) {
	t.Helper()
	var stake, profit int64
	if err := pool.QueryRow(ctx, `SELECT stake_cents, potential_profit_cents FROM wagers WHERE id = $1::uuid`,
		wagerID).Scan(&stake, &profit); err != nil {
		t.Fatalf("read reduced wager: %v", err)
	}
	return stake, profit
}
