package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5"
)

// ErrMarketHasWagers is returned when a market is closed out as having no
// action but somebody has money on it. Grading it properly is the only correct
// outcome then.
var ErrMarketHasWagers = errors.New("bettingpg: market has wagers and must be graded or voided")

// ErrMatchMarketNeedsGrading is returned when a match market is closed out as
// having no action. Match markets always resolve through match settlement, so
// letting one skip grading would leave the competition record and the book
// disagreeing about whether the match happened.
var ErrMatchMarketNeedsGrading = errors.New("bettingpg: a match market always grades through match settlement")

// ErrMarketChangedConcurrently is returned when the market moved out from under
// the close between the lock being taken and the update landing.
var ErrMarketChangedConcurrently = errors.New("bettingpg: market changed while closing")

// CloseMarketWithoutAction retires a market that closed with nobody on it.
//
// No settlement row is written and no ledger entry is made, because no money
// was ever at risk: a settlement would claim otherwise and reconciliation reads
// those rows. The audit entry is the record that this happened.
//
// The zero-wager check runs inside the same transaction as the state change and
// behind the market's row lock, so a wager landing concurrently cannot slip in
// between the check and the update.
func (s Store) CloseMarketWithoutAction(ctx context.Context, marketID, actorUserID, reason string) error {
	if !isUUID(marketID) {
		return fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	if !isUUID(actorUserID) {
		return betting.ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return betting.ErrReasonRequired
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var state, marketType string
	if err := tx.QueryRow(ctx, `
		SELECT state, market_type FROM markets WHERE id = $1::uuid FOR UPDATE`,
		marketID).Scan(&state, &marketType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
		}
		return fmt.Errorf("load market for no-action close: %w", err)
	}
	if marketType == "match" {
		return ErrMatchMarketNeedsGrading
	}
	if !betting.MarketState(state).CanTransitionTo(betting.MarketNoAction) {
		return fmt.Errorf("%w: market %s is %s", ErrMarketNotSettleable, marketID, state)
	}

	// Cancelled and rejected wagers never held money and voided ones have
	// already been unwound, so none of them stops a no-action close. Anything
	// pending or accepted does.
	var live int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM wagers
		WHERE market_id = $1::uuid AND state IN ('pending', 'accepted')`,
		marketID).Scan(&live); err != nil {
		return fmt.Errorf("count live wagers: %w", err)
	}
	if live > 0 {
		return fmt.Errorf("%w: %d live wager(s)", ErrMarketHasWagers, live)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE markets SET state = 'closed_no_action', updated_at = now()
		WHERE id = $1::uuid AND state = $2`, marketID, state)
	if err != nil {
		return fmt.Errorf("close market without action: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: market %s", ErrMarketChangedConcurrently, marketID)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'market.closed_no_action', 'market', $2::uuid, $3,
		        jsonb_build_object('state', $4::text),
		        jsonb_build_object('state', 'closed_no_action'))`,
		actorUserID, marketID, reason, state); err != nil {
		return fmt.Errorf("record no-action close: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit no-action close: %w", err)
	}
	return nil
}

// SetTotalSideCap sets the ceiling on what the whole book will take on one side
// of a market. Zero clears it.
//
// It is deliberately separate from SetStakeLimit: that caps one member, this
// caps everybody together, and conflating them in one control is how an admin
// ends up setting the wrong one. Wagers already on are never disturbed —
// lowering a cap below what the side already holds simply stops it growing.
func (s Store) SetTotalSideCap(ctx context.Context, marketID, selectionID string, cents int64, actorUserID, reason string) error {
	if !isUUID(marketID) {
		return fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	if !isUUID(selectionID) {
		return fmt.Errorf("%w: selection %s", betting.ErrNotFound, selectionID)
	}
	if !isUUID(actorUserID) {
		return betting.ErrUnauthorized
	}
	if cents < 0 {
		return fmt.Errorf("%w: a cap cannot be negative", betting.ErrInvalid)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return betting.ErrReasonRequired
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM markets WHERE id = $1::uuid FOR UPDATE`, marketID).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
		}
		return fmt.Errorf("load market for side cap: %w", err)
	}
	if state != string(betting.MarketOpen) && state != string(betting.MarketDraft) {
		return fmt.Errorf("%w: market %s is %s", ErrMarketNotPriceable, marketID, state)
	}

	var previous *int64
	if err := tx.QueryRow(ctx, `
		SELECT total_stake_cap_cents FROM selections WHERE id = $1::uuid AND market_id = $2::uuid`,
		selectionID, marketID).Scan(&previous); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: selection %s is not on market %s", betting.ErrNotFound, selectionID, marketID)
		}
		return fmt.Errorf("load side cap: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE selections SET total_stake_cap_cents = nullif($3, 0::bigint), updated_at = now()
		WHERE id = $1::uuid AND market_id = $2::uuid`, selectionID, marketID, cents)
	if err != nil {
		return fmt.Errorf("update side cap: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: nothing to cap", betting.ErrNotFound)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'market.total_side_cap_changed', 'market', $2::uuid, $3,
		        jsonb_build_object('selection_id', $4::uuid, 'total_stake_cap_cents', $5::bigint),
		        jsonb_build_object('selection_id', $4::uuid, 'total_stake_cap_cents', nullif($6, 0::bigint)))`,
		actorUserID, marketID, reason, selectionID, previousOrZero(previous), cents); err != nil {
		return fmt.Errorf("record side cap change: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit side cap: %w", err)
	}
	return nil
}
