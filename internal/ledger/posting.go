package ledger

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrInvalidTransaction = errors.New("invalid ledger transaction")
	ErrUnbalancedPostings = errors.New("ledger postings do not balance")
)

type TransactionType string

const (
	TransactionWagerAcceptance TransactionType = "wager_acceptance"
	TransactionWagerWin        TransactionType = "wager_win"
	TransactionWagerLoss       TransactionType = "wager_loss"
	TransactionWagerRefund     TransactionType = "wager_refund"
	TransactionAdminAdjustment TransactionType = "admin_adjustment"
	TransactionMigrationAdjust TransactionType = "migration_adjustment"
	TransactionReversal        TransactionType = "reversal"
)

func (t TransactionType) Validate() error {
	switch t {
	case TransactionWagerAcceptance, TransactionWagerWin, TransactionWagerLoss,
		TransactionWagerRefund, TransactionAdminAdjustment, TransactionMigrationAdjust,
		TransactionReversal:
		return nil
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidTransaction, t)
	}
}

type Posting struct {
	AccountID string
	Amount    Money
}

// Transaction is the storage-independent input required to post immutable ledger
// history. Actor is explicit even for automated work (for example,
// "system:settlement-worker").
type Transaction struct {
	Type           TransactionType
	Currency       Currency
	IdempotencyKey string
	Actor          string
	SourceType     string
	SourceID       string
	Reason         string
	ReversalOf     string
	Postings       []Posting
}

// ExpectedPostingCount is persisted with the immutable transaction header. The
// database compares it to the deferred posting count, preventing later balanced
// postings from being appended to an already committed transaction.
func (t Transaction) ExpectedPostingCount() int {
	return len(t.Postings)
}

func (t Transaction) Validate() error {
	if err := t.Type.Validate(); err != nil {
		return err
	}
	if err := t.Currency.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTransaction, err)
	}
	if strings.TrimSpace(t.IdempotencyKey) == "" || strings.TrimSpace(t.Actor) == "" ||
		strings.TrimSpace(t.SourceType) == "" || strings.TrimSpace(t.SourceID) == "" {
		return fmt.Errorf("%w: idempotency key, actor, and source reference are required", ErrInvalidTransaction)
	}
	if t.Type == TransactionReversal && strings.TrimSpace(t.ReversalOf) == "" {
		return fmt.Errorf("%w: reversal must reference the original transaction", ErrInvalidTransaction)
	}
	if (t.Type == TransactionAdminAdjustment || t.Type == TransactionMigrationAdjust || t.Type == TransactionReversal) &&
		strings.TrimSpace(t.Reason) == "" {
		return fmt.Errorf("%w: adjustment and reversal transactions require a reason", ErrInvalidTransaction)
	}
	return ValidatePostings(t.Currency, t.Postings)
}

// ValidatePostings enforces the same rules as the deferred PostgreSQL ledger
// constraint: at least two non-zero, single-currency postings that sum to zero.
func ValidatePostings(currency Currency, postings []Posting) error {
	if err := currency.Validate(); err != nil {
		return err
	}
	if len(postings) < 2 {
		return fmt.Errorf("%w: at least two postings are required", ErrUnbalancedPostings)
	}

	seen := make(map[string]struct{}, len(postings))
	var sum int64
	for i, posting := range postings {
		accountID := strings.TrimSpace(posting.AccountID)
		if accountID == "" {
			return fmt.Errorf("%w: posting %d has no account", ErrInvalidTransaction, i)
		}
		if _, exists := seen[accountID]; exists {
			return fmt.Errorf("%w: account %q is posted more than once", ErrInvalidTransaction, accountID)
		}
		seen[accountID] = struct{}{}
		if err := posting.Amount.Validate(); err != nil {
			return fmt.Errorf("%w: posting %d: %v", ErrInvalidTransaction, i, err)
		}
		if posting.Amount.Currency != currency {
			return fmt.Errorf("%w: posting %d: %v", ErrInvalidTransaction, i, ErrCurrencyMismatch)
		}
		if posting.Amount.Cents == 0 {
			return fmt.Errorf("%w: posting %d is zero", ErrInvalidTransaction, i)
		}
		amount := posting.Amount.Cents
		if (amount > 0 && sum > math.MaxInt64-amount) ||
			(amount < 0 && sum < math.MinInt64-amount) {
			return ErrAmountOverflow
		}
		sum += amount
	}
	if sum != 0 {
		return fmt.Errorf("%w: net is %d cents", ErrUnbalancedPostings, sum)
	}
	return nil
}
