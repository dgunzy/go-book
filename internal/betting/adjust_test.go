package betting

import (
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

func adjustmentCommand() AdjustmentCommand {
	return AdjustmentCommand{
		AdjustmentID: "00000000-0000-4000-8000-00000000000a",
		UserID:       testUserID,
		Direction:    AdjustmentPaymentReceived,
		Amount:       ledger.Money{Cents: 50_000, Currency: ledger.CAD},
		Reason:       "e-transfer received, settling the season to date",
		Actor:        testAdminID,
		Refs: AdjustmentAccountRefs{
			UserFundingAccountID: "user-1-cash", HouseClearingAccountID: "house",
		},
		OccurredAt: time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC),
	}
}

// postingFor finds one account's amount in a transaction.
func postingFor(t *testing.T, txn ledger.Transaction, accountID string) int64 {
	t.Helper()
	for _, posting := range txn.Postings {
		if posting.AccountID == accountID {
			return posting.Amount.Cents
		}
	}
	t.Fatalf("no posting for account %q", accountID)
	return 0
}

func TestRecordAdjustmentMovesMoneyTheRightWay(t *testing.T) {
	t.Parallel()

	// A member who owes the book pays it: their balance rises toward zero and
	// the book's outstanding position falls by the same amount.
	paid, err := RecordAdjustment(adjustmentCommand())
	if err != nil {
		t.Fatalf("RecordAdjustment(payment) error = %v", err)
	}
	if postingFor(t, paid, "user-1-cash") != 50_000 || postingFor(t, paid, "house") != -50_000 {
		t.Fatalf("payment postings = %+v", paid.Postings)
	}

	// A payout is the mirror image.
	command := adjustmentCommand()
	command.Direction = AdjustmentPayoutSent
	sent, err := RecordAdjustment(command)
	if err != nil {
		t.Fatalf("RecordAdjustment(payout) error = %v", err)
	}
	if postingFor(t, sent, "user-1-cash") != -50_000 || postingFor(t, sent, "house") != 50_000 {
		t.Fatalf("payout postings = %+v", sent.Postings)
	}

	// Either way it is an admin adjustment, never a wager transaction, so
	// reporting that sums wager types is untouched by settling up.
	for _, txn := range []ledger.Transaction{paid, sent} {
		if txn.Type != ledger.TransactionAdminAdjustment {
			t.Fatalf("transaction type = %q, want admin_adjustment", txn.Type)
		}
		if txn.SourceType != AdjustmentSourceType || txn.SourceID != command.AdjustmentID {
			t.Fatalf("source = %s/%s", txn.SourceType, txn.SourceID)
		}
		if err := txn.Validate(); err != nil {
			t.Fatalf("transaction Validate() error = %v", err)
		}
	}
}

func TestRecordAdjustmentRefusesBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(AdjustmentCommand) AdjustmentCommand
		wantErr error
	}{
		{"no reason", func(c AdjustmentCommand) AdjustmentCommand { c.Reason = "   "; return c }, ErrReasonRequired},
		{"no actor", func(c AdjustmentCommand) AdjustmentCommand { c.Actor = ""; return c }, ErrUnauthorized},
		{"zero amount", func(c AdjustmentCommand) AdjustmentCommand { c.Amount.Cents = 0; return c }, ErrInvalid},
		{"negative amount", func(c AdjustmentCommand) AdjustmentCommand { c.Amount.Cents = -100; return c }, ErrInvalid},
		{"unknown direction", func(c AdjustmentCommand) AdjustmentCommand { c.Direction = "refund"; return c }, ErrInvalid},
		{"no member", func(c AdjustmentCommand) AdjustmentCommand { c.UserID = ""; return c }, ErrInvalid},
		{"missing accounts", func(c AdjustmentCommand) AdjustmentCommand { c.Refs = AdjustmentAccountRefs{}; return c }, ErrInvalid},
		{"same account twice", func(c AdjustmentCommand) AdjustmentCommand {
			c.Refs.HouseClearingAccountID = c.Refs.UserFundingAccountID
			return c
		}, ErrInvalid},
		{"no occurrence time", func(c AdjustmentCommand) AdjustmentCommand { c.OccurredAt = time.Time{}; return c }, ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := RecordAdjustment(test.mutate(adjustmentCommand())); !errors.Is(err, test.wantErr) {
				t.Fatalf("RecordAdjustment() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestReverseAdjustmentCancelsTheOriginalWithoutDeletingIt(t *testing.T) {
	t.Parallel()
	original, err := RecordAdjustment(adjustmentCommand())
	if err != nil {
		t.Fatal(err)
	}
	const originalID = "00000000-0000-4000-8000-00000000000b"

	reversal, err := ReverseAdjustment(original, originalID, testAdminID, "recorded against the wrong member")
	if err != nil {
		t.Fatalf("ReverseAdjustment() error = %v", err)
	}
	if reversal.Type != ledger.TransactionReversal || reversal.ReversalOf != originalID {
		t.Fatalf("reversal = %+v", reversal)
	}
	// Every posting is the exact inverse, so the pair nets to zero and the
	// member's balance returns to where it was before the mistake.
	var sum int64
	for _, posting := range append(append([]ledger.Posting{}, original.Postings...), reversal.Postings...) {
		sum += posting.Amount.Cents
	}
	if sum != 0 {
		t.Fatalf("original plus reversal = %d, want 0", sum)
	}
	if postingFor(t, reversal, "user-1-cash") != -50_000 || postingFor(t, reversal, "house") != 50_000 {
		t.Fatalf("reversal postings = %+v", reversal.Postings)
	}
	// The keys differ, so the reversal cannot collide with the original, and
	// re-reversing collides with itself rather than backing it out twice.
	if reversal.IdempotencyKey == original.IdempotencyKey {
		t.Fatal("reversal reused the original idempotency key")
	}
	if err := reversal.Validate(); err != nil {
		t.Fatalf("reversal Validate() error = %v", err)
	}
}

func TestReverseAdjustmentRefusesBadInput(t *testing.T) {
	t.Parallel()
	original, err := RecordAdjustment(adjustmentCommand())
	if err != nil {
		t.Fatal(err)
	}
	const originalID = "00000000-0000-4000-8000-00000000000b"

	if _, err := ReverseAdjustment(original, originalID, testAdminID, " "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("reversal without a reason error = %v, want ErrReasonRequired", err)
	}
	if _, err := ReverseAdjustment(original, originalID, "", "typo"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("reversal without an actor error = %v, want ErrUnauthorized", err)
	}
	if _, err := ReverseAdjustment(original, "", testAdminID, "typo"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reversal without the original error = %v, want ErrInvalid", err)
	}
	// A wager transaction is not reversible through the settle-up path; wagers
	// are unwound by voiding or re-settling their market.
	wagerTransaction := original
	wagerTransaction.Type = ledger.TransactionWagerAcceptance
	if _, err := ReverseAdjustment(wagerTransaction, originalID, testAdminID, "typo"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reversing a wager transaction error = %v, want ErrInvalid", err)
	}
}
