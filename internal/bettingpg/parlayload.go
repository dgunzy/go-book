package bettingpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
)

func loadParlayForUpdate(ctx context.Context, tx pgx.Tx, parlayID string) (state, userID string, funding betting.FundingAccountType, stakeCents int64, currency ledger.Currency, err error) {
	var fundingText, currencyText string
	if err = tx.QueryRow(ctx, `
		SELECT state, user_id::text, funding_account_type, stake_cents, currency::text
		FROM parlays WHERE id = $1::uuid FOR UPDATE`, parlayID).
		Scan(&state, &userID, &fundingText, &stakeCents, &currencyText); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", 0, "", fmt.Errorf("%w: parlay %s", betting.ErrNotFound, parlayID)
		}
		return "", "", "", 0, "", fmt.Errorf("load parlay for update: %w", err)
	}
	return state, userID, betting.FundingAccountType(fundingText), stakeCents, ledger.Currency(currencyText), nil
}

// loadParlayForGrading reads the shape GradeParlay needs: the stake, the placed
// price, and every leg's snapshot and result.
func loadParlayForGrading(ctx context.Context, tx pgx.Tx, parlayID string) (betting.Parlay, error) {
	var parlay betting.Parlay
	var fundingText, currencyText, stateText string
	var stakeCents, profitCents int64
	var odds int32
	if err := tx.QueryRow(ctx, `
		SELECT id::text, user_id::text, funding_account_type, stake_cents, currency::text,
		       accepted_american_odds, potential_profit_cents, state
		FROM parlays WHERE id = $1::uuid FOR UPDATE`, parlayID).
		Scan(&parlay.ID, &parlay.UserID, &fundingText, &stakeCents, &currencyText,
			&odds, &profitCents, &stateText); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return betting.Parlay{}, fmt.Errorf("%w: parlay %s", betting.ErrNotFound, parlayID)
		}
		return betting.Parlay{}, fmt.Errorf("load parlay for grading: %w", err)
	}
	currency := ledger.Currency(currencyText)
	parlay.FundingAccountType = betting.FundingAccountType(fundingText)
	parlay.Stake = ledger.Money{Cents: stakeCents, Currency: currency}
	parlay.PotentialProfit = ledger.Money{Cents: profitCents, Currency: currency}
	parlay.AcceptedOdds = ledger.AmericanOdds(odds)
	parlay.State = betting.WagerState(stateText)

	rows, err := tx.Query(ctx, `
		SELECT market_id::text, selection_id::text, accepted_american_odds, accepted_terms,
		       coalesce(result, '')
		FROM parlay_legs WHERE parlay_id = $1::uuid ORDER BY leg_index`, parlayID)
	if err != nil {
		return betting.Parlay{}, fmt.Errorf("load parlay legs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var leg betting.ParlayLeg
		var legOdds int32
		var result string
		if err := rows.Scan(&leg.MarketID, &leg.SelectionID, &legOdds, &leg.AcceptedTerms, &result); err != nil {
			return betting.Parlay{}, fmt.Errorf("scan parlay leg: %w", err)
		}
		leg.AcceptedOdds = ledger.AmericanOdds(legOdds)
		leg.Result = betting.SettlementResult(result)
		parlay.Legs = append(parlay.Legs, leg)
	}
	return parlay, rows.Err()
}

// loadParlay reads a parlay for display, including member name and leg titles.
func loadParlay(ctx context.Context, tx pgx.Tx, parlayID string) (ParlayRow, error) {
	var row ParlayRow
	var fundingText, currencyText, stateText string
	var odds int32
	if err := tx.QueryRow(ctx, `
		SELECT p.id::text, p.user_id::text, coalesce(u.display_name, ''), p.funding_account_type,
		       p.stake_cents, p.currency::text, p.accepted_american_odds, p.potential_profit_cents,
		       p.state, p.placed_at
		FROM parlays p LEFT JOIN users u ON u.id = p.user_id
		WHERE p.id = $1::uuid`, parlayID).
		Scan(&row.ID, &row.UserID, &row.MemberName, &fundingText, &row.StakeCents,
			&currencyText, &odds, &row.PotentialProfit, &stateText, &row.PlacedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ParlayRow{}, fmt.Errorf("%w: parlay %s", betting.ErrNotFound, parlayID)
		}
		return ParlayRow{}, fmt.Errorf("load parlay: %w", err)
	}
	row.FundingAccountType = betting.FundingAccountType(fundingText)
	row.Currency = ledger.Currency(currencyText)
	row.AcceptedOdds = ledger.AmericanOdds(odds)
	row.State = betting.WagerState(stateText)

	legs, err := loadParlayLegs(ctx, tx, parlayID)
	if err != nil {
		return ParlayRow{}, err
	}
	row.Legs = legs
	return row, nil
}

