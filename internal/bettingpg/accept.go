package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/jackc/pgx/v5"
)

// AutoApproveActor is the actor recorded on wagers accepted automatically by
// the auto-approve policy rather than by a human admin. It is intentionally
// not a UUID, so accepted_by and the ledger actor are left NULL.
const AutoApproveActor = "system:auto-approve"

// AutoApproveLimitForUser returns a user's per-player auto-approve override in
// cents and whether one is set. When no override exists the caller falls back
// to the book-wide default.
func (s Store) AutoApproveLimitForUser(ctx context.Context, userID string) (int64, bool, error) {
	if s.DB == nil {
		return 0, false, errors.New("bettingpg: PostgreSQL pool is required")
	}
	if !isUUID(userID) {
		return 0, false, fmt.Errorf("%w: auto-approve limit requires a user ID", betting.ErrInvalid)
	}
	rows, err := s.DB.Query(ctx, `SELECT wager_auto_approve_max_cents FROM users WHERE id = $1::uuid`, userID)
	if err != nil {
		return 0, false, fmt.Errorf("load auto-approve limit: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, false, err
		}
		return 0, false, fmt.Errorf("%w: user %s", betting.ErrNotFound, userID)
	}
	var limit *int64
	if err := rows.Scan(&limit); err != nil {
		return 0, false, fmt.Errorf("scan auto-approve limit: %w", err)
	}
	if limit == nil {
		return 0, false, rows.Err()
	}
	return *limit, true, rows.Err()
}

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

	// A market that has already been graded or voided will never grade this
	// wager, so accepting one would take a member's stake into escrow with no
	// path back out. The market row is read without a lock on purpose: settle
	// locks the market first and the wagers second, so taking the market lock
	// here would invert that order. Holding this wager's row lock is enough —
	// an in-flight settlement is blocked on it and will grade this wager once
	// we commit, and a settlement that already committed is visible here.
	marketState, err := marketStateForWager(ctx, tx, string(wager.MarketID))
	if err != nil {
		return betting.Wager{}, err
	}
	switch marketState {
	case betting.MarketSettled, betting.MarketVoided, betting.MarketCancelled:
		return betting.Wager{}, fmt.Errorf("%w: market is already %s", ErrMarketDecided, marketState)
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
	// A member may bet on credit: their cash balance may go negative down to
	// their credit limit, so the available amount is balance + credit limit.
	creditLimit, err := creditLimitForUser(ctx, tx, string(wager.UserID))
	if err != nil {
		return betting.Wager{}, err
	}
	if balance+creditLimit < wager.Stake.Cents {
		return betting.Wager{}, ErrInsufficientFunds
	}

	// A wager fills at the odds it was placed at even when dynamic pricing has
	// since moved the line. The admin review queue shows the live line next to
	// the locked price, so taking or refusing a stale price is a decision made
	// there, not silently here.
	eventID, err := betting.NewEventID()
	if err != nil {
		return betting.Wager{}, err
	}
	// One acceptance instant, used for the ledger, the event envelope, and the
	// wagers row alike, so the audit trail agrees with itself.
	acceptedAt := time.Now().UTC()
	result, err := betting.AcceptWager(wager, betting.ID(actorUserID), acceptedAt, betting.AcceptanceAccountRefs{
		UserFundingAccountID: userAccountID,
		EscrowAccountID:      escrowAccountID,
	}, eventID)
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
		wagerID, acceptedAt, acceptedBy, transactionID)
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

func creditLimitForUser(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	var limit int64
	if err := tx.QueryRow(ctx, `SELECT credit_limit_cents FROM users WHERE id = $1::uuid`, userID).Scan(&limit); err != nil {
		return 0, fmt.Errorf("load credit limit: %w", err)
	}
	return limit, nil
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
	// A reduced wager's stake is deliberately smaller than what acceptance
	// moved: the difference went back to the member as a partial refund. The
	// acceptance transaction itself is never rewritten, so the check is that
	// escrow still holds acceptance less every reduction since.
	refunded, err := reducedCents(ctx, tx, wager)
	if err != nil {
		return err
	}
	held := wager.Stake.Cents + refunded
	if count != 2 || -userAmount != held || escrowAmount != held {
		return fmt.Errorf("%w: accepted wager %s acceptance transaction does not match its stake", ErrIdempotencyConflict, wager.ID)
	}
	return nil
}

// reducedCents totals what has been handed back to the member by stake
// reductions on this wager.
func reducedCents(ctx context.Context, tx pgx.Tx, wager betting.Wager) (int64, error) {
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(p.amount_cents) FILTER (WHERE p.amount_cents > 0), 0)
		FROM ledger_transactions t
		JOIN ledger_postings p ON p.transaction_id = t.id
		WHERE t.currency = $1 AND t.idempotency_key LIKE $2`,
		string(wager.Stake.Currency), fmt.Sprintf("wager:%s:reduce:%%", wager.ID)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("total wager reductions: %w", err)
	}
	return total, nil
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

// CancelWager lets a member withdraw their own still-pending wager. The wager
// row is locked first so a cancellation racing the book's acceptance cannot
// both win: whichever transaction commits first leaves the other looking at a
// wager that is no longer pending. A wager belonging to another member reads
// as not found, so this route cannot be used to probe for other people's bets.
func (s Store) CancelWager(ctx context.Context, wagerID, userID string) (betting.Wager, error) {
	if !isUUID(userID) {
		return betting.Wager{}, fmt.Errorf("%w: cancelling a wager requires a user ID", betting.ErrInvalid)
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
	if string(wager.UserID) != userID {
		return betting.Wager{}, fmt.Errorf("%w: wager %s", betting.ErrNotFound, wagerID)
	}

	// Cancelling an already-cancelled wager is a no-op, so a double-submit or
	// a retried request does not turn into an error the member has to read.
	if wager.State == betting.WagerRejected {
		var storedReason string
		if err := tx.QueryRow(ctx, `SELECT coalesce(rejection_reason, '') FROM wagers WHERE id = $1::uuid`, wagerID).Scan(&storedReason); err != nil {
			return betting.Wager{}, fmt.Errorf("load existing rejection: %w", err)
		}
		if strings.TrimSpace(storedReason) != betting.CancelWagerReason {
			return betting.Wager{}, fmt.Errorf("%w: wager %s was rejected by the book, not cancelled", ErrIdempotencyConflict, wagerID)
		}
		if err := tx.Commit(ctx); err != nil {
			return betting.Wager{}, fmt.Errorf("commit cancel wager (idempotent): %w", err)
		}
		return wager, nil
	}

	cancelled, err := betting.CancelWager(wager, betting.ID(userID))
	if err != nil {
		return betting.Wager{}, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wagers
		SET state = 'rejected', rejected_at = now(), rejected_by = $2::uuid, rejection_reason = $3
		WHERE id = $1::uuid AND user_id = $2::uuid AND state = 'pending'`,
		wagerID, userID, betting.CancelWagerReason)
	if err != nil {
		return betting.Wager{}, fmt.Errorf("update wager to cancelled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return betting.Wager{}, fmt.Errorf("%w: wager %s is no longer pending", betting.ErrInvalidTransition, wagerID)
	}

	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, fmt.Errorf("commit cancel wager: %w", err)
	}
	return cancelled, nil
}
