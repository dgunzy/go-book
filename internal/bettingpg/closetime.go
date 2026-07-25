package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5"
)

// ErrCloseTimeInPast is returned when a market is asked to close at a moment
// that has already passed. Closing it now is what the Close control is for;
// backdating would hide the change from anyone reading the board.
var ErrCloseTimeInPast = errors.New("bettingpg: a market's closing time must be in the future")

// SetMarketCloseTime moves when a market stops taking action. Wagers already
// on it are untouched — they keep their odds and stand or fall on the result —
// so this only changes how long the board stays open to new money.
//
// Only a draft or open market can be moved: once closed, action has already
// stopped and reopening it by moving the time would let money in against a
// result people may already know.
func (s Store) SetMarketCloseTime(ctx context.Context, marketID string, closesAt time.Time, actorUserID, reason string) error {
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
	if closesAt.IsZero() {
		return fmt.Errorf("%w: a closing time is required", betting.ErrInvalid)
	}
	if !closesAt.After(time.Now()) {
		return ErrCloseTimeInPast
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var state string
	var opensAt *time.Time
	var previous time.Time
	err = tx.QueryRow(ctx, `
		SELECT state, opens_at, closes_at FROM markets WHERE id = $1::uuid FOR UPDATE`,
		marketID).Scan(&state, &opensAt, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	if err != nil {
		return fmt.Errorf("load market for close time: %w", err)
	}
	if state != string(betting.MarketOpen) && state != string(betting.MarketDraft) {
		return fmt.Errorf("%w: market %s is %s", ErrMarketNotPriceable, marketID, state)
	}
	if opensAt != nil && !closesAt.After(*opensAt) {
		return fmt.Errorf("%w: a market cannot close before it opens", betting.ErrInvalid)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE markets SET closes_at = $2, updated_at = now() WHERE id = $1::uuid`,
		marketID, closesAt.UTC()); err != nil {
		return fmt.Errorf("update market close time: %w", err)
	}
	// The old and new times both go on the audit trail: "why is this still
	// open?" needs an answer that does not depend on memory.
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'market.close_time_changed', 'market', $2::uuid, $3,
		        jsonb_build_object('closes_at', $4::timestamptz),
		        jsonb_build_object('closes_at', $5::timestamptz))`,
		actorUserID, marketID, reason, previous.UTC(), closesAt.UTC()); err != nil {
		return fmt.Errorf("record close time change: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit market close time: %w", err)
	}
	return nil
}
