package betting

import (
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// VoidWagerAccountRefs names the two accounts a single-wager void moves money
// between: escrow, where the stake has been held since acceptance, and the
// member's funding account it came from.
type VoidWagerAccountRefs struct {
	UserFundingAccountID string
	EscrowAccountID      string
}

func (r VoidWagerAccountRefs) validate() error {
	if strings.TrimSpace(r.UserFundingAccountID) == "" || strings.TrimSpace(r.EscrowAccountID) == "" {
		return invalidf("voiding a wager needs the member's funding and escrow accounts")
	}
	if r.UserFundingAccountID == r.EscrowAccountID {
		return invalidf("funding and escrow accounts must be distinct")
	}
	return nil
}

// VoidWager cancels one accepted wager and returns its stake, leaving every
// other wager on the market alone. Voiding the market refunds everybody; this
// is the tool for the single bet that should not stand — taken in error, or
// on terms the book will not honour for that member.
//
// The stake goes back exactly as it came: escrow out, member in. No profit or
// loss is recorded for either side, because the bet never resolves. Only an
// accepted wager can be voided this way — a pending one is rejected instead,
// which moves no money because none has moved yet.
func VoidWager(wager Wager, actor ID, reason string, refs VoidWagerAccountRefs, occurredAt time.Time) (Wager, ledger.Transaction, error) {
	if err := wager.Validate(); err != nil {
		return Wager{}, ledger.Transaction{}, err
	}
	if !wager.State.CanTransitionTo(WagerVoided) {
		return Wager{}, ledger.Transaction{}, transitionErr("void wager", string(wager.State))
	}
	if !validID(actor) {
		return Wager{}, ledger.Transaction{}, ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Wager{}, ledger.Transaction{}, ErrReasonRequired
	}
	if err := refs.validate(); err != nil {
		return Wager{}, ledger.Transaction{}, err
	}
	if occurredAt.IsZero() {
		return Wager{}, ledger.Transaction{}, invalidf("voiding a wager requires an occurrence time")
	}

	negatedStake, err := wager.Stake.Negate()
	if err != nil {
		return Wager{}, ledger.Transaction{}, err
	}
	transaction := ledger.Transaction{
		Type:     ledger.TransactionWagerRefund,
		Currency: wager.Stake.Currency,
		// Distinct from the settlement key, so a void and a later market
		// settlement can never be mistaken for each other, and a repeated
		// void collides with itself instead of refunding twice.
		IdempotencyKey: VoidIdempotencyKey(wager.ID),
		Actor:          string(actor),
		SourceType:     "wager",
		SourceID:       string(wager.ID),
		Reason:         reason,
		Postings: []ledger.Posting{
			{AccountID: refs.EscrowAccountID, Amount: negatedStake},
			{AccountID: refs.UserFundingAccountID, Amount: wager.Stake},
		},
	}
	if err := transaction.Validate(); err != nil {
		return Wager{}, ledger.Transaction{}, fmt.Errorf("build wager void transaction: %w", err)
	}

	wager.State = WagerVoided
	return wager, transaction, nil
}

// VoidIdempotencyKey is the ledger key for voiding one wager on its own.
func VoidIdempotencyKey(wagerID ID) string {
	return fmt.Sprintf("wager:%s:void", wagerID)
}
