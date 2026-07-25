package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/pricing"
	"github.com/jackc/pgx/v5"
)

// ErrMarketNotPriceable is returned when a market's line cannot be moved by
// hand because it is no longer taking action.
var ErrMarketNotPriceable = errors.New("bettingpg: only a draft or open market's line can be moved")

// SetOpeningLine moves a selection's line by hand. It sets the selection's
// opening odds — the stable prior the pricing engine works from — rather than
// the offered price, because an offered price would be recomputed away by the
// next accepted wager. The rest of the board is then repriced from the new
// prior and the action already on it, so a hand-set line and the automatic
// engine can never disagree.
//
// Accepted wagers keep the odds they were filled at; this only changes what
// the next bettor is shown.
func (s Store) SetOpeningLine(ctx context.Context, marketID, selectionID string, odds ledger.AmericanOdds, actorUserID, reason string) (bool, error) {
	if !isUUID(marketID) || !isUUID(selectionID) {
		return false, fmt.Errorf("%w: setting a line requires a market and selection", betting.ErrInvalid)
	}
	if !isUUID(actorUserID) {
		return false, betting.ErrUnauthorized
	}
	if err := odds.Validate(); err != nil {
		return false, err
	}
	// Trim before validating, or a reason of spaces passes here and is only
	// caught by the database's own check on the audit row.
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false, betting.ErrReasonRequired
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
	if state != string(betting.MarketOpen) && state != string(betting.MarketDraft) {
		return false, fmt.Errorf("%w: market %s is %s", ErrMarketNotPriceable, marketID, state)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE selections SET opening_american_odds = $3, updated_at = now()
		WHERE id = $1::uuid AND market_id = $2::uuid`, selectionID, marketID, int32(odds))
	if err != nil {
		return false, fmt.Errorf("set opening line for selection %s: %w", selectionID, err)
	}
	if tag.RowsAffected() == 0 {
		return false, fmt.Errorf("%w: selection %s", betting.ErrNotFound, selectionID)
	}

	changed, err := repriceInTx(ctx, tx, marketID, priceMove{
		DynamicPricing: dynamicPricing,
		LiquidityCents: liquidityCents,
		ActorUserID:    actorUserID,
		Reason:         reason,
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit hand-set line for market %s: %w", marketID, err)
	}
	return changed, nil
}

// priceMove carries what a repricing pass needs to know about why it is
// running: the market's pricing configuration, and, for a hand-set line, who
// asked for it and why. Automatic moves leave the actor and reason empty and
// name the wager that triggered them instead.
type priceMove struct {
	DynamicPricing bool
	LiquidityCents int64
	TriggerWagerID string
	ActorUserID    string
	Reason         string
}

// repriceInTx recomputes every selection's offered line from its opening odds
// and the accepted stake on it, writing an audit row for each price that
// actually moves. It is the single place offered odds are written, so the
// automatic engine and a hand-set line always agree on the arithmetic.
//
// A market without dynamic pricing still needs its offered line to follow a
// hand-set opening line, so it is written straight across in that case.
func repriceInTx(ctx context.Context, tx pgx.Tx, marketID string, move priceMove) (bool, error) {
	selectionIDs, openingOdds, offeredOdds, err := lockSelectionsForPricing(ctx, tx, marketID)
	if err != nil {
		return false, err
	}
	if len(selectionIDs) == 0 {
		return false, nil
	}
	stakes, err := acceptedStakeBySelection(ctx, tx, marketID)
	if err != nil {
		return false, err
	}

	target := make([]ledger.AmericanOdds, len(selectionIDs))
	if move.DynamicPricing && len(selectionIDs) >= 2 && move.LiquidityCents > 0 {
		inputs := make([]pricing.SelectionInput, len(selectionIDs))
		for i, id := range selectionIDs {
			inputs[i] = pricing.SelectionInput{OpeningOdds: openingOdds[i], StakeCents: stakes[id]}
		}
		repriced, err := pricing.Reprice(inputs, move.LiquidityCents)
		if err != nil {
			return false, fmt.Errorf("reprice market %s: %w", marketID, err)
		}
		for i := range repriced {
			target[i] = repriced[i].Odds
		}
	} else {
		// Fixed-price market: the offered line is simply the opening line.
		copy(target, openingOdds)
	}

	changed := false
	for i, id := range selectionIDs {
		if target[i] == offeredOdds[i] {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE selections SET offered_american_odds = $2, updated_at = now() WHERE id = $1::uuid`,
			id, int32(target[i])); err != nil {
			return false, fmt.Errorf("update selection %s line: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO selection_price_changes
			(market_id, selection_id, trigger_wager_id, old_american_odds, new_american_odds, exposure_cents,
			 actor_user_id, reason)
			VALUES ($1::uuid, $2::uuid, nullif($3, '')::uuid, $4, $5, $6, nullif($7, '')::uuid, nullif($8, ''))`,
			marketID, id, move.TriggerWagerID, int32(offeredOdds[i]), int32(target[i]), stakes[id],
			move.ActorUserID, move.Reason); err != nil {
			return false, fmt.Errorf("record price change for selection %s: %w", id, err)
		}
		changed = true
	}
	return changed, nil
}
