package bettingpg

import (
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
)

// sideSemanticKeyPrefix and tieSemanticKey define the semantic_result_key
// convention match-type selections must use for automatic settlement:
// "side:<match_side_uuid>" for a side winning, or "tie" for a tied result.
const (
	sideSemanticKeyPrefix = "side:"
	tieSemanticKey        = "tie"
)

func sideSemanticKey(sideID string) string { return sideSemanticKeyPrefix + sideID }

// buildMatchOutcome maps a verified match result onto a per-selection
// settlement outcome using the semantic_result_key convention. selections
// maps selection ID to its raw semantic_result_key column value (empty
// string for NULL). It is a pure function so the mapping rules can be unit
// tested without a database.
//
// Rules:
//   - Every selection must have a recognized key ("side:<uuid>" or "tie");
//     if any selection's key is empty or unrecognized, ok is false and the
//     market must be skipped for manual settlement rather than guessed.
//   - outcome "side_win": the selection keyed "side:<winningSideID>" wins;
//     every other selection loses.
//   - outcome "tie": if a "tie" selection exists, it wins and every other
//     selection loses. If no selection is keyed "tie", every selection is
//     pushed (stake refunded) since none of the offered outcomes occurred.
//   - Any other outcome value is unrecognized and returns ok=false.
func buildMatchOutcome(selections map[string]string, outcome, winningSideID string) (map[string]betting.SettlementResult, bool) {
	if len(selections) == 0 {
		return nil, false
	}
	for _, key := range selections {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, false
		}
		if key != tieSemanticKey && !strings.HasPrefix(key, sideSemanticKeyPrefix) {
			return nil, false
		}
	}

	result := make(map[string]betting.SettlementResult, len(selections))
	switch outcome {
	case "side_win":
		winKey := sideSemanticKey(winningSideID)
		for id, key := range selections {
			if key == winKey {
				result[id] = betting.ResultWin
			} else {
				result[id] = betting.ResultLoss
			}
		}
	case "tie":
		hasTieSelection := false
		for _, key := range selections {
			if key == tieSemanticKey {
				hasTieSelection = true
				break
			}
		}
		for id, key := range selections {
			switch {
			case !hasTieSelection:
				result[id] = betting.ResultPush
			case key == tieSemanticKey:
				result[id] = betting.ResultWin
			default:
				result[id] = betting.ResultLoss
			}
		}
	default:
		return nil, false
	}
	return result, true
}
