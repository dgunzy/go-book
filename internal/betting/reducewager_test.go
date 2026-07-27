package betting

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

func reducibleWager(t *testing.T) Wager {
	t.Helper()
	odds, err := ledger.NewAmericanOdds(-180)
	if err != nil {
		t.Fatal(err)
	}
	stake := ledger.Money{Cents: 300_000, Currency: ledger.CAD}
	profit, err := odds.Profit(stake)
	if err != nil {
		t.Fatal(err)
	}
	return Wager{
		ID: "11111111-1111-1111-1111-111111111111", UserID: "22222222-2222-2222-2222-222222222222",
		MarketID: "33333333-3333-3333-3333-333333333333", SelectionID: "44444444-4444-4444-4444-444444444444",
		FundingAccountType: FundingUserCash, Stake: stake, AcceptedOdds: odds,
		AcceptedTerms: "DC, Ryan W to win", PotentialProfit: profit, State: WagerAccepted,
		IdempotencyKey: "k", PlacedAt: time.Now(),
	}
}

var reduceRefs = VoidWagerAccountRefs{UserFundingAccountID: "a", EscrowAccountID: "b"}

// The whole point: the member keeps the price they were filled at, and only
// the money moves. $3,000 at -180 cut to $2,000 wins $1,111.11, not $1,666.67.
func TestReduceWagerKeepsThePriceAndRecomputesProfit(t *testing.T) {
	wager := reducibleWager(t)
	if wager.PotentialProfit.Cents != 166_667 {
		t.Fatalf("fixture profit = %d, want 166667", wager.PotentialProfit.Cents)
	}

	reduced, refund, transaction, err := ReduceWager(wager, ledger.Money{Cents: 200_000, Currency: ledger.CAD},
		"55555555-5555-5555-5555-555555555555", "member asked to come down", reduceRefs, time.Now())
	if err != nil {
		t.Fatalf("ReduceWager() error = %v", err)
	}
	if reduced.AcceptedOdds != wager.AcceptedOdds {
		t.Fatalf("odds moved to %v, want the accepted price %v held", reduced.AcceptedOdds, wager.AcceptedOdds)
	}
	if reduced.Stake.Cents != 200_000 {
		t.Fatalf("reduced stake = %d, want 200000", reduced.Stake.Cents)
	}
	if reduced.PotentialProfit.Cents != 111_111 {
		t.Fatalf("reduced profit = %d, want 111111", reduced.PotentialProfit.Cents)
	}
	if refund.Cents != 100_000 {
		t.Fatalf("refund = %d, want 100000", refund.Cents)
	}
	if reduced.State != WagerAccepted {
		t.Fatalf("state = %v, want the wager still accepted", reduced.State)
	}

	// Escrow out, member in, for the difference only.
	if transaction.Type != ledger.TransactionWagerRefund || len(transaction.Postings) != 2 {
		t.Fatalf("transaction = %+v", transaction)
	}
	if transaction.Postings[0].Amount.Cents != -100_000 || transaction.Postings[1].Amount.Cents != 100_000 {
		t.Fatalf("postings = %+v, want escrow -100000 and member +100000", transaction.Postings)
	}
	// The member reads this on their own ledger, so it has to say what happened.
	if !strings.Contains(transaction.Reason, "CA$3000.00") || !strings.Contains(transaction.Reason, "CA$2000.00") ||
		!strings.Contains(transaction.Reason, "member asked to come down") {
		t.Fatalf("reason = %q, want both stakes and the admin's note", transaction.Reason)
	}
}

// Increasing a stake would be new money at a price no longer offered, which
// has to go through placement so the caps and the payout ceiling apply.
func TestReduceWagerRefusesAnythingButADecrease(t *testing.T) {
	wager := reducibleWager(t)
	actor := ID("55555555-5555-5555-5555-555555555555")

	for _, stake := range []int64{300_000, 400_000} {
		if _, _, _, err := ReduceWager(wager, ledger.Money{Cents: stake, Currency: ledger.CAD}, actor, "why", reduceRefs, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Errorf("ReduceWager() to %d error = %v, want ErrInvalid", stake, err)
		}
	}
	// Zero is a void, and voiding has its own command with its own ledger key.
	if _, _, _, err := ReduceWager(wager, ledger.Money{Cents: 0, Currency: ledger.CAD}, actor, "why", reduceRefs, time.Now()); !errors.Is(err, ErrInvalid) {
		t.Errorf("ReduceWager() to zero error = %v, want ErrInvalid", err)
	}
}

func TestReduceWagerRequiresAnAcceptedWagerActorAndReason(t *testing.T) {
	actor := ID("55555555-5555-5555-5555-555555555555")
	newStake := ledger.Money{Cents: 200_000, Currency: ledger.CAD}

	for _, state := range []WagerState{WagerPending, WagerSettled, WagerVoided, WagerRejected} {
		wager := reducibleWager(t)
		wager.State = state
		if _, _, _, err := ReduceWager(wager, newStake, actor, "why", reduceRefs, time.Now()); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("ReduceWager() from %s error = %v, want ErrInvalidTransition", state, err)
		}
	}
	if _, _, _, err := ReduceWager(reducibleWager(t), newStake, "", "why", reduceRefs, time.Now()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("ReduceWager() without an actor error = %v, want ErrUnauthorized", err)
	}
	if _, _, _, err := ReduceWager(reducibleWager(t), newStake, actor, "   ", reduceRefs, time.Now()); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("ReduceWager() without a reason error = %v, want ErrReasonRequired", err)
	}
}

// Two different reductions must post separately; the same one twice must not.
func TestReduceIdempotencyKeyIsPerResultingStake(t *testing.T) {
	id := ID("11111111-1111-1111-1111-111111111111")
	if ReduceIdempotencyKey(id, 200_000) == ReduceIdempotencyKey(id, 150_000) {
		t.Fatal("two different reductions share a ledger key, so the second would be swallowed")
	}
	if ReduceIdempotencyKey(id, 200_000) != ReduceIdempotencyKey(id, 200_000) {
		t.Fatal("the same reduction produces different keys, so a resubmit would refund twice")
	}
	if ReduceIdempotencyKey(id, 200_000) == VoidIdempotencyKey(id) {
		t.Fatal("a reduction and a void share a ledger key")
	}
}
