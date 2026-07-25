package betting

import (
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

func acceptedWagerForVoid(t *testing.T) Wager {
	t.Helper()
	wager, err := PlaceWager(placeCommand())
	if err != nil {
		t.Fatal(err)
	}
	wager.State = WagerAccepted
	return wager
}

func voidRefs() VoidWagerAccountRefs {
	return VoidWagerAccountRefs{UserFundingAccountID: "user-1-cash", EscrowAccountID: "escrow"}
}

func TestVoidWagerReturnsTheStakeAndNothingElse(t *testing.T) {
	t.Parallel()
	wager := acceptedWagerForVoid(t)
	at := time.Date(2026, time.July, 25, 18, 0, 0, 0, time.UTC)

	voided, transaction, err := VoidWager(wager, testAdminID, "taken in error", voidRefs(), at)
	if err != nil {
		t.Fatalf("VoidWager() error = %v", err)
	}
	if voided.State != WagerVoided {
		t.Fatalf("state = %s, want voided", voided.State)
	}
	// The stake goes back exactly as it came, and no profit or loss is booked.
	if len(transaction.Postings) != 2 {
		t.Fatalf("postings = %d, want 2", len(transaction.Postings))
	}
	var escrow, member int64
	for _, posting := range transaction.Postings {
		switch posting.AccountID {
		case "escrow":
			escrow = posting.Amount.Cents
		case "user-1-cash":
			member = posting.Amount.Cents
		}
	}
	if escrow != -wager.Stake.Cents || member != wager.Stake.Cents {
		t.Fatalf("postings escrow %d / member %d, want -%d / %d",
			escrow, member, wager.Stake.Cents, wager.Stake.Cents)
	}
	if transaction.Type != ledger.TransactionWagerRefund {
		t.Fatalf("transaction type = %q, want wager_refund", transaction.Type)
	}
	if transaction.Reason != "taken in error" {
		t.Fatalf("reason = %q, want it recorded on the ledger entry", transaction.Reason)
	}
	// The key must not collide with a settlement of the same wager.
	if transaction.IdempotencyKey == "wager:"+string(wager.ID)+":settlement:v1" {
		t.Fatal("the void reuses the settlement idempotency key")
	}
	if err := transaction.Validate(); err != nil {
		t.Fatalf("transaction Validate() error = %v", err)
	}
	// The snapshot the bet was struck at is untouched.
	if voided.Stake != wager.Stake || voided.AcceptedOdds != wager.AcceptedOdds {
		t.Fatal("voiding rewrote the wager's stake or odds")
	}
}

func TestVoidWagerOnlyAppliesToAnAcceptedWager(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.July, 25, 18, 0, 0, 0, time.UTC)

	// A pending wager is rejected instead: no money has moved, so there is
	// nothing to return.
	pending := acceptedWagerForVoid(t)
	pending.State = WagerPending
	if _, _, err := VoidWager(pending, testAdminID, "not yet", voidRefs(), at); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("voiding a pending wager error = %v, want ErrInvalidTransition", err)
	}

	for _, state := range []WagerState{WagerSettled, WagerVoided, WagerRejected} {
		wager := acceptedWagerForVoid(t)
		wager.State = state
		if _, _, err := VoidWager(wager, testAdminID, "too late", voidRefs(), at); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("voiding a %s wager error = %v, want ErrInvalidTransition", state, err)
		}
	}
}

func TestVoidWagerRefusesBadInput(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.July, 25, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		actor   ID
		reason  string
		refs    VoidWagerAccountRefs
		wantErr error
	}{
		{"no reason", testAdminID, "   ", voidRefs(), ErrReasonRequired},
		{"no actor", "", "taken in error", voidRefs(), ErrUnauthorized},
		{"missing accounts", testAdminID, "taken in error", VoidWagerAccountRefs{}, ErrInvalid},
		{"same account twice", testAdminID, "taken in error",
			VoidWagerAccountRefs{UserFundingAccountID: "same", EscrowAccountID: "same"}, ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := VoidWager(acceptedWagerForVoid(t), test.actor, test.reason, test.refs, at)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("VoidWager() error = %v, want %v", err, test.wantErr)
			}
		})
	}
	if _, _, err := VoidWager(acceptedWagerForVoid(t), testAdminID, "taken in error", voidRefs(), time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("voiding without a time error = %v, want ErrInvalid", err)
	}
}
