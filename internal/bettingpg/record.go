package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// wagerRecordLimit caps the record page. It is a review surface, not an
// export: the full history is always in the ledger.
const wagerRecordLimit = 200

// WagerRecordRow is one wager with the three prices that describe it: the line
// the market opened at, the line the member actually took, and where the line
// finished. Together they say whether the book was picked off or got the best
// of it, which no single price can show on its own.
type WagerRecordRow struct {
	ID             string
	PlacedAt       time.Time
	MemberName     string
	MarketTitle    string
	MarketState    betting.MarketState
	SelectionTerms string
	// OpeningOdds is the price the selection was posted at, the stable prior
	// the pricing engine reprices from.
	OpeningOdds ledger.AmericanOdds
	// TakenOdds is the snapshot the wager was placed and filled at.
	TakenOdds ledger.AmericanOdds
	// ClosingOdds is the price the selection is offered at now. It is only the
	// closing line once the market has stopped taking action, which
	// MarketClosed reports.
	ClosingOdds ledger.AmericanOdds
	Stake       ledger.Money
	// PotentialProfit is what the wager pays at the odds it was taken at.
	PotentialProfit ledger.Money
	State           betting.WagerState
	// Result is the settlement outcome (win, loss, push, void) once the wager
	// has been graded, and empty before that.
	Result betting.SettlementResult
}

// MarketClosed reports whether this wager's market has stopped taking action,
// which is what makes its closing line final.
func (r WagerRecordRow) MarketClosed() bool {
	return r.MarketState != betting.MarketOpen && r.MarketState != betting.MarketDraft
}

const wagerRecordSQL = `
SELECT w.id::text, w.placed_at, u.display_name, m.title, m.state, w.accepted_terms,
       s.opening_american_odds, w.accepted_american_odds, s.offered_american_odds,
       w.stake_cents, w.currency::text, w.potential_profit_cents, w.state,
       coalesce(ws.result, '')
FROM wagers w
JOIN users u ON u.id = w.user_id
JOIN markets m ON m.id = w.market_id
JOIN selections s ON s.market_id = w.market_id AND s.id = w.selection_id
LEFT JOIN wager_settlements ws ON ws.wager_id = w.id
    AND NOT EXISTS (SELECT 1 FROM wager_settlements later
                    WHERE later.supersedes_wager_settlement_id = ws.id)
ORDER BY w.placed_at DESC, w.id DESC
LIMIT $1`

// ListWagerRecord returns every wager the book has taken, newest first, with
// its opening, taken, and closing prices. Unlike the approval queue it is not
// filtered by state: a settled wager is exactly the one worth reviewing.
func (s Store) ListWagerRecord(ctx context.Context, limit int) ([]WagerRecordRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	if limit <= 0 || limit > wagerRecordLimit {
		limit = wagerRecordLimit
	}
	rows, err := s.DB.Query(ctx, wagerRecordSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("query wager record: %w", err)
	}
	defer rows.Close()

	result := make([]WagerRecordRow, 0)
	for rows.Next() {
		var row WagerRecordRow
		var openingOdds, takenOdds, closingOdds int32
		var stakeCents, profitCents int64
		var currency, marketState, wagerState, settlementResult string
		if err := rows.Scan(&row.ID, &row.PlacedAt, &row.MemberName, &row.MarketTitle, &marketState,
			&row.SelectionTerms, &openingOdds, &takenOdds, &closingOdds,
			&stakeCents, &currency, &profitCents, &wagerState, &settlementResult); err != nil {
			return nil, fmt.Errorf("scan wager record: %w", err)
		}
		if err := fillWagerMoney(&row.TakenOdds, &row.Stake, &row.PotentialProfit,
			takenOdds, stakeCents, profitCents, currency); err != nil {
			return nil, fmt.Errorf("wager %s: %w", row.ID, err)
		}
		if row.OpeningOdds, err = ledger.NewAmericanOdds(openingOdds); err != nil {
			return nil, fmt.Errorf("wager %s opening line: %w", row.ID, err)
		}
		if row.ClosingOdds, err = ledger.NewAmericanOdds(closingOdds); err != nil {
			return nil, fmt.Errorf("wager %s closing line: %w", row.ID, err)
		}
		row.PlacedAt = row.PlacedAt.UTC()
		row.MarketState = betting.MarketState(marketState)
		row.State = betting.WagerState(wagerState)
		row.Result = betting.SettlementResult(settlementResult)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wager record: %w", err)
	}
	return result, nil
}
