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
	// TotalStakeCapCents caps what every member together may have on this
	// side; zero means the book has set no ceiling on it.
	TotalStakeCapCents int64
	// MaxStakeCents caps what one member may have on this side; zero means
	// only the market's own cap applies, if it has one.
	MaxStakeCents       int64
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
	// MaxStakeCents caps what one member may have on this market; zero means
	// no cap.
	MaxStakeCents int64
	OpensAt       time.Time
	ClosesAt      time.Time
	// LiveWagerCents counts wagers that are pending or accepted on this market.
	// It is what decides whether a market can be closed out without grading,
	// and it is read here so the admin list never offers that on a market
	// somebody has money on.
	LiveWagerCount int
	Selections     []MarketSelectionRow
}

// HasWagers reports whether anybody has money on this market.
func (m MarketRow) HasWagers() bool { return m.LiveWagerCount > 0 }

// MatchMarketOption is an open competition match that does not yet have an
// active match market. The IDs are retained for server-side settlement
// mapping; the browser renders only the event, teams, and participants.
type MatchMarketOption struct {
	MatchID       string
	EventName     string
	SeasonYear    int
	MatchNumber   int
	Format        string
	Side1ID       string
	Side1TeamName string
	Side1Players  string
	Side2ID       string
	Side2TeamName string
	Side2Players  string
}

// Title is the canonical, readable title used by a match winner market.
func (m MatchMarketOption) Title() string {
	return fmt.Sprintf("%s %d · Match %d · %s vs %s", m.EventName, m.SeasonYear, m.MatchNumber, m.Side1BetLabel(), m.Side2BetLabel())
}

// Side1Label and Side2Label keep player identity in every compact match label.
func (m MatchMarketOption) Side1Label() string {
	return matchSideLabel(m.Side1TeamName, m.Side1Players)
}
func (m MatchMarketOption) Side2Label() string {
	return matchSideLabel(m.Side2TeamName, m.Side2Players)
}

// Side1BetLabel and Side2BetLabel name the wager from its golfers. The team
// remains a fallback only for pre-enforcement historical setup rows.
func (m MatchMarketOption) Side1BetLabel() string {
	return matchBetLabel(m.Side1TeamName, m.Side1Players)
}
func (m MatchMarketOption) Side2BetLabel() string {
	return matchBetLabel(m.Side2TeamName, m.Side2Players)
}

func matchSideLabel(team, players string) string {
	if strings.TrimSpace(players) == "" {
		return team
	}
	return team + " — " + players
}

func matchBetLabel(team, players string) string {
	if strings.TrimSpace(players) != "" {
		return players
	}
	return team
}

