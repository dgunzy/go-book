package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
)

// AutoApproveActor is the actor recorded on wagers accepted automatically by
// the auto-approve policy rather than by a human admin. It is intentionally
// not a UUID, so accepted_by and the ledger actor are left NULL.
const AutoApproveActor = "system:auto-approve"

// AcceptWager approves a pending wager, moving its stake from the user's
// funding account to the shared escrow account. Idempotency is guaranteed by
// locking the wager row FOR UPDATE first: if it is already accepted, the
// existing acceptance ledger transaction is verified and returned unchanged
// rather than posting a second debit. Insufficient funds is detected after
// locking the user's funding account row, which also serializes concurrent
// acceptances against the same account so they cannot overspend it.
func (s Store) AcceptWager(ctx context.Context, wagerID, actorUserID string) (betting.Wager, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return betting.Wager{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	wager, err := loadWagerForUpdate(ctx, tx, wagerID)
	if err != nil {
		return betting.Wager{}, err
	}

	if wager.State == betting.WagerAccepted {
		if err := verifyAcceptance(ctx, tx, wager); err != nil {
			return betting.Wager{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return betting.Wager{}, fmt.Errorf("commit accept wager (idempotent): %w", err)
		}
		return wager, nil
	}

	userAccountID, err := ensureUserAccount(ctx, tx, string(wager.UserID), wager.FundingAccountType, wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, err
	}
	escrowAccountID, err := ensureSystemAccount(ctx, tx, "wager_escrow", wager.Stake.Currency)
	if err != nil {
		return betting.Wager{}, err
	}
	if _, err := ensureSystemAccount(ctx, tx, "house_clearing", wager.Stake.Currency); err != nil {
		return betting.Wager{}, err
	}

	// Lock the user's funding account row. Any other transaction that also
	// tries to accept a wager funded from this same account must wait here
	// until this transaction commits or rolls back, so two concurrent
	// acceptances can never both see a balance sufficient for the stake.
	if err := lockAccount(ctx, tx, userAccountID); err != nil {
		return betting.Wager{}, err
	}
	balance, err := accountBalance(ctx, tx, userAccountID)
	if err != nil {
		return betting.Wager{}, err
	}
	if balance < wager.Stake.Cents {
		return betting.Wager{}, ErrInsufficientFunds
	}

	// The line the wager's selection is offered at right now. If it no longer
	// matches the odds snapshotted when the wager was placed, the line moved
	// while the wager was pending; the domain refuses acceptance and the stale
	// wager is invalidated (rejected) rather than filled at a stale price.
	currentOdds, err := currentSelectionOdds(ctx, tx, string(wager.SelectionID))
	if err != nil {
		return betting.Wager{}, err
	}

	eventID, err := betting.NewEventID()
	if err != nil {
		return betting.Wager{}, err
	}
	result, err := betting.AcceptWager(wager, betting.ID(actorUserID), time.Now(), betting.AcceptanceAccountRefs{
		UserFundingAccountID: userAccountID,
		EscrowAccountID:      escrowAccountID,
	}, currentOdds, eventID)
	if errors.Is(err, betting.ErrOddsMoved) {
		return s.invalidateStaleWager(ctx, tx, wager, actorUserID)
	}
	if err != nil {
		return betting.Wager{}, err
	}

	transactionID, err := insertLedgerTransaction(ctx, tx, result.Transaction)
	if err != nil {
		return betting.Wager{}, err
	}

	acceptedBy := actorUserID
	if !isUUID(acceptedBy) {
		acceptedBy = "" // a system actor (auto-approve) leaves accepted_by NULL
	}
	tag, err := tx.Exec(ctx, `
		UPDATE wagers
		SET state = 'accepted', accepted_at = $2, accepted_by = nullif($3, '')::uuid, acceptance_ledger_transaction_id = $4::uuid
		WHERE id = $1::uuid AND state = 'pending'`,
		wagerID, result.Wager.PlacedAt, acceptedBy, transactionID)
	if err != nil {
		return betting.Wager{}, fmt.Errorf("update wager to accepted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return betting.Wager{}, fmt.Errorf("update wager %s to accepted: wager was not pending", wagerID)
	}

	if err := eventspg.Publish(ctx, tx, result.Event, maxOutboxAttempts); err != nil {
		return betting.Wager{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, fmt.Errorf("commit accept wager: %w", err)
	}
	return result.Wager, nil
}

func currentSelectionOdds(ctx context.Context, tx pgx.Tx, selectionID string) (ledger.AmericanOdds, error) {
	var odds int32
	if err := tx.QueryRow(ctx, `SELECT offered_american_odds FROM selections WHERE id = $1::uuid`, selectionID).Scan(&odds); err != nil {
		return 0, fmt.Errorf("load current selection odds: %w", err)
	}
	return ledger.NewAmericanOdds(odds)
}

// invalidateStaleWager rejects a pending wager whose quoted line has moved,
// committing the rejection and returning ErrOddsMoved so the caller can tell
// the bettor to re-place at the current line. A system (auto-approve) actor
// cannot be recorded as the rejecter, but auto-approval accepts immediately at
// placement so the line cannot have moved in that path.
func (s Store) invalidateStaleWager(ctx context.Context, tx pgx.Tx, wager betting.Wager, actorUserID string) (betting.Wager, error) {
	if !isUUID(actorUserID) {
		return betting.Wager{}, betting.ErrOddsMoved
	}
	const reason = "line moved since placement"
	if _, err := tx.Exec(ctx, `
		UPDATE wagers SET state = 'rejected', rejected_at = now(), rejected_by = $2::uuid, rejection_reason = $3
		WHERE id = $1::uuid AND state = 'pending'`, string(wager.ID), actorUserID, reason); err != nil {
		return betting.Wager{}, fmt.Errorf("invalidate stale wager: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, fmt.Errorf("commit stale wager rejection: %w", err)
	}
	wager.State = betting.WagerRejected
	return wager, betting.ErrOddsMoved
}

// verifyAcceptance checks that an already-accepted wager's acceptance ledger
// transaction is present and balanced as expected for its stake, so a
// repeated AcceptWager call is provably a no-op rather than a silent
// pass-through.
func verifyAcceptance(ctx context.Context, tx pgx.Tx, wager betting.Wager) error {
	idempotencyKey := fmt.Sprintf("wager:%s:acceptance", wager.ID)
	var count int
	var userAmount, escrowAmount int64
	err := tx.QueryRow(ctx, `
		SELECT count(*),
		       coalesce(sum(p.amount_cents) FILTER (WHERE p.amount_cents < 0), 0),
		       coalesce(sum(p.amount_cents) FILTER (WHERE p.amount_cents > 0), 0)
		FROM ledger_transactions t
		JOIN ledger_postings p ON p.transaction_id = t.id
		WHERE t.currency = $1 AND t.idempotency_key = $2`,
		string(wager.Stake.Currency), idempotencyKey).Scan(&count, &userAmount, &escrowAmount)
	if err != nil {
		return fmt.Errorf("verify wager acceptance transaction: %w", err)
	}
	if count != 2 || -userAmount != wager.Stake.Cents || escrowAmount != wager.Stake.Cents {
		return fmt.Errorf("%w: accepted wager %s acceptance transaction does not match its stake", ErrIdempotencyConflict, wager.ID)
	}
	return nil
}

// RejectWager moves a pending wager to rejected. No funds ever moved, so no
// ledger writes happen here; a repeated reject of an already-rejected wager
// is a no-op as long as the reason matches.
func (s Store) RejectWager(ctx context.Context, wagerID, actorUserID, reason string) (betting.Wager, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return betting.Wager{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	wager, err := loadWagerForUpdate(ctx, tx, wagerID)
	if err != nil {
		return betting.Wager{}, err
	}

	if wager.State == betting.WagerRejected {
		var storedReason string
		if err := tx.QueryRow(ctx, `SELECT coalesce(rejection_reason, '') FROM wagers WHERE id = $1::uuid`, wagerID).Scan(&storedReason); err != nil {
			return betting.Wager{}, fmt.Errorf("load existing rejection: %w", err)
		}
		if strings.TrimSpace(storedReason) != strings.TrimSpace(reason) {
			return betting.Wager{}, fmt.Errorf("%w: wager %s rejection reason does not match", ErrIdempotencyConflict, wagerID)
		}
		if err := tx.Commit(ctx); err != nil {
			return betting.Wager{}, fmt.Errorf("commit reject wager (idempotent): %w", err)
		}
		return wager, nil
	}

	rejected, err := betting.RejectWager(wager, betting.ID(actorUserID), reason)
	if err != nil {
		return betting.Wager{}, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wagers
		SET state = 'rejected', rejected_at = now(), rejected_by = $2::uuid, rejection_reason = $3
		WHERE id = $1::uuid AND state = 'pending'`,
		wagerID, actorUserID, strings.TrimSpace(reason))
	if err != nil {
		return betting.Wager{}, fmt.Errorf("update wager to rejected: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return betting.Wager{}, fmt.Errorf("update wager %s to rejected: wager was not pending", wagerID)
	}

	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, fmt.Errorf("commit reject wager: %w", err)
	}
	return rejected, nil
}
