package ledger

import (
	"errors"
	"math"
	"testing"
)

func validTransaction() Transaction {
	return Transaction{
		Type:           TransactionWagerAcceptance,
		Currency:       CAD,
		IdempotencyKey: "accept:wager-1",
		Actor:          "user:user-1",
		SourceType:     "wager",
		SourceID:       "wager-1",
		Postings: []Posting{
			{AccountID: "user-cash", Amount: Money{Cents: -1000, Currency: CAD}},
			{AccountID: "escrow", Amount: Money{Cents: 1000, Currency: CAD}},
		},
	}
}

func TestTransactionValidate(t *testing.T) {
	t.Parallel()
	tx := validTransaction()
	if err := tx.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := tx.ExpectedPostingCount(); got != 2 {
		t.Fatalf("ExpectedPostingCount() = %d, want 2", got)
	}

	reversal := validTransaction()
	reversal.Type = TransactionReversal
	if err := reversal.Validate(); err == nil {
		t.Fatal("reversal without reference and reason unexpectedly passed")
	}
	reversal.ReversalOf = "transaction-1"
	reversal.Reason = "corrected settlement"
	if err := reversal.Validate(); err != nil {
		t.Fatalf("valid reversal error = %v", err)
	}
}

func TestValidatePostingsRejectsBrokenJournal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		postings []Posting
		want     error
	}{
		{"one posting", []Posting{{AccountID: "cash", Amount: Money{Cents: 1, Currency: CAD}}}, ErrUnbalancedPostings},
		{"unbalanced", []Posting{{AccountID: "cash", Amount: Money{Cents: -100, Currency: CAD}}, {AccountID: "escrow", Amount: Money{Cents: 99, Currency: CAD}}}, ErrUnbalancedPostings},
		{"zero", []Posting{{AccountID: "cash", Amount: Money{Cents: 0, Currency: CAD}}, {AccountID: "escrow", Amount: Money{Cents: 1, Currency: CAD}}}, ErrInvalidTransaction},
		{"duplicate account", []Posting{{AccountID: "cash", Amount: Money{Cents: -1, Currency: CAD}}, {AccountID: "cash", Amount: Money{Cents: 1, Currency: CAD}}}, ErrInvalidTransaction},
		{"currency mismatch", []Posting{{AccountID: "cash", Amount: Money{Cents: -1, Currency: CAD}}, {AccountID: "escrow", Amount: Money{Cents: 1, Currency: "USD"}}}, ErrInvalidTransaction},
		{"overflow", []Posting{{AccountID: "a", Amount: Money{Cents: math.MaxInt64, Currency: CAD}}, {AccountID: "b", Amount: Money{Cents: 1, Currency: CAD}}, {AccountID: "c", Amount: Money{Cents: math.MinInt64, Currency: CAD}}}, ErrAmountOverflow},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePostings(CAD, test.postings); !errors.Is(err, test.want) {
				t.Fatalf("ValidatePostings() error = %v, want %v", err, test.want)
			}
		})
	}
}