// ListMarketableMatches returns scheduled/open matches that have the required
// participants and do not already have a non-terminal match market.
// Participant names are aggregated per side so admins never need to copy
// competition UUIDs into the betting form.
func (s Store) ListMarketableMatches(ctx context.Context) ([]MatchMarketOption, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT m.id::text, e.name, e.season_year, m.match_number, m.format,
		       s1.id::text, t1.name, coalesce(p1.names, ''),
		       s2.id::text, t2.name, coalesce(p2.names, '')
		FROM matches m
		JOIN events e ON e.id = m.event_id
		JOIN match_sides s1 ON s1.match_id = m.id AND s1.side_number = 1
		JOIN teams t1 ON t1.id = s1.team_id
		JOIN match_sides s2 ON s2.match_id = m.id AND s2.side_number = 2
		JOIN teams t2 ON t2.id = s2.team_id
		LEFT JOIN LATERAL (
			SELECT string_agg(p.display_name, ', ' ORDER BY mp.playing_order, p.display_name) AS names,
			       count(*) AS player_count
			FROM match_participants mp JOIN players p ON p.id = mp.player_id AND p.active
			WHERE mp.match_side_id = s1.id
		) p1 ON true
		LEFT JOIN LATERAL (
			SELECT string_agg(p.display_name, ', ' ORDER BY mp.playing_order, p.display_name) AS names,
			       count(*) AS player_count
			FROM match_participants mp JOIN players p ON p.id = mp.player_id AND p.active
			WHERE mp.match_side_id = s2.id
		) p2 ON true
		WHERE m.state IN ('scheduled', 'open')
		  AND NOT EXISTS (
			SELECT 1 FROM markets existing
			WHERE existing.match_id = m.id AND existing.state NOT IN ('voided', 'cancelled')
		  )
		  AND (
			(m.format = 'singles' AND p1.player_count = 1 AND p2.player_count = 1)
			OR (m.format IN ('fourball', 'foursomes', 'scramble') AND p1.player_count = 2 AND p2.player_count = 2)
			OR (m.format = 'other' AND p1.player_count >= 1 AND p2.player_count >= 1)
		  )
		ORDER BY e.season_year DESC, e.name, m.match_number`)
	if err != nil {
		return nil, fmt.Errorf("list marketable matches: %w", err)
	}
	defer rows.Close()
	var result []MatchMarketOption
	for rows.Next() {
		var option MatchMarketOption
		if err := rows.Scan(&option.MatchID, &option.EventName, &option.SeasonYear, &option.MatchNumber, &option.Format,
			&option.Side1ID, &option.Side1TeamName, &option.Side1Players,
			&option.Side2ID, &option.Side2TeamName, &option.Side2Players); err != nil {
			return nil, fmt.Errorf("scan marketable match: %w", err)
		}
		result = append(result, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate marketable matches: %w", err)
	}
	return result, nil
}

// AdminWagerRow is one wager in the admin review queue. It includes the
// wagering user's identity, so it must only ever be rendered for admins.
type AdminWagerRow struct {
	ID              string
	UserID          string
	UserDisplayName string
	MarketID        string
	MarketTitle     string
	SelectionID     string
	SelectionTerms  string
	Odds            ledger.AmericanOdds
	Stake           ledger.Money
	PotentialProfit ledger.Money
	State           betting.WagerState
	RejectionReason string
	PlacedAt        time.Time
	// CurrentOdds is the price the selection is offered at right now, which
	// can differ from the snapshot the wager was placed at when dynamic
	// pricing moved the line. SelectionActive and MarketState describe
	// whether that live line is still on the board.
	CurrentOdds     ledger.AmericanOdds
	SelectionActive bool
	MarketState     betting.MarketState
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
	// CurrentOdds, SelectionActive, and MarketState describe the line the
	// member's selection is offered at right now, so a member looking at a
	// pending wager can see whether the price moved while they waited.
	CurrentOdds     ledger.AmericanOdds
	SelectionActive bool
	MarketState     betting.MarketState
}

// Markets are ordered by closing time, then by when they were created. A whole
// board posted for the same event closes at the same moment, so closing time
// alone decides nothing and creation order is what a reader actually expects —
// the order the markets were put up. Both follow the caller's direction, so a
// list reading newest-first is newest-first the whole way down. Market ID is a
// last resort for stability only; on its own, as this used to sort, it is a
// random UUID order.
//
// Selections come back shortest price first, so a board reads like a price
// board: the favourite at the top down to the longest shot. Ascending American
// odds is exactly that order without any arithmetic — valid prices are at most
// -100 or at least +100, so -500, -110, +100, +3000 runs from most likely to
// least. Sorting by selection ID instead, as this used to, is a random UUID
// order, which on a sixteen-name prop looks like no order at all.
const marketRowsSQL = `
SELECT m.id::text, m.market_type, coalesce(m.match_id::text, ''), m.title, m.state, m.currency::text,
       m.dynamic_pricing, coalesce(m.max_stake_cents, 0), m.opens_at, m.closes_at,
       (SELECT count(*) FROM wagers w WHERE w.market_id = m.id AND w.state IN ('pending', 'accepted')),
       coalesce(s.id::text, ''), coalesce(s.selection_key, ''), coalesce(s.display_terms, ''),
       coalesce(s.offered_american_odds, 100), coalesce(s.opening_american_odds, 100),
       coalesce(s.max_stake_cents, 0), coalesce(s.total_stake_cap_cents, 0),
       coalesce(s.semantic_result_key, ''), coalesce(s.active, false)
FROM markets m
LEFT JOIN selections s ON s.market_id = m.id%s
WHERE %s
ORDER BY m.closes_at %[3]s, m.created_at %[3]s, m.id, s.offered_american_odds, s.display_terms, s.id`

// ListMarkets returns every market with all of its selections for the admin
// market list, newest closing time first.
func (s Store) ListMarkets(ctx context.Context) ([]MarketRow, error) {
	return s.listMarkets(ctx, fmt.Sprintf(marketRowsSQL, "", "true", "DESC"))
}

// ListOpenMarkets returns markets currently open for wagering (state open,
// inside their open/close window) with only their active selections, soonest
// closing time first. It applies no restrictions, so it is for admin views;
// member views must use ListOpenMarketsForUser.
func (s Store) ListOpenMarkets(ctx context.Context) ([]MarketRow, error) {
	return s.listMarkets(ctx, fmt.Sprintf(marketRowsSQL,
		" AND s.active",
		openMarketPredicate,
		"ASC"))
}

// ListOpenMarketsForUser is the member's board: the open markets minus
// everything this member is restricted from. A whole-market restriction hides
// the market; a selection-level one hides just that outcome and leaves the
// rest bettable. A market left with no selections they may back is dropped
// rather than shown as an empty card.
//
// Hiding is a courtesy, not the control: PlaceWager re-checks restrictions
// against the database, so a member who reconstructs the form still cannot bet.
func (s Store) ListOpenMarketsForUser(ctx context.Context, userID string) ([]MarketRow, error) {
	if !isUUID(userID) {
		return nil, fmt.Errorf("%w: listing markets requires a user ID", betting.ErrInvalid)
	}
	markets, err := s.listMarkets(ctx, fmt.Sprintf(marketRowsSQL,
		" AND s.active",
		openMarketPredicate+` AND NOT EXISTS (
			SELECT 1 FROM market_restrictions r
			WHERE r.market_id = m.id AND r.user_id = $1::uuid AND r.selection_id IS NULL)`,
		"ASC"), userID)
	if err != nil {
		return nil, err
	}

	hidden, err := s.restrictedSelections(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(hidden) == 0 {
		return markets, nil
	}
	visible := make([]MarketRow, 0, len(markets))
	for _, market := range markets {
		selections := make([]MarketSelectionRow, 0, len(market.Selections))
		for _, selection := range market.Selections {
			if hidden[selection.ID] {
				continue
			}
			selections = append(selections, selection)
		}
		if len(selections) == 0 {
			continue
		}
		market.Selections = selections
		visible = append(visible, market)
	}
	return visible, nil
}

// restrictedSelections is the set of selection IDs this member may not back.
func (s Store) restrictedSelections(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT selection_id::text FROM market_restrictions
		WHERE user_id = $1::uuid AND selection_id IS NOT NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("load member selection restrictions: %w", err)
	}
	defer rows.Close()
	hidden := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan member selection restriction: %w", err)
		}
		hidden[id] = true
	}
	return hidden, rows.Err()
}

const openMarketPredicate = "m.state = 'open' AND m.closes_at > now() AND (m.opens_at IS NULL OR m.opens_at <= now())"

func (s Store) listMarkets(ctx context.Context, query string, args ...any) ([]MarketRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query markets: %w", err)
	}
	defer rows.Close()

	result := make([]MarketRow, 0)
	index := make(map[string]int)
	for rows.Next() {
		var id, marketType, matchID, title, state, currency string
		var dynamicPricing bool
		var maxStakeCents int64
		var opensAt sql.NullTime
		var closesAt time.Time
		var liveWagers int
		var selectionID, selectionKey, displayTerms, semanticKey string
		var odds, openingOdds int32
		var selectionMaxStake, selectionTotalCap int64
		var active bool
		if err := rows.Scan(&id, &marketType, &matchID, &title, &state, &currency,
			&dynamicPricing, &maxStakeCents, &opensAt, &closesAt, &liveWagers, &selectionID, &selectionKey, &displayTerms, &odds, &openingOdds, &selectionMaxStake, &selectionTotalCap, &semanticKey, &active); err != nil {
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
				LiveWagerCount: liveWagers,
				State:          betting.MarketState(state), Currency: parsedCurrency, DynamicPricing: dynamicPricing,
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
			ID: selectionID, Key: selectionKey, DisplayTerms: displayTerms, MaxStakeCents: selectionMaxStake,
			TotalStakeCapCents:  selectionTotalCap,
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
SELECT w.id::text, w.user_id::text, u.display_name, w.market_id::text, m.title,
       w.selection_id::text, w.accepted_terms,
       w.accepted_american_odds, w.stake_cents, w.currency::text, w.potential_profit_cents, w.state,
       coalesce(w.rejection_reason, ''), w.placed_at,
       s.offered_american_odds, s.active, m.state
FROM wagers w
JOIN markets m ON m.id = w.market_id
JOIN users u ON u.id = w.user_id
JOIN selections s ON s.market_id = w.market_id AND s.id = w.selection_id
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
		var odds, currentOdds int32
		var stakeCents, profitCents int64
		var currency, wagerState, marketState string
		if err := rows.Scan(&row.ID, &row.UserID, &row.UserDisplayName, &row.MarketID, &row.MarketTitle,
			&row.SelectionID, &row.SelectionTerms, &odds, &stakeCents, &currency, &profitCents, &wagerState,
			&row.RejectionReason, &row.PlacedAt, &currentOdds, &row.SelectionActive, &marketState); err != nil {
			return nil, fmt.Errorf("scan admin wager: %w", err)
		}
		if err := fillWagerMoney(&row.Odds, &row.Stake, &row.PotentialProfit, odds, stakeCents, profitCents, currency); err != nil {
			return nil, fmt.Errorf("wager %s: %w", row.ID, err)
		}
		parsedCurrentOdds, err := ledger.NewAmericanOdds(currentOdds)
		if err != nil {
			return nil, fmt.Errorf("wager %s live line: %w", row.ID, err)
		}
		row.CurrentOdds = parsedCurrentOdds
		row.MarketState = betting.MarketState(marketState)
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
       w.potential_profit_cents, w.state, coalesce(w.rejection_reason, ''), w.placed_at,
       s.offered_american_odds, s.active, m.state
FROM wagers w
JOIN markets m ON m.id = w.market_id
JOIN selections s ON s.market_id = w.market_id AND s.id = w.selection_id
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
		var odds, currentOdds int32
		var stakeCents, profitCents int64
		var currency, wagerState, marketState string
		if err := rows.Scan(&row.ID, &row.MarketTitle, &row.SelectionTerms, &odds, &stakeCents,
			&currency, &profitCents, &wagerState, &row.RejectionReason, &row.PlacedAt,
			&currentOdds, &row.SelectionActive, &marketState); err != nil {
			return nil, fmt.Errorf("scan user wager: %w", err)
		}
		if err := fillWagerMoney(&row.Odds, &row.Stake, &row.PotentialProfit, odds, stakeCents, profitCents, currency); err != nil {
			return nil, fmt.Errorf("wager %s: %w", row.ID, err)
		}
		parsedCurrentOdds, err := ledger.NewAmericanOdds(currentOdds)
		if err != nil {
			return nil, fmt.Errorf("wager %s live line: %w", row.ID, err)
		}
		row.CurrentOdds = parsedCurrentOdds
		row.MarketState = betting.MarketState(marketState)
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
