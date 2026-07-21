package bettingpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/pricing"
	"github.com/jackc/pgx/v5"
)

// RepriceMarketAfterWager moves an open, dynamically-priced market's line to
// reflect the accepted stake now on each selection, tilting toward the
// heavily-backed side while preserving the house margin. It is a no-op when
// the market has dynamic pricing disabled, is not open, or has fewer than two
// selections. It reports whether any selection's offered line changed.
//
// The new line is always recomputed from each selection's stable opening odds
// and the current total accepted stake, never from the previous offered line,
// so the operation is idempotent: re-running it for the same state produces no
// further change and writes no duplicate audit rows. Accepted wagers are never
// touched — their odds were snapshotted at placement — so only future bets see
// the new line.
func (s Store) RepriceMarketAfterWager(ctx context.Context, marketID, triggerWagerID string) (bool, error) {
	if !isUUID(marketID) {
		return false, fmt.Errorf("%w: reprice requires a market ID", betting.ErrInvalid)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var dynamicPricing bool
	var liquidityCents int64
	var state string
	err = tx.QueryRow(ctx, `
		SELECT dynamic_pricing, coalesce(pricing_liquidity_cents, 0), state
		FROM markets WHERE id = $1::uuid FOR UPDATE`, marketID).Scan(&dynamicPricing, &liquidityCents, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	if err != nil {
		return false, fmt.Errorf("load market pricing config %s: %w", marketID, err)
	}
	if !dynamicPricing || state != string(betting.MarketOpen) {
		return false, nil
	}

	selectionIDs, openingOdds, offeredOdds, err := lockSelectionsForPricing(ctx, tx, marketID)
	if err != nil {
		return false, err
	}
	if len(selectionIDs) < 2 {
		return false, nil
	}
	stakes, err := acceptedStakeBySelection(ctx, tx, marketID)
	if err != nil {
		return false, err
	}

	inputs := make([]pricing.SelectionInput, len(selectionIDs))
	for i, id := range selectionIDs {
		inputs[i] = pricing.SelectionInput{OpeningOdds: openingOdds[i], StakeCents: stakes[id]}
	}
	repriced, err := pricing.Reprice(inputs, liquidityCents)
	if err != nil {
		return false, fmt.Errorf("reprice market %s: %w", marketID, err)
	}

	changed := false
	for i, id := range selectionIDs {
		newOdds := repriced[i].Odds
		if newOdds == offeredOdds[i] {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE selections SET offered_american_odds = $2, updated_at = now() WHERE id = $1::uuid`,
			id, int32(newOdds)); err != nil {
			return false, fmt.Errorf("update selection %s line: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO selection_price_changes
			(market_id, selection_id, trigger_wager_id, old_american_odds, new_american_odds, exposure_cents)
			VALUES ($1::uuid, $2::uuid, nullif($3, '')::uuid, $4, $5, $6)`,
			marketID, id, triggerWagerID, int32(offeredOdds[i]), int32(newOdds), stakes[id]); err != nil {
			return false, fmt.Errorf("record price change for selection %s: %w", id, err)
		}
		changed = true
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reprice for market %s: %w", marketID, err)
	}
	return changed, nil
}

func lockSelectionsForPricing(ctx context.Context, tx pgx.Tx, marketID string) (ids []string, opening, offered []ledger.AmericanOdds, err error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, opening_american_odds, offered_american_odds
		FROM selections WHERE market_id = $1::uuid ORDER BY id FOR UPDATE`, marketID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lock selections for market %s: %w", marketID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var openingOdds, offeredOdds int32
		if err := rows.Scan(&id, &openingOdds, &offeredOdds); err != nil {
			return nil, nil, nil, fmt.Errorf("scan selection for pricing: %w", err)
		}
		openingValidated, err := ledger.NewAmericanOdds(openingOdds)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("selection %s opening odds: %w", id, err)
		}
		offeredValidated, err := ledger.NewAmericanOdds(offeredOdds)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("selection %s offered odds: %w", id, err)
		}
		ids = append(ids, id)
		opening = append(opening, openingValidated)
		offered = append(offered, offeredValidated)
	}
	return ids, opening, offered, rows.Err()
}

func acceptedStakeBySelection(ctx context.Context, tx pgx.Tx, marketID string) (map[string]int64, error) {
	rows, err := tx.Query(ctx, `
		SELECT selection_id::text, coalesce(sum(stake_cents), 0)
		FROM wagers WHERE market_id = $1::uuid AND state = 'accepted'
		GROUP BY selection_id`, marketID)
	if err != nil {
		return nil, fmt.Errorf("sum accepted stake for market %s: %w", marketID, err)
	}
	defer rows.Close()
	stakes := make(map[string]int64)
	for rows.Next() {
		var selectionID string
		var stake int64
		if err := rows.Scan(&selectionID, &stake); err != nil {
			return nil, fmt.Errorf("scan accepted stake: %w", err)
		}
		stakes[selectionID] = stake
	}
	return stakes, rows.Err()
}

// PricingConsumer moves a market's line whenever one of its wagers is accepted.
// It implements events.Consumer for WagerAccepted. Handling derives the new
// line from current state, so redelivery is safe.
type PricingConsumer struct {
	Store  *Store
	Logger *slog.Logger
}

func (c *PricingConsumer) Name() string { return "betting-dynamic-pricing" }

func (c *PricingConsumer) Handles(t events.Type) bool { return t == events.WagerAccepted }

func (c *PricingConsumer) Handle(ctx context.Context, envelope events.Envelope) error {
	if c.Store == nil {
		return errors.New("betting-dynamic-pricing: store is required")
	}
	var payload struct {
		WagerID  string `json:"wager_id"`
		MarketID string `json:"market_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("parse WagerAccepted payload: %w", err)
	}
	changed, err := c.Store.RepriceMarketAfterWager(ctx, payload.MarketID, payload.WagerID)
	if err != nil {
		return err
	}
	if changed {
		c.logger().Info("betting market line moved on accepted action", "market_id", payload.MarketID)
	}
	return nil
}

func (c *PricingConsumer) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
