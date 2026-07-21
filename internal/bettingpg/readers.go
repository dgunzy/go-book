package bettingpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// MarketSelectionRow is one selection inside a market browse row.
type MarketSelectionRow struct {
	ID                  string
	Key                 string
	DisplayTerms        string
	OfferedAmericanOdds ledger.AmericanOdds
	OpeningAmericanOdds ledger.AmericanOdds
	SemanticResultKey   string
	Active              bool
}

// Moved reports whether the live line has drifted from the opening line, i.e.
// dynamic pricing has repriced this selection.
func (r MarketSelectionRow) Moved() bool { return r.OfferedAmericanOdds != r.OpeningAmericanOdds }

// MarketRow is a market with its selections for browse and admin pages.
type MarketRow struct {
	ID             string
	Type           betting.MarketType
	MatchID        string
	Title          string
	State          betting.MarketState
	Currency       ledger.Currency
	DynamicPricing bool
	OpensAt        time.Time
	ClosesAt       time.Time
	Selections     []MarketSelectionRow
}

// AdminWagerRow is one wager in the admin review queue. It includes the
// wagering user's identity, so it must only ever be rendered for admins.
type AdminWagerRow struct {
	ID              string
	UserID          string
	UserDisplayName string
	MarketID        string
	MarketTitle     string
	SelectionTerms  string
	Odds            ledger.AmericanOdds
	Stake           ledger.Money
	PotentialProfit ledger.Money
	State           betting.WagerState
	RejectionReason string
	PlacedAt        time.Time
}

// UserWagerRow is one of a member's own wagers. It intentionally carries no
// other user's identity or data.
type UserWagerRow struct {
	ID              string
	MarketTitle     string
	SelectionTerms  string
	Odds            ledger.AmericanOdds
	Stake           ledger.Money
	PotentialProfit ledger.Money
	State           betting.WagerState
	RejectionReason string
	PlacedAt        time.Time
}

const marketRowsSQL = `
SELECT m.id::text, m.market_type, coalesce(m.match_id::text, ''), m.title, m.state, m.currency::text,
       m.dynamic_pricing, m.opens_at, m.closes_at,
       coalesce(s.id::text, ''), coalesce(s.selection_key, ''), coalesce(s.display_terms, ''),
       coalesce(s.offered_american_odds, 100), coalesce(s.opening_american_odds, 100),
       coalesce(s.semantic_result_key, ''), coalesce(s.active, false)
FROM markets m
LEFT JOIN selections s ON s.market_id = m.id%s
WHERE %s
ORDER BY m.closes_at %s, m.id, s.id`

// ListMarkets returns every market with all of its selections for the admin
// market list, newest closing time first.
func (s Store) ListMarkets(ctx context.Context) ([]MarketRow, error) {
	return s.listMarkets(ctx, fmt.Sprintf(marketRowsSQL, "", "true", "DESC"))
}

// ListOpenMarkets returns markets currently open for wagering (state open,
// inside their open/close window) with only their active selections, soonest
// closing time first.
func (s Store) ListOpenMarkets(ctx context.Context) ([]MarketRow, error) {
	return s.listMarkets(ctx, fmt.Sprintf(marketRowsSQL,
		" AND s.active",
		"m.state = 'open' AND m.closes_at > now() AND (m.opens_at IS NULL OR m.opens_at <= now())",
		"ASC"))
}

