package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// ErrWagerNotReducible is returned when a wager's market has already been
// decided, so there is nothing left to cut.
var ErrWagerNotReducible = errors.New("bettingpg: this wager's market is already settled or voided")

// ReduceWager cuts one accepted wager's stake and returns the difference to
// the member, keeping the price they were accepted at.
//
// It is the tool for "take me down from $3,000 to $2,000" once the line has
// moved past what the member was filled at, where voiding and re-betting would
// fill them at today's worse number and swing the board twice on the way.
//
// The wager row is locked first, exactly as VoidWager does, so a reduction
// racing a settlement or a void cannot both pay out: whichever commits first
// leaves the other looking at a wager that no longer matches. Repeating the
// same reduction is a no-op, because the refund's idempotency key is derived
// from the wager and the stake it is being cut to.
//
// Allowed while the market is open or closed but not yet decided: an admin
// often only gets the request once betting has stopped. A settled or voided
// market is refused — that money has already been paid or returned.
//
// The market is repriced afterwards: the reduction is action that is no longer
// on that side, so the line has to come back accordingly.
func (s Store) ReduceWager(ctx context.Context, wagerID string, newStakeCents int64, actorUserID, reason string) (betting.Wager, ledger.Money, error) {
	var noMoney ledger.Money
	if !isUUID(wagerID) {
		return betting.Wager{}, noMoney, fmt.Errorf("%w: wager %s", betting.ErrNotFound, wagerID)
	}
	if !isUUID(actorUserID) {
		return betting.Wager{}, noMoney, betting.ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return betting.Wager{}, noMoney, betting.ErrReasonRequired
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return betting.Wager{}, noMoney, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	wager, err := loadWagerForUpdate(ctx, tx, wagerID)
	if err != nil {
		return betting.Wager{}, noMoney, err
	}
	previousStake := wager.Stake
	previousProfit := wager.PotentialProfit

	// A decided market has already moved this money. The market row is read
	// without a lock for the same reason AcceptWager does: settlement locks
	// the market first and the wagers second, so taking it here would invert
	// that order.
	marketState, err := marketStateForWager(ctx, tx, string(wager.MarketID))
	if err != nil {
		return betting.Wager{}, noMoney, err
	}
	switch marketState {
	case betting.MarketSettled, betting.MarketVoided, betting.MarketCancelled:
		return betting.Wager{}, noMoney, fmt.Errorf("%w: market is %s", ErrWagerNotReducible, marketState)
	}

	newStake, err := ledger.NewMoney(newStakeCents, wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, noMoney, fmt.Errorf("%w: %s", betting.ErrInvalid, err)
	}

	// A repeat of the same reduction must not refund a second time.
	var alreadyReduced bool
	if err := tx.QueryRow(ctx, `
		SELECT exists(SELECT 1 FROM ledger_transactions WHERE currency = $1 AND idempotency_key = $2)`,
		string(wager.Stake.Currency), betting.ReduceIdempotencyKey(wager.ID, newStakeCents)).Scan(&alreadyReduced); err != nil {
		return betting.Wager{}, noMoney, fmt.Errorf("check existing wager reduction: %w", err)
	}
	if alreadyReduced {
		if err := tx.Commit(ctx); err != nil {
			return betting.Wager{}, noMoney, fmt.Errorf("commit wager reduction (idempotent): %w", err)
		}
		return wager, ledger.Money{Currency: wager.Stake.Currency}, nil
	}

	userAccountID, err := ensureUserAccount(ctx, tx, string(wager.UserID), wager.FundingAccountType, wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, noMoney, err
	}
	escrowAccountID, err := ensureSystemAccount(ctx, tx, "wager_escrow", wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, noMoney, err
	}

	reduced, refund, transaction, err := betting.ReduceWager(wager, newStake, betting.ID(actorUserID), reason,
		betting.VoidWagerAccountRefs{UserFundingAccountID: userAccountID, EscrowAccountID: escrowAccountID},
		time.Now())
	if err != nil {
		return betting.Wager{}, noMoney, err
	}
	if _, err := insertLedgerTransaction(ctx, tx, transaction); err != nil {
		return betting.Wager{}, noMoney, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wagers SET stake_cents = $2, potential_profit_cents = $3
		WHERE id = $1::uuid AND state = 'accepted' AND stake_cents = $4`,
		wagerID, reduced.Stake.Cents, reduced.PotentialProfit.Cents, previousStake.Cents)
	if err != nil {
		return betting.Wager{}, noMoney, fmt.Errorf("update reduced wager: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return betting.Wager{}, noMoney, fmt.Errorf("%w: wager %s is no longer accepted at that stake", betting.ErrInvalidTransition, wagerID)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'wager.reduced', 'wager', $2::uuid, $3,
		        jsonb_build_object('stake_cents', $4::bigint, 'potential_profit_cents', $5::bigint),
		        jsonb_build_object('stake_cents', $6::bigint, 'potential_profit_cents', $7::bigint))`,
		actorUserID, wagerID, reason,
		previousStake.Cents, previousProfit.Cents,
		reduced.Stake.Cents, reduced.PotentialProfit.Cents); err != nil {
		return betting.Wager{}, noMoney, fmt.Errorf("record wager reduction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, noMoney, fmt.Errorf("commit wager reduction: %w", err)
	}

	// The refunded part is no longer action on that side. A pricing failure
	// must not undo the refund, which is committed and correct on its own.
	if _, err := s.RepriceMarketAfterWager(ctx, string(wager.MarketID), wagerID); err != nil {
		return reduced, refund, nil
	}
	return reduced, refund, nil
}
