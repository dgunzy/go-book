package betting

import (
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// ReduceWager cuts an accepted wager's stake without touching its price, and
// returns the difference to the member.
//
// The alternative — void the bet and place a smaller one — only works while
// the line has not moved. Once it has, re-placing fills at the new price, and
// the void itself reprices the market twice on the way through. This keeps the
// odds the member was accepted at and moves only money.
//
// The refund is a new ledger transaction, never an edit of the acceptance:
// escrow out, member in, for the reduction alone. Profit is recomputed from
// the unchanged accepted odds, so a $3,000 bet at -180 cut to $2,000 wins
// $1,111.11 instead of $1,666.67.
//
// Only downward. Increasing a stake would be new money at a price that is no
// longer offered, which is a fresh wager and has to go through placement so it
// meets the stake caps, credit limit, and payout ceiling.
func ReduceWager(wager Wager, newStake ledger.Money, actor ID, reason string, refs VoidWagerAccountRefs, occurredAt time.Time) (Wager, ledger.Money, ledger.Transaction, error) {
	var noMoney ledger.Money
	if err := wager.Validate(); err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, err
	}
	if wager.State != WagerAccepted {
		return Wager{}, noMoney, ledger.Transaction{}, transitionErr("reduce wager", string(wager.State))
	}
	if !validID(actor) {
		return Wager{}, noMoney, ledger.Transaction{}, ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Wager{}, noMoney, ledger.Transaction{}, ErrReasonRequired
	}
	if err := refs.validate(); err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, err
	}
	if occurredAt.IsZero() {
		return Wager{}, noMoney, ledger.Transaction{}, invalidf("reducing a wager requires an occurrence time")
	}
	if err := newStake.Validate(); err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, err
	}
	if newStake.Currency != wager.Stake.Currency {
		return Wager{}, noMoney, ledger.Transaction{}, invalidf("a reduced stake must stay in %s", wager.Stake.Currency)
	}
	if newStake.Cents <= 0 {
		return Wager{}, noMoney, ledger.Transaction{}, invalidf("a reduced stake must be greater than zero; void the wager to remove it entirely")
	}
	if newStake.Cents >= wager.Stake.Cents {
		return Wager{}, noMoney, ledger.Transaction{}, invalidf("a wager can only be reduced: %s is not less than the current stake %s", stakeText(newStake), stakeText(wager.Stake))
	}

	refund, err := ledger.NewMoney(wager.Stake.Cents-newStake.Cents, wager.Stake.Currency)
	if err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, err
	}
	negatedRefund, err := refund.Negate()
	if err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, err
	}
	profit, err := wager.AcceptedOdds.Profit(newStake)
	if err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, err
	}

	transaction := ledger.Transaction{
		Type:     ledger.TransactionWagerRefund,
		Currency: wager.Stake.Currency,
		// Keyed on the resulting stake, so resubmitting the same reduction is
		// a no-op while a genuine second reduction to a different figure still
		// posts. A full void keys on the wager alone and cannot collide here.
		IdempotencyKey: ReduceIdempotencyKey(wager.ID, newStake.Cents),
		Actor:          string(actor),
		SourceType:     "wager",
		SourceID:       string(wager.ID),
		// The reason is what the member sees against this line in their own
		// ledger, so it states the mechanics before the admin's note.
		Reason: fmt.Sprintf("Stake reduced from %s to %s — %s", stakeText(wager.Stake), stakeText(newStake), reason),
		Postings: []ledger.Posting{
			{AccountID: refs.EscrowAccountID, Amount: negatedRefund},
			{AccountID: refs.UserFundingAccountID, Amount: refund},
		},
	}
	if err := transaction.Validate(); err != nil {
		return Wager{}, noMoney, ledger.Transaction{}, fmt.Errorf("build wager reduction transaction: %w", err)
	}

	wager.Stake = newStake
	wager.PotentialProfit = profit
	return wager, refund, transaction, nil
}

// ReduceIdempotencyKey is the ledger key for cutting one wager to a given
// stake. It includes the resulting stake so each distinct reduction posts
// once and only once.
func ReduceIdempotencyKey(wagerID ID, newStakeCents int64) string {
	return fmt.Sprintf("wager:%s:reduce:%d", wagerID, newStakeCents)
}

// stakeText renders an amount the way a member reads it, because this string
// becomes the description on their own ledger line.
func stakeText(amount ledger.Money) string {
	symbol := string(amount.Currency) + " "
	if amount.Currency == ledger.CAD {
		symbol = "CA$"
	}
	return fmt.Sprintf("%s%d.%02d", symbol, amount.Cents/100, amount.Cents%100)
}
