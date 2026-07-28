package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
)

// ErrNothingToRegrade is returned when a settled market has no wager left
// behind, so there is nothing for a regrade to do.
var ErrNothingToRegrade = errors.New("bettingpg: this market has no ungraded wagers")

// StrandedWagerRow is one wager that a market's settlement never graded,
// because it was accepted after the market was already settled.
type StrandedWagerRow struct {
	WagerID    string
	MemberName string
	Selection  string
	StakeCents int64
	Result     betting.SettlementResult
}

// StrandedWagers lists the wagers on a settled market that are still in
// accepted state, together with the result the market's own settlement
// recorded for the selection each one backed. It is read-only.
func (s Store) StrandedWagers(ctx context.Context, marketID string) ([]StrandedWagerRow, error) {
	if !isUUID(marketID) {
		return nil, fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	rows, err := s.DB.Query(ctx, `
		SELECT w.id::text, coalesce(u.display_name, ''), s.display_terms, w.stake_cents, o.outcome
		FROM wagers w
		JOIN selections s ON s.id = w.selection_id
		LEFT JOIN users u ON u.id = w.user_id
		JOIN markets m ON m.id = w.market_id
		JOIN market_settlement_outcomes o
		  ON o.selection_id = w.selection_id
		 AND o.market_settlement_id = (
			SELECT ms.id FROM market_settlements ms
			WHERE ms.market_id = w.market_id AND ms.settlement_type = 'graded'
			ORDER BY ms.version DESC LIMIT 1)
		WHERE w.market_id = $1::uuid AND w.state = 'accepted' AND m.state = 'settled'
		ORDER BY w.placed_at`, marketID)
	if err != nil {
		return nil, fmt.Errorf("load stranded wagers for market %s: %w", marketID, err)
	}
	defer rows.Close()
	var out []StrandedWagerRow
	for rows.Next() {
		var row StrandedWagerRow
		var result string
		if err := rows.Scan(&row.WagerID, &row.MemberName, &row.Selection, &row.StakeCents, &result); err != nil {
			return nil, fmt.Errorf("scan stranded wager: %w", err)
		}
		row.Result = betting.SettlementResult(result)
		out = append(out, row)
	}
	return out, rows.Err()
}

// RegradeStrandedWagers grades the wagers a settled market left behind, using
// the outcome that market's own settlement already recorded.
//
// This exists because a wager could be accepted after its market was graded
// (see AcceptWager, which now refuses that), leaving a member's stake in
// escrow with no settlement to resolve it. It is deliberately narrow:
//
//   - The outcome is read from market_settlement_outcomes, never supplied by
//     the caller, so a regrade cannot flip a market's result and pay the
//     other side.
//   - Settlement grades only wagers still in accepted state, so every wager
//     already paid by the original settlement is skipped and cannot be paid
//     twice.
//   - It runs as a new settlement version, so the original settlement, its
//     outcomes and its ledger transactions are left exactly as they were.
func (s Store) RegradeStrandedWagers(ctx context.Context, marketID, actorUserID, reason string) (SettleReport, error) {
	if !isUUID(marketID) {
		return SettleReport{}, fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
	}
	if !isUUID(actorUserID) {
		return SettleReport{}, betting.ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return SettleReport{}, betting.ErrReasonRequired
	}

	stranded, err := s.StrandedWagers(ctx, marketID)
	if err != nil {
		return SettleReport{}, err
	}
	if len(stranded) == 0 {
		return SettleReport{}, ErrNothingToRegrade
	}

	outcome, divisor, err := s.recordedOutcome(ctx, marketID)
	if err != nil {
		return SettleReport{}, err
	}
	if len(outcome) == 0 {
		return SettleReport{}, fmt.Errorf("%w: no recorded outcome to regrade against", ErrNothingToRegrade)
	}

	// A recorded divisor above 1 means the original settlement was a dead
	// heat. Replaying it is the whole point: a stranded wager on a tied
	// selection must be paid the fraction its peers were paid, not the full
	// price it would fetch if the winners were counted again from an outcome
	// that has since been graded one selection at a time.
	return s.settleWith(ctx, marketID, "graded", outcome, actorUserID, reason, "", true,
		deadHeatSpec{declared: divisor > 1, divisor: divisor})
}

// recordedOutcome reads back the grading a market's latest settlement stored,
// so a regrade replays that decision rather than making a new one. The second
// return is the dead-heat divisor that settlement applied to its winners, 1
// when it was not a dead heat.
func (s Store) recordedOutcome(ctx context.Context, marketID string) (map[string]betting.SettlementResult, int, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT o.selection_id::text, o.outcome, o.dead_heat_divisor
		FROM market_settlement_outcomes o
		WHERE o.market_settlement_id = (
			SELECT ms.id FROM market_settlements ms
			WHERE ms.market_id = $1::uuid AND ms.settlement_type = 'graded'
			ORDER BY ms.version DESC LIMIT 1)`, marketID)
	if err != nil {
		return nil, 0, fmt.Errorf("load recorded outcome for market %s: %w", marketID, err)
	}
	defer rows.Close()
	outcome := make(map[string]betting.SettlementResult)
	divisor := 1
	for rows.Next() {
		var selectionID, result string
		var rowDivisor int
		if err := rows.Scan(&selectionID, &result, &rowDivisor); err != nil {
			return nil, 0, fmt.Errorf("scan recorded outcome: %w", err)
		}
		outcome[selectionID] = betting.SettlementResult(result)
		// Every winner in one settlement carries the same divisor; losers
		// carry 1. Taking the largest reads the tie off the winners without
		// depending on row order.
		if rowDivisor > divisor {
			divisor = rowDivisor
		}
	}
	return outcome, divisor, rows.Err()
}
