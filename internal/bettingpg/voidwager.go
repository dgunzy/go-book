package bettingpg

import (
	"context"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
)

// VoidWager cancels one accepted wager and returns its stake, leaving the rest
// of the market untouched. Voiding the market refunds everybody; this is the
// tool for the single bet that should not stand.
//
// The wager row is locked first, so a void racing a market settlement cannot
// both pay out: whichever commits first leaves the other looking at a wager
// that is no longer accepted. Repeating a void is a no-op — the refund's
// idempotency key is derived from the wager, so a second attempt cannot return
// the stake twice.
//
// The market is repriced afterwards: the stake no longer counts as action, so
// the line must come back accordingly.
func (s Store) VoidWager(ctx context.Context, wagerID, actorUserID, reason string) (betting.Wager, error) {
	if !isUUID(wagerID) {
		return betting.Wager{}, fmt.Errorf("%w: wager %s", betting.ErrNotFound, wagerID)
	}
	if !isUUID(actorUserID) {
		return betting.Wager{}, betting.ErrUnauthorized
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return betting.Wager{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	wager, err := loadWagerForUpdate(ctx, tx, wagerID)
	if err != nil {
		return betting.Wager{}, err
	}

	// A wager already voided on its own is left as it is rather than refunded
	// again, so a double-submitted form is harmless.
	if wager.State == betting.WagerVoided {
		var alreadyRefunded bool
		if err := tx.QueryRow(ctx, `
			SELECT exists(SELECT 1 FROM ledger_transactions
			WHERE currency = $1 AND idempotency_key = $2)`,
			string(wager.Stake.Currency), betting.VoidIdempotencyKey(wager.ID)).Scan(&alreadyRefunded); err != nil {
			return betting.Wager{}, fmt.Errorf("check existing wager void: %w", err)
		}
		if alreadyRefunded {
			if err := tx.Commit(ctx); err != nil {
				return betting.Wager{}, fmt.Errorf("commit wager void (idempotent): %w", err)
			}
			return wager, nil
		}
	}

	userAccountID, err := ensureUserAccount(ctx, tx, string(wager.UserID), wager.FundingAccountType, wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, err
	}
	escrowAccountID, err := ensureSystemAccount(ctx, tx, "wager_escrow", wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, err
	}

	voided, transaction, err := betting.VoidWager(wager, betting.ID(actorUserID), reason,
		betting.VoidWagerAccountRefs{UserFundingAccountID: userAccountID, EscrowAccountID: escrowAccountID},
		time.Now())
	if err != nil {
		return betting.Wager{}, err
	}
	if _, err := insertLedgerTransaction(ctx, tx, transaction); err != nil {
		return betting.Wager{}, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wagers SET state = 'voided' WHERE id = $1::uuid AND state = 'accepted'`, wagerID)
	if err != nil {
		return betting.Wager{}, fmt.Errorf("update wager to voided: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return betting.Wager{}, fmt.Errorf("%w: wager %s is no longer accepted", betting.ErrInvalidTransition, wagerID)
	}

	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, fmt.Errorf("commit wager void: %w", err)
	}

	// The voided stake is no longer action on that side, so the board has to
	// come back. A pricing failure must not undo the refund, which is already
	// committed and correct on its own.
	if _, err := s.RepriceMarketAfterWager(ctx, string(wager.MarketID), wagerID); err != nil {
		return voided, nil
	}
	return voided, nil
}
