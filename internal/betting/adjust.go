package betting

import (
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// AdjustmentDirection says which way real-world money moved. It is deliberately
// separate from a signed amount so an admin picks "paid in" or "paid out"
// rather than typing a minus sign that is easy to get backwards.
type AdjustmentDirection string

const (
	// AdjustmentPaymentReceived records cash the member handed to the book,
	// which clears what they owe: their balance moves toward zero from below.
	AdjustmentPaymentReceived AdjustmentDirection = "payment_received"
	// AdjustmentPayoutSent records cash the book handed to the member, which
	// clears what the book owes: their balance moves toward zero from above.
	AdjustmentPayoutSent AdjustmentDirection = "payout_sent"
)

func (d AdjustmentDirection) Validate() error {
	switch d {
	case AdjustmentPaymentReceived, AdjustmentPayoutSent:
		return nil
	default:
		return invalidf("settlement direction %q is not supported", d)
	}
}

// AdjustmentAccountRefs names the two accounts every settle-up posts against:
// the member's funding account and the house clearing account. No wager is
// involved, so escrow is never touched.
type AdjustmentAccountRefs struct {
	UserFundingAccountID   string
	HouseClearingAccountID string
}

func (r AdjustmentAccountRefs) validate() error {
	if strings.TrimSpace(r.UserFundingAccountID) == "" || strings.TrimSpace(r.HouseClearingAccountID) == "" {
		return invalidf("a settlement needs both the member and house clearing accounts")
	}
	if r.UserFundingAccountID == r.HouseClearingAccountID {
		return invalidf("a settlement cannot post a member account against itself")
	}
	return nil
}

// AdjustmentCommand records money that changed hands outside the app — an
// e-transfer, cash, whatever — so a member's balance reflects that they have
// settled up. It never grades a wager and never touches escrow.
type AdjustmentCommand struct {
	AdjustmentID string
	UserID       ID
	Direction    AdjustmentDirection
	Amount       ledger.Money
	Reason       string
	Actor        ID
	Refs         AdjustmentAccountRefs
	OccurredAt   time.Time
}

// RecordAdjustment builds the balanced transaction for a settle-up. Money paid
// in credits the member and debits house clearing; money paid out does the
// reverse. House clearing therefore carries the book's net outstanding position
// — what it is still owed less what it still owes — while the wager
// transactions on the same account carry the betting result. Reporting tells
// the two apart by transaction type, so a settle-up never moves anyone's
// profit and loss.
func RecordAdjustment(command AdjustmentCommand) (ledger.Transaction, error) {
	if !validID(ID(command.AdjustmentID)) {
		return ledger.Transaction{}, invalidf("a settlement requires an adjustment ID")
	}
	if !validID(command.UserID) {
		return ledger.Transaction{}, invalidf("a settlement requires the member it belongs to")
	}
	if !validID(command.Actor) {
		return ledger.Transaction{}, ErrUnauthorized
	}
	if err := command.Direction.Validate(); err != nil {
		return ledger.Transaction{}, err
	}
	if err := command.Amount.Validate(); err != nil {
		return ledger.Transaction{}, err
	}
	if command.Amount.Cents <= 0 {
		return ledger.Transaction{}, invalidf("a settlement amount must be greater than zero")
	}
	reason := strings.TrimSpace(command.Reason)
	if reason == "" {
		return ledger.Transaction{}, ErrReasonRequired
	}
	if err := command.Refs.validate(); err != nil {
		return ledger.Transaction{}, err
	}
	if command.OccurredAt.IsZero() {
		return ledger.Transaction{}, invalidf("a settlement requires an occurrence time")
	}

	memberAmount := command.Amount
	if command.Direction == AdjustmentPayoutSent {
		negated, err := command.Amount.Negate()
		if err != nil {
			return ledger.Transaction{}, err
		}
		memberAmount = negated
	}
	houseAmount, err := memberAmount.Negate()
	if err != nil {
		return ledger.Transaction{}, err
	}

	transaction := ledger.Transaction{
		Type:           ledger.TransactionAdminAdjustment,
		Currency:       command.Amount.Currency,
		IdempotencyKey: AdjustmentIdempotencyKey(command.AdjustmentID),
		Actor:          string(command.Actor),
		SourceType:     AdjustmentSourceType,
		SourceID:       command.AdjustmentID,
		Reason:         reason,
		Postings: []ledger.Posting{
			{AccountID: command.Refs.UserFundingAccountID, Amount: memberAmount},
			{AccountID: command.Refs.HouseClearingAccountID, Amount: houseAmount},
		},
	}
	if err := transaction.Validate(); err != nil {
		return ledger.Transaction{}, err
	}
	return transaction, nil
}

// AdjustmentSourceType tags settle-up transactions in the ledger so reporting
// can find them without parsing reasons.
const AdjustmentSourceType = "settlement"

// AdjustmentIdempotencyKey keeps a double-submitted settle-up from posting
// twice: the second insert collides on (currency, idempotency_key).
func AdjustmentIdempotencyKey(adjustmentID string) string {
	return fmt.Sprintf("settlement:%s", adjustmentID)
}

// ReversalIdempotencyKey is the key for the correcting entry that backs out a
// settle-up. It is derived from the original, so a double-submitted reversal
// collides rather than backing the entry out twice.
func ReversalIdempotencyKey(adjustmentID string) string {
	return fmt.Sprintf("settlement:%s:reversal", adjustmentID)
}

// ReverseAdjustment builds the correcting entry for a settle-up that should
// not have been recorded. Ledger history is immutable: nothing is deleted or
// edited, so a mistake leaves two visible rows — the original and the reversal
// that cancels it — and the member's balance returns to where it started.
func ReverseAdjustment(original ledger.Transaction, originalTransactionID string, actor ID, reason string) (ledger.Transaction, error) {
	if original.Type != ledger.TransactionAdminAdjustment {
		return ledger.Transaction{}, invalidf("only a recorded settlement can be reversed here")
	}
	if !validID(ID(originalTransactionID)) {
		return ledger.Transaction{}, invalidf("a reversal requires the original transaction")
	}
	if !validID(actor) {
		return ledger.Transaction{}, ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ledger.Transaction{}, ErrReasonRequired
	}

	postings := make([]ledger.Posting, 0, len(original.Postings))
	for _, posting := range original.Postings {
		negated, err := posting.Amount.Negate()
		if err != nil {
			return ledger.Transaction{}, err
		}
		postings = append(postings, ledger.Posting{AccountID: posting.AccountID, Amount: negated})
	}

	reversal := ledger.Transaction{
		Type:           ledger.TransactionReversal,
		Currency:       original.Currency,
		IdempotencyKey: ReversalIdempotencyKey(original.SourceID),
		Actor:          string(actor),
		SourceType:     AdjustmentSourceType,
		SourceID:       original.SourceID,
		Reason:         reason,
		ReversalOf:     originalTransactionID,
		Postings:       postings,
	}
	if err := reversal.Validate(); err != nil {
		return ledger.Transaction{}, err
	}
	return reversal, nil
}
