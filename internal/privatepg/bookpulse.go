package privatepg

import (
	"context"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/privateweb"
)

// bookCurrency is the currency the dashboard totals are reported in. Sums are
// scoped to it rather than added across currencies, which would be meaningless.
const bookCurrency = ledger.CAD

// openWagerLimit caps the "action on the board" list. Exposure per market is
// summarised above it, so the list is a recent-activity read, not a ledger.
const openWagerLimit = 25

// BookPulse returns the book-wide dashboard: realized house result, money at
// risk, per-market exposure, the open action, and every player's record. It
// names other members, so callers must gate it on an admin session.
func (r *Readers) BookPulse(ctx context.Context) (privateweb.BookPulse, error) {
	pulse := privateweb.BookPulse{
		AsOf:         time.Now().UTC(),
		HouseResult:  ledger.Money{Currency: bookCurrency},
		Escrow:       ledger.Money{Currency: bookCurrency},
		Handle:       ledger.Money{Currency: bookCurrency},
		WorstCase:    ledger.Money{Currency: bookCurrency},
		BestCase:     ledger.Money{Currency: bookCurrency},
		PendingStake: ledger.Money{Currency: bookCurrency},
		PlayerScale:  ledger.Money{Currency: bookCurrency},
	}

	var house, escrow, handle, pendingStake int64
	var openMarkets, pendingWagers, openWagers int64
	if err := r.db.QueryRow(ctx, bookTotalsSQL, string(bookCurrency)).Scan(
		&pulse.AsOf, &house, &escrow, &handle, &pendingStake,
		&openMarkets, &pendingWagers, &openWagers,
	); err != nil {
		return privateweb.BookPulse{}, fmt.Errorf("load book totals: %w", err)
	}
	pulse.HouseResult = ledger.Money{Cents: house, Currency: bookCurrency}
	pulse.Escrow = ledger.Money{Cents: escrow, Currency: bookCurrency}
	pulse.Handle = ledger.Money{Cents: handle, Currency: bookCurrency}
	pulse.PendingStake = ledger.Money{Cents: pendingStake, Currency: bookCurrency}
	for target, value := range map[*int]int64{
		&pulse.OpenMarkets:    openMarkets,
		&pulse.PendingWagers:  pendingWagers,
		&pulse.OpenWagerCount: openWagers,
	} {
		converted, err := countToInt(value)
		if err != nil {
			return privateweb.BookPulse{}, err
		}
		*target = converted
	}

	exposure, err := r.exposureRows(ctx)
	if err != nil {
		return privateweb.BookPulse{}, err
	}
	pulse.Exposure = exposure
	for _, market := range exposure {
		worst, best := marketSwing(market)
		pulse.WorstCase.Cents += worst
		pulse.BestCase.Cents += best
	}

	if pulse.OpenWagers, err = r.openWagerRows(ctx); err != nil {
		return privateweb.BookPulse{}, err
	}
	if pulse.Players, err = r.playerResults(ctx); err != nil {
		return privateweb.BookPulse{}, err
	}
	pulse.PlayerScale = ledger.Money{Cents: scalePlayerBars(pulse.Players), Currency: bookCurrency}
	return pulse, nil
}

// marketSwing returns the house's net on this market's worst and best outcomes.
// A market nobody has bet is a zero swing either way.
func marketSwing(market privateweb.MarketExposure) (worst, best int64) {
	if len(market.Outcomes) == 0 {
		return 0, 0
	}
	worst, best = market.Outcomes[0].HouseNet.Cents, market.Outcomes[0].HouseNet.Cents
	for _, outcome := range market.Outcomes[1:] {
		if outcome.HouseNet.Cents < worst {
			worst = outcome.HouseNet.Cents
		}
		if outcome.HouseNet.Cents > best {
			best = outcome.HouseNet.Cents
		}
	}
	return worst, best
}

// scalePlayerBars sizes the standings bars against the largest absolute result
// on the board, so the longest bar always fills its track.
func scalePlayerBars(players []privateweb.PlayerResult) int64 {
	var scale int64
	for _, player := range players {
		magnitude := player.Net.Cents
		if magnitude < 0 {
			magnitude = -magnitude
		}
		if magnitude > scale {
			scale = magnitude
		}
	}
	if scale == 0 {
		return 0
	}
	for i := range players {
		magnitude := players[i].Net.Cents
		if magnitude < 0 {
			magnitude = -magnitude
		}
		players[i].BarPercent = int(magnitude * 100 / scale)
	}
	return scale
}