func (s Store) listMarkets(ctx context.Context, query string) ([]MarketRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query markets: %w", err)
	}
	defer rows.Close()

	result := make([]MarketRow, 0)
	index := make(map[string]int)
	for rows.Next() {
		var id, marketType, matchID, title, state, currency string
		var dynamicPricing bool
		var opensAt sql.NullTime
		var closesAt time.Time
		var selectionID, selectionKey, displayTerms, semanticKey string
		var odds, openingOdds int32
		var active bool
		if err := rows.Scan(&id, &marketType, &matchID, &title, &state, &currency,
			&dynamicPricing, &opensAt, &closesAt, &selectionID, &selectionKey, &displayTerms, &odds, &openingOdds, &semanticKey, &active); err != nil {
			return nil, fmt.Errorf("scan market row: %w", err)
		}
		position, seen := index[id]
		if !seen {
			parsedCurrency, err := ledger.ParseCurrency(strings.TrimSpace(currency))
			if err != nil {
				return nil, fmt.Errorf("market %s currency: %w", id, err)
			}
			market := MarketRow{
				ID: id, Type: betting.MarketType(marketType), MatchID: matchID, Title: title,
				State: betting.MarketState(state), Currency: parsedCurrency, DynamicPricing: dynamicPricing,
				ClosesAt: closesAt.UTC(),
			}
			if opensAt.Valid {
				market.OpensAt = opensAt.Time.UTC()
			}
			result = append(result, market)
			position = len(result) - 1
			index[id] = position
		}
		if selectionID == "" {
			continue
		}
		parsedOdds, err := ledger.NewAmericanOdds(odds)
		if err != nil {
			return nil, fmt.Errorf("selection %s odds: %w", selectionID, err)
		}
		parsedOpeningOdds, err := ledger.NewAmericanOdds(openingOdds)
		if err != nil {
			return nil, fmt.Errorf("selection %s opening odds: %w", selectionID, err)
		}
		result[position].Selections = append(result[position].Selections, MarketSelectionRow{
			ID: selectionID, Key: selectionKey, DisplayTerms: displayTerms,
			OfferedAmericanOdds: parsedOdds, OpeningAmericanOdds: parsedOpeningOdds,
			SemanticResultKey: semanticKey, Active: active,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate markets: %w", err)
	}
	return result, nil
}

const adminWagersSQL = `
SELECT w.id::text, w.user_id::text, u.display_name, w.market_id::text, m.title, w.accepted_terms,
       w.accepted_american_odds, w.stake_cents, w.currency::text, w.potential_profit_cents, w.state,
       coalesce(w.rejection_reason, ''), w.placed_at
FROM wagers w
JOIN markets m ON m.id = w.market_id
JOIN users u ON u.id = w.user_id
WHERE w.state = $1
ORDER BY w.placed_at, w.id`

// ListWagersByState returns every wager in one state for admin review,
// oldest first so the approval queue is worked in placement order.
func (s Store) ListWagersByState(ctx context.Context, state betting.WagerState) ([]AdminWagerRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, adminWagersSQL, string(state))
	if err != nil {
		return nil, fmt.Errorf("query wagers by state: %w", err)
	}
	defer rows.Close()

	result := make([]AdminWagerRow, 0)
	for rows.Next() {
		var row AdminWagerRow
		var odds int32
		var stakeCents, profitCents int64
		var currency, wagerState string
		if err := rows.Scan(&row.ID, &row.UserID, &row.UserDisplayName, &row.MarketID, &row.MarketTitle,
			&row.SelectionTerms, &odds, &stakeCents, &currency, &profitCents, &wagerState,
			&row.RejectionReason, &row.PlacedAt); err != nil {
			return nil, fmt.Errorf("scan admin wager: %w", err)
		}
		if err := fillWagerMoney(&row.Odds, &row.Stake, &row.PotentialProfit, odds, stakeCents, profitCents, currency); err != nil {
			return nil, fmt.Errorf("wager %s: %w", row.ID, err)
		}
		row.State = betting.WagerState(wagerState)
		row.PlacedAt = row.PlacedAt.UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin wagers: %w", err)
	}
	return result, nil
}

const userWagersSQL = `
SELECT w.id::text, m.title, w.accepted_terms, w.accepted_american_odds, w.stake_cents, w.currency::text,
       w.potential_profit_cents, w.state, coalesce(w.rejection_reason, ''), w.placed_at
FROM wagers w
JOIN markets m ON m.id = w.market_id
WHERE w.user_id = $1::uuid
ORDER BY w.placed_at DESC, w.id DESC`

// ListWagersForUser returns one member's own wagers, newest first. The query
// is scoped by user ID so no other member's wagers can ever be returned.
func (s Store) ListWagersForUser(ctx context.Context, userID string) ([]UserWagerRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	if !isUUID(userID) {
		return nil, fmt.Errorf("%w: listing wagers requires a user ID", betting.ErrInvalid)
	}
	rows, err := s.DB.Query(ctx, userWagersSQL, userID)
	if err != nil {
		return nil, fmt.Errorf("query wagers for user: %w", err)
	}
	defer rows.Close()

	result := make([]UserWagerRow, 0)
	for rows.Next() {
		var row UserWagerRow
		var odds int32
		var stakeCents, profitCents int64
		var currency, wagerState string
		if err := rows.Scan(&row.ID, &row.MarketTitle, &row.SelectionTerms, &odds, &stakeCents,
			&currency, &profitCents, &wagerState, &row.RejectionReason, &row.PlacedAt); err != nil {
			return nil, fmt.Errorf("scan user wager: %w", err)
		}
		if err := fillWagerMoney(&row.Odds, &row.Stake, &row.PotentialProfit, odds, stakeCents, profitCents, currency); err != nil {
			return nil, fmt.Errorf("wager %s: %w", row.ID, err)
		}
		row.State = betting.WagerState(wagerState)
		row.PlacedAt = row.PlacedAt.UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user wagers: %w", err)
	}
	return result, nil
}

func fillWagerMoney(odds *ledger.AmericanOdds, stake, profit *ledger.Money, rawOdds int32, stakeCents, profitCents int64, currency string) error {
	parsedCurrency, err := ledger.ParseCurrency(strings.TrimSpace(currency))
	if err != nil {
		return fmt.Errorf("currency: %w", err)
	}
	if *odds, err = ledger.NewAmericanOdds(rawOdds); err != nil {
		return fmt.Errorf("odds: %w", err)
	}
	if *stake, err = ledger.NewMoney(stakeCents, parsedCurrency); err != nil {
		return fmt.Errorf("stake: %w", err)
	}
	if *profit, err = ledger.NewMoney(profitCents, parsedCurrency); err != nil {
		return fmt.Errorf("potential profit: %w", err)
	}
	return nil
}
