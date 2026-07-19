package bettingpg

import (
	"context"
	"fmt"

	"github.com/dgunzy/go-book/internal/betting"
)

// MatchSettleReport summarizes the outcome of SettleMatchMarkets: which
// match-type markets were settled automatically, and which were skipped
// because at least one of their selections has no recognized
// semantic_result_key and therefore requires manual settlement.
type MatchSettleReport struct {
	MatchID string
	Settled []string
	Skipped []string
}

// SettleMatchMarkets finds every match-type market for matchID still in
// open, closed, or settlement_pending state, auto-closes any that are still
// open (the result is in), and settles the rest by mapping the verified
// match result onto each market's selections through the
// semantic_result_key convention (see buildMatchOutcome). Markets where that
// mapping cannot be made with confidence are left untouched and reported as
// skipped rather than guessed.
func (s Store) SettleMatchMarkets(ctx context.Context, matchID, outcome, winningSideID, verifiedResultID, actor string) (MatchSettleReport, error) {
	report := MatchSettleReport{MatchID: matchID}

	marketIDs, states, err := s.matchMarketsToSettle(ctx, matchID)
	if err != nil {
		return report, err
	}

	for _, marketID := range marketIDs {
		if states[marketID] == string(betting.MarketOpen) {
			if err := s.CloseMarket(ctx, marketID, actor); err != nil {
				return report, fmt.Errorf("auto-close market %s before settlement: %w", marketID, err)
			}
		}

		semanticKeys, err := s.selectionSemanticKeys(ctx, marketID)
		if err != nil {
			return report, err
		}
		marketOutcome, ok := buildMatchOutcome(semanticKeys, outcome, winningSideID)
		if !ok {
			report.Skipped = append(report.Skipped, marketID)
			continue
		}

		if _, err := s.SettleMarket(ctx, SettleMarketRequest{
			MarketID:         marketID,
			Outcome:          marketOutcome,
			ActorUserID:      actor,
			Reason:           "match result verified",
			VerifiedResultID: verifiedResultID,
		}); err != nil {
			return report, fmt.Errorf("settle market %s: %w", marketID, err)
		}
		report.Settled = append(report.Settled, marketID)
	}
	return report, nil
}

func (s Store) matchMarketsToSettle(ctx context.Context, matchID string) ([]string, map[string]string, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id::text, state FROM markets
		WHERE match_id = $1::uuid AND market_type = 'match' AND state IN ('open', 'closed', 'settlement_pending')
		ORDER BY id`, matchID)
	if err != nil {
		return nil, nil, fmt.Errorf("load match markets for match %s: %w", matchID, err)
	}
	defer rows.Close()
	var ids []string
	states := make(map[string]string)
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, nil, fmt.Errorf("scan match market: %w", err)
		}
		ids = append(ids, id)
		states[id] = state
	}
	return ids, states, rows.Err()
}

func (s Store) selectionSemanticKeys(ctx context.Context, marketID string) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, coalesce(semantic_result_key, '') FROM selections WHERE market_id = $1::uuid`, marketID)
	if err != nil {
		return nil, fmt.Errorf("load selection semantic keys for market %s: %w", marketID, err)
	}
	defer rows.Close()
	keys := make(map[string]string)
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, fmt.Errorf("scan selection semantic key: %w", err)
		}
		keys[id] = key
	}
	return keys, rows.Err()
}