func loadParlayLegs(ctx context.Context, tx pgx.Tx, parlayID string) ([]ParlayLegRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.market_id::text, l.selection_id::text, coalesce(m.title, ''),
		       l.accepted_terms, l.accepted_american_odds, coalesce(l.result, '')
		FROM parlay_legs l LEFT JOIN markets m ON m.id = l.market_id
		WHERE l.parlay_id = $1::uuid ORDER BY l.leg_index`, parlayID)
	if err != nil {
		return nil, fmt.Errorf("load parlay legs: %w", err)
	}
	defer rows.Close()
	legs := make([]ParlayLegRow, 0)
	for rows.Next() {
		var leg ParlayLegRow
		var odds int32
		var result string
		if err := rows.Scan(&leg.MarketID, &leg.SelectionID, &leg.MarketTitle,
			&leg.AcceptedTerms, &odds, &result); err != nil {
			return nil, fmt.Errorf("scan parlay leg row: %w", err)
		}
		leg.AcceptedOdds = ledger.AmericanOdds(odds)
		leg.Result = betting.SettlementResult(result)
		legs = append(legs, leg)
	}
	return legs, rows.Err()
}

func loadParlayByIdempotency(ctx context.Context, tx pgx.Tx, userID, key string) (ParlayRow, error) {
	var id string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM parlays WHERE user_id = $1::uuid AND idempotency_key = $2`,
		userID, key).Scan(&id); err != nil {
		return ParlayRow{}, fmt.Errorf("load parlay by idempotency key: %w", err)
	}
	return loadParlay(ctx, tx, id)
}

// verifyParlayMatches refuses to hand back an unrelated parlay just because an
// idempotency key was reused, which is the same guarantee single wagers give.
func verifyParlayMatches(existing ParlayRow, placed betting.Parlay) error {
	if existing.StakeCents != placed.Stake.Cents ||
		existing.AcceptedOdds != placed.AcceptedOdds ||
		len(existing.Legs) != len(placed.Legs) {
		return betting.ErrIdempotencyConflict
	}
	for index, leg := range placed.Legs {
		if existing.Legs[index].SelectionID != string(leg.SelectionID) {
			return betting.ErrIdempotencyConflict
		}
	}
	return nil
}

// ListParlaysForUser returns a member's parlays, newest first.
func (s Store) ListParlaysForUser(ctx context.Context, userID string) ([]ParlayRow, error) {
	return s.listParlays(ctx, `WHERE p.user_id = $1::uuid`, userID)
}

// ListPendingParlays returns every parlay awaiting a decision, oldest first so
// the review queue works front to back.
func (s Store) ListPendingParlays(ctx context.Context) ([]ParlayRow, error) {
	return s.listParlays(ctx, `WHERE p.state = 'pending'`)
}

func (s Store) listParlays(ctx context.Context, predicate string, args ...any) ([]ParlayRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT p.id::text, p.user_id::text, coalesce(u.display_name, ''), p.funding_account_type,
		       p.stake_cents, p.currency::text, p.accepted_american_odds, p.potential_profit_cents,
		       p.state, p.placed_at
		FROM parlays p LEFT JOIN users u ON u.id = p.user_id
		`+predicate+`
		ORDER BY p.placed_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list parlays: %w", err)
	}
	defer rows.Close()
	result := make([]ParlayRow, 0)
	for rows.Next() {
		var row ParlayRow
		var fundingText, currencyText, stateText string
		var odds int32
		if err := rows.Scan(&row.ID, &row.UserID, &row.MemberName, &fundingText, &row.StakeCents,
			&currencyText, &odds, &row.PotentialProfit, &stateText, &row.PlacedAt); err != nil {
			return nil, fmt.Errorf("scan parlay: %w", err)
		}
		row.FundingAccountType = betting.FundingAccountType(fundingText)
		row.Currency = ledger.Currency(currencyText)
		row.AcceptedOdds = ledger.AmericanOdds(odds)
		row.State = betting.WagerState(stateText)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		legs, err := s.parlayLegsFor(ctx, result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index].Legs = legs
	}
	return result, nil
}

func (s Store) parlayLegsFor(ctx context.Context, parlayID string) ([]ParlayLegRow, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT l.market_id::text, l.selection_id::text, coalesce(m.title, ''),
		       l.accepted_terms, l.accepted_american_odds, coalesce(l.result, '')
		FROM parlay_legs l LEFT JOIN markets m ON m.id = l.market_id
		WHERE l.parlay_id = $1::uuid ORDER BY l.leg_index`, parlayID)
	if err != nil {
		return nil, fmt.Errorf("load parlay legs: %w", err)
	}
	defer rows.Close()
	legs := make([]ParlayLegRow, 0)
	for rows.Next() {
		var leg ParlayLegRow
		var odds int32
		var result string
		if err := rows.Scan(&leg.MarketID, &leg.SelectionID, &leg.MarketTitle,
			&leg.AcceptedTerms, &odds, &result); err != nil {
			return nil, fmt.Errorf("scan parlay leg row: %w", err)
		}
		leg.AcceptedOdds = ledger.AmericanOdds(odds)
		leg.Result = betting.SettlementResult(result)
		legs = append(legs, leg)
	}
	return legs, rows.Err()
}
