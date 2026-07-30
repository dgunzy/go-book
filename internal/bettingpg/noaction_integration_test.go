package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// TestCloseMarketWithoutActionRetiresAnUnbetMarket covers the case the feature
// exists for: a prop nobody touched should not make anyone work out a winner,
// and closing it out must not look like a settlement, because no money moved.
func TestCloseMarketWithoutActionRetiresAnUnbetMarket(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 50_000)
	marketID, _ := cappedMarket(t, ctx, store, 0, f.UserB)
	if err := store.CloseMarket(ctx, marketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}

	if err := store.CloseMarketWithoutAction(ctx, marketID, f.UserB, "nobody bet it"); err != nil {
		t.Fatalf("CloseMarketWithoutAction() error = %v", err)
	}

	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM markets WHERE id = $1::uuid`, marketID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(betting.MarketNoAction) {
		t.Fatalf("market state = %q, want closed_no_action", state)
	}

	// No settlement row: reconciliation reads those, and one here would claim
	// money moved when none ever did.
	var settlements, ledgerRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM market_settlements WHERE market_id = $1::uuid`, marketID).Scan(&settlements); err != nil {
		t.Fatal(err)
	}
	if settlements != 0 {
		t.Fatalf("no-action close wrote %d settlement rows, want 0", settlements)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transactions lt
		WHERE lt.idempotency_key LIKE 'market:' || $1 || '%'`, marketID).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 0 {
		t.Fatalf("no-action close wrote %d ledger transactions, want 0", ledgerRows)
	}

	var audits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_entries
		WHERE action = 'market.closed_no_action' AND target_id = $1::uuid`, marketID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit entries = %d, want 1", audits)
	}
}

// TestCloseMarketWithoutActionRefusesAMarketWithMoneyOnIt is the guard that
// matters: skipping the grade on a market someone has money on would strand
// their stake in escrow with nothing to resolve it.
func TestCloseMarketWithoutActionRefusesAMarketWithMoneyOnIt(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 50_000)
	marketID, selections := cappedMarket(t, ctx, store, 0, f.UserB)

	wagerID := mustNewUUID(t, ctx, store)
	if _, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: f.UserA, MarketID: marketID, SelectionID: selections[0],
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "noaction:" + wagerID,
	}); err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	if err := store.CloseMarket(ctx, marketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}

	// A pending wager counts. It is money trying to get on, and approving it
	// after the market had been retired would leave it unresolvable.
	if err := store.CloseMarketWithoutAction(ctx, marketID, f.UserB, "trying to skip a graded market"); !errors.Is(err, ErrMarketHasWagers) {
		t.Fatalf("error = %v, want ErrMarketHasWagers", err)
	}

	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM markets WHERE id = $1::uuid`, marketID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(betting.MarketClosed) {
		t.Fatalf("market state = %q, want it left closed", state)
	}
}

// TestCloseMarketWithoutActionRequiresAReasonAndAValidState keeps the audit
// trail honest and the state machine closed.
func TestCloseMarketWithoutActionRequiresAReasonAndAValidState(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 50_000)
	marketID, _ := cappedMarket(t, ctx, store, 0, f.UserB)

	// Still open: closing out without action is a decision about a market that
	// has already stopped taking bets.
	if err := store.CloseMarketWithoutAction(ctx, marketID, f.UserB, "too early"); !errors.Is(err, ErrMarketNotSettleable) {
		t.Fatalf("open market error = %v, want ErrMarketNotSettleable", err)
	}
	if err := store.CloseMarket(ctx, marketID, f.UserB); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseMarketWithoutAction(ctx, marketID, f.UserB, "   "); !errors.Is(err, betting.ErrReasonRequired) {
		t.Fatalf("blank reason error = %v, want ErrReasonRequired", err)
	}
	if err := store.CloseMarketWithoutAction(ctx, marketID, f.UserB, "fine"); err != nil {
		t.Fatal(err)
	}
	// Not repeatable: it is already retired.
	if err := store.CloseMarketWithoutAction(ctx, marketID, f.UserB, "again"); !errors.Is(err, ErrMarketNotSettleable) {
		t.Fatalf("second close error = %v, want ErrMarketNotSettleable", err)
	}
}