func (r *Readers) exposureRows(ctx context.Context) ([]privateweb.MarketExposure, error) {
	rows, err := r.db.Query(ctx, exposureSQL, string(bookCurrency))
	if err != nil {
		return nil, fmt.Errorf("query market exposure: %w", err)
	}
	defer rows.Close()

	result := make([]privateweb.MarketExposure, 0)
	index := make(map[string]int)
	for rows.Next() {
		var marketID, title, state, selection string
		var closesAt time.Time
		var wagerCount, stakeCents, payoutCents int64
		if err := rows.Scan(&marketID, &title, &state, &closesAt, &selection,
			&wagerCount, &stakeCents, &payoutCents); err != nil {
			return nil, fmt.Errorf("scan market exposure: %w", err)
		}
		position, seen := index[marketID]
		if !seen {
			result = append(result, privateweb.MarketExposure{
				Market: title, State: state, ClosesAt: closesAt.UTC(),
				Stake: ledger.Money{Currency: bookCurrency},
			})
			position = len(result) - 1
			index[marketID] = position
		}
		count, err := countToInt(wagerCount)
		if err != nil {
			return nil, err
		}
		market := &result[position]
		market.Wagers += count
		market.Stake.Cents += stakeCents
		market.Outcomes = append(market.Outcomes, privateweb.ExposureOutcome{
			Selection: selection, Wagers: count,
			Stake:  ledger.Money{Cents: stakeCents, Currency: bookCurrency},
			Payout: ledger.Money{Cents: payoutCents, Currency: bookCurrency},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate market exposure: %w", err)
	}

	// The house nets every stake in the market less what the winning side is
	// paid, so this can only be filled in once the whole market is read.
	for i := range result {
		market := &result[i]
		for j := range market.Outcomes {
			market.Outcomes[j].HouseNet = ledger.Money{
				Cents:    market.Stake.Cents - market.Outcomes[j].Payout.Cents,
				Currency: bookCurrency,
			}
		}
		worst, _ := marketSwing(*market)
		for j := range market.Outcomes {
			if market.Outcomes[j].HouseNet.Cents == worst {
				market.Outcomes[j].Worst = true
				break
			}
		}
	}
	return result, nil
}

func (r *Readers) openWagerRows(ctx context.Context) ([]privateweb.OpenWagerRow, error) {
	rows, err := r.db.Query(ctx, openWagersSQL, openWagerLimit)
	if err != nil {
		return nil, fmt.Errorf("query open wagers: %w", err)
	}
	defer rows.Close()

	result := make([]privateweb.OpenWagerRow, 0)
	for rows.Next() {
		var row privateweb.OpenWagerRow
		var odds int32
		var stakeCents, profitCents int64
		var currencyCode string
		if err := rows.Scan(&row.PlacedAt, &row.Member, &row.Market, &row.Selection,
			&odds, &stakeCents, &profitCents, &currencyCode); err != nil {
			return nil, fmt.Errorf("scan open wager: %w", err)
		}
		currency, err := ledger.ParseCurrency(currencyCode)
		if err != nil {
			return nil, fmt.Errorf("open wager currency: %w", err)
		}
		if row.Odds, err = ledger.NewAmericanOdds(odds); err != nil {
			return nil, fmt.Errorf("open wager odds: %w", err)
		}
		row.PlacedAt = row.PlacedAt.UTC()
		row.Stake = ledger.Money{Cents: stakeCents, Currency: currency}
		row.ToWin = ledger.Money{Cents: profitCents, Currency: currency}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open wagers: %w", err)
	}
	return result, nil
}

func (r *Readers) playerResults(ctx context.Context) ([]privateweb.PlayerResult, error) {
	rows, err := r.db.Query(ctx, playerResultsSQL)
	if err != nil {
		return nil, fmt.Errorf("query player results: %w", err)
	}
	defer rows.Close()

	result := make([]privateweb.PlayerResult, 0)
	for rows.Next() {
		var row privateweb.PlayerResult
		var netCents, handleCents int64
		var won, lost, pushed, open int64
		if err := rows.Scan(&row.Name, &netCents, &won, &lost, &pushed, &open, &handleCents); err != nil {
			return nil, fmt.Errorf("scan player result: %w", err)
		}
		for target, value := range map[*int]int64{
			&row.Won: won, &row.Lost: lost, &row.Pushed: pushed, &row.Open: open,
		} {
			converted, err := countToInt(value)
			if err != nil {
				return nil, err
			}
			*target = converted
		}
		row.Net = ledger.Money{Cents: netCents, Currency: bookCurrency}
		row.Handle = ledger.Money{Cents: handleCents, Currency: bookCurrency}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate player results: %w", err)
	}
	return result, nil
}

// bookTotalsSQL reads the headline numbers in one round trip. Money is scoped
// to $1 (the book currency) so nothing is added across currencies.
const bookTotalsSQL = `
SELECT statement_timestamp(),
       coalesce((SELECT sum(balance_cents) FROM ledger_account_balances
                 WHERE account_type = 'house_clearing' AND currency::text = $1), 0)::bigint,
       coalesce((SELECT sum(balance_cents) FROM ledger_account_balances
                 WHERE account_type = 'wager_escrow' AND currency::text = $1), 0)::bigint,
       coalesce((SELECT sum(stake_cents) FROM wagers
                 WHERE state IN ('accepted', 'settled') AND currency::text = $1), 0)::bigint,
       coalesce((SELECT sum(stake_cents) FROM wagers
                 WHERE state = 'pending' AND currency::text = $1), 0)::bigint,
       (SELECT count(*) FROM markets WHERE state = 'open')::bigint,
       (SELECT count(*) FROM wagers WHERE state = 'pending')::bigint,
       (SELECT count(*) FROM wagers WHERE state = 'accepted')::bigint`

// exposureSQL lists every selection of every market that still carries risk,
// with the accepted action on it and what that side would be paid.
const exposureSQL = `
SELECT m.id::text, m.title, m.state, m.closes_at, s.display_terms,
       count(w.id)::bigint,
       coalesce(sum(w.stake_cents), 0)::bigint,
       coalesce(sum(w.stake_cents + w.potential_profit_cents), 0)::bigint
FROM markets m
JOIN selections s ON s.market_id = m.id
LEFT JOIN wagers w ON w.market_id = m.id AND w.selection_id = s.id
    AND w.state = 'accepted' AND w.currency::text = $1
WHERE m.state IN ('open', 'closed', 'settlement_pending')
GROUP BY m.id, m.title, m.state, m.closes_at, s.id, s.display_terms
ORDER BY m.closes_at, m.id, s.id`

const openWagersSQL = `
SELECT w.placed_at, u.display_name, m.title, w.accepted_terms,
       w.accepted_american_odds, w.stake_cents, w.potential_profit_cents, w.currency::text
FROM wagers w
JOIN users u ON u.id = w.user_id
JOIN markets m ON m.id = w.market_id
WHERE w.state = 'accepted'
ORDER BY w.placed_at DESC, w.id DESC
LIMIT $1`

// playerResultsSQL reports each member's realized result from the ledger — the
// sum of every wager posting against their accounts — alongside their record.
// Members who have never had action are left out.
const playerResultsSQL = `
WITH betting AS (
    SELECT a.owner_user_id AS user_id, sum(p.amount_cents)::bigint AS net_cents
    FROM ledger_postings p
    JOIN ledger_accounts a ON a.id = p.account_id
    JOIN ledger_transactions t ON t.id = p.transaction_id
    WHERE a.owner_user_id IS NOT NULL
      AND t.transaction_type IN ('wager_acceptance', 'wager_win', 'wager_loss', 'wager_refund')
    GROUP BY a.owner_user_id
),
record AS (
    SELECT w.user_id,
           count(*) FILTER (WHERE ws.result = 'win')::bigint AS won,
           count(*) FILTER (WHERE ws.result = 'loss')::bigint AS lost,
           count(*) FILTER (WHERE ws.result IN ('push', 'void'))::bigint AS pushed,
           count(*) FILTER (WHERE w.state = 'accepted')::bigint AS open_wagers,
           coalesce(sum(w.stake_cents) FILTER (WHERE w.state IN ('accepted', 'settled')), 0)::bigint AS handle_cents
    FROM wagers w
    LEFT JOIN wager_settlements ws ON ws.wager_id = w.id
        AND NOT EXISTS (SELECT 1 FROM wager_settlements later
                        WHERE later.supersedes_wager_settlement_id = ws.id)
    GROUP BY w.user_id
)
SELECT u.display_name,
       coalesce(b.net_cents, 0)::bigint,
       coalesce(r.won, 0)::bigint, coalesce(r.lost, 0)::bigint, coalesce(r.pushed, 0)::bigint,
       coalesce(r.open_wagers, 0)::bigint, coalesce(r.handle_cents, 0)::bigint
FROM users u
LEFT JOIN betting b ON b.user_id = u.id
LEFT JOIN record r ON r.user_id = u.id
WHERE coalesce(r.handle_cents, 0) > 0 OR coalesce(b.net_cents, 0) <> 0
ORDER BY coalesce(b.net_cents, 0) DESC, u.display_name`

var _ privateweb.BookPulseReader = (*Readers)(nil)
