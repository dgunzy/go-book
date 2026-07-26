package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5"
)

// SetStakeLimit changes how much one member may have riding on a market, or on
// one side of it. An empty selectionID sets the market-wide cap; naming a
// selection caps that side alone, which is what a lopsided prop wants: a tight
// limit on the long price and room on the short one.
//
// Passing zero clears the cap. Wagers already accepted are never disturbed —
// lowering a limit below what someone already has on simply stops them adding
// more.
func (s Store) SetStakeLimit(ctx context.Context, marketID, selectionID string, cents int64, actorUserID, reason string) error {
	if !isUUID(marketID) {
		return fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	if selectionID != "" && !isUUID(selectionID) {
		return fmt.Errorf("%w: selection %s", betting.ErrNotFound, selectionID)
	}
	if !isUUID(actorUserID) {
		return betting.ErrUnauthorized
	}
	if cents < 0 {
		return fmt.Errorf("%w: a limit cannot be negative", betting.ErrInvalid)
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
		return fmt.Errorf("load market for stake limit: %w", err)
	}
	if state != string(betting.MarketOpen) && state != string(betting.MarketDraft) {
		return fmt.Errorf("%w: market %s is %s", ErrMarketNotPriceable, marketID, state)
	}

	var previous *int64
	var tag pgconnTag
	if selectionID == "" {
		if err := tx.QueryRow(ctx, `SELECT max_stake_cents FROM markets WHERE id = $1::uuid`, marketID).Scan(&previous); err != nil {
			return fmt.Errorf("load market stake limit: %w", err)
		}
		tag, err = tx.Exec(ctx, `
			UPDATE markets SET max_stake_cents = nullif($2, 0::bigint), updated_at = now()
			WHERE id = $1::uuid`, marketID, cents)
	} else {
		if err := tx.QueryRow(ctx, `
			SELECT max_stake_cents FROM selections WHERE id = $1::uuid AND market_id = $2::uuid`,
			selectionID, marketID).Scan(&previous); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: selection %s is not on market %s", betting.ErrNotFound, selectionID, marketID)
			}
			return fmt.Errorf("load selection stake limit: %w", err)
		}
		tag, err = tx.Exec(ctx, `
			UPDATE selections SET max_stake_cents = nullif($3, 0::bigint), updated_at = now()
			WHERE id = $1::uuid AND market_id = $2::uuid`, selectionID, marketID, cents)
	}
	if err != nil {
		return fmt.Errorf("update stake limit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: nothing to limit", betting.ErrNotFound)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'market.stake_limit_changed', 'market', $2::uuid, $3,
		        jsonb_build_object('selection_id', nullif($4, ''), 'max_stake_cents', $5::bigint),
		        jsonb_build_object('selection_id', nullif($4, ''), 'max_stake_cents', nullif($6, 0::bigint)))`,
		actorUserID, marketID, reason, selectionID, previousOrZero(previous), cents); err != nil {
		return fmt.Errorf("record stake limit change: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit stake limit: %w", err)
	}
	return nil
}

func previousOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
