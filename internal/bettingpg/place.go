package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
)

// PlaceWagerRequest is the caller-supplied input to place a new pending
// wager. WagerID and IdempotencyKey are supplied by the caller (typically a
// client-generated UUID and an idempotency header) so retried HTTP requests
// are safe.
type PlaceWagerRequest struct {
	WagerID            string
	UserID             string
	MarketID           string
	SelectionID        string
	FundingAccountType betting.FundingAccountType
	StakeCents         int64
	Currency           ledger.Currency
	IdempotencyKey     string
}

// PlaceWager loads the market, selection, and restricted-user list, runs the
// pure betting.PlaceWager command, and persists the resulting pending wager.
// Insert uses ON CONFLICT (user_id, idempotency_key) DO NOTHING; on conflict
// the existing row must describe the same market, selection, stake, and odds
// snapshot or PlaceWager returns ErrIdempotencyConflict.
func (s Store) PlaceWager(ctx context.Context, req PlaceWagerRequest) (betting.Wager, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return betting.Wager{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	market, err := loadMarket(ctx, tx, req.MarketID)
	if err != nil {
		return betting.Wager{}, err
	}
	selection, err := loadSelection(ctx, tx, req.MarketID, req.SelectionID)
	if err != nil {
		return betting.Wager{}, err
	}
	restricted, err := loadRestrictedUsers(ctx, tx, req.MarketID)
	if err != nil {
		return betting.Wager{}, err
	}
	stake, err := ledger.NewMoney(req.StakeCents, req.Currency)
	if err != nil {
		return betting.Wager{}, fmt.Errorf("build stake: %w", err)
	}

	wager, err := betting.PlaceWager(betting.PlaceWagerCommand{
		WagerID:            betting.ID(req.WagerID),
		UserID:             betting.ID(req.UserID),
		Market:             market,
		Selection:          selection,
		RestrictedUsers:    restricted,
		FundingAccountType: req.FundingAccountType,
		Stake:              stake,
		IdempotencyKey:     req.IdempotencyKey,
		Now:                time.Now(),
	})
	if err != nil {
		return betting.Wager{}, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO wagers (id, user_id, market_id, selection_id, funding_account_type, stake_cents, currency,
			accepted_american_odds, accepted_terms, potential_profit_cents, state, idempotency_key, placed_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING`,
		wager.ID, wager.UserID, wager.MarketID, wager.SelectionID, string(wager.FundingAccountType),
		wager.Stake.Cents, string(wager.Stake.Currency), int32(wager.AcceptedOdds), wager.AcceptedTerms,
		wager.PotentialProfit.Cents, string(wager.State), wager.IdempotencyKey, wager.PlacedAt)
	if err != nil {
		return betting.Wager{}, fmt.Errorf("insert wager: %w", err)
	}

	stored, err := loadWagerByUserIdempotencyKey(ctx, tx, string(wager.UserID), wager.IdempotencyKey)
	if err != nil {
		return betting.Wager{}, err
	}
	if stored.MarketID != wager.MarketID || stored.SelectionID != wager.SelectionID ||
		stored.Stake != wager.Stake || stored.AcceptedOdds != wager.AcceptedOdds ||
		stored.FundingAccountType != wager.FundingAccountType {
		return betting.Wager{}, fmt.Errorf("%w: wager idempotency key %q", ErrIdempotencyConflict, wager.IdempotencyKey)
	}

	if err := tx.Commit(ctx); err != nil {
		return betting.Wager{}, fmt.Errorf("commit place wager: %w", err)
	}
	return stored, nil
}

func loadWagerByUserIdempotencyKey(ctx context.Context, tx pgx.Tx, userID, idempotencyKey string) (betting.Wager, error) {
	wager, err := wagerRow(tx.QueryRow(ctx, `
		SELECT `+wagerColumns+` FROM wagers WHERE user_id = $1::uuid AND idempotency_key = $2`, userID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return betting.Wager{}, fmt.Errorf("%w: placed wager was not found after insert", betting.ErrNotFound)
	}
	if err != nil {
		return betting.Wager{}, fmt.Errorf("load placed wager: %w", err)
	}
	return wager, nil
}
