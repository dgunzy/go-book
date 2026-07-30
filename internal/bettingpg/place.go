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
	// PlacedByUserID is the admin placing this wager for the member. Empty
	// when the member placed it themselves.
	PlacedByUserID string
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
	restricted, err := loadRestrictions(ctx, tx, req.MarketID)
	if err != nil {
		return betting.Wager{}, err
	}
	limits, err := loadStakeLimits(ctx, tx, req.MarketID, req.SelectionID, req.UserID)
	if err != nil {
		return betting.Wager{}, err
	}
	stake, err := ledger.NewMoney(req.StakeCents, req.Currency)
	if err != nil {
		return betting.Wager{}, fmt.Errorf("build stake: %w", err)
	}

	wager, err := betting.PlaceWager(betting.PlaceWagerCommand{
		WagerID:                     betting.ID(req.WagerID),
		UserID:                      betting.ID(req.UserID),
		Market:                      market,
		Selection:                   selection,
		Restrictions:                restricted,
		MaxStakeCents:               limits.MarketMax,
		ExistingStakeCents:          limits.MarketExisting,
		SelectionMaxStakeCents:      limits.SelectionMax,
		ExistingSelectionStakeCents: limits.SelectionExisting,
		TotalStakeCapCents:          limits.TotalCap,
		ExistingTotalStakeCents:     limits.TotalExisting,
		MaxPayoutCents:              s.maxPayout(),
		FundingAccountType:          req.FundingAccountType,
		Stake:                       stake,
		IdempotencyKey:              req.IdempotencyKey,
		PlacedBy:                    betting.ID(req.PlacedByUserID),
		Now:                         time.Now(),
	})
	if err != nil {
		return betting.Wager{}, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO wagers (id, user_id, market_id, selection_id, funding_account_type, stake_cents, currency,
			accepted_american_odds, accepted_terms, potential_profit_cents, state, idempotency_key, placed_at,
			placed_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			nullif($14, '')::uuid)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING`,
		wager.ID, wager.UserID, wager.MarketID, wager.SelectionID, string(wager.FundingAccountType),
		wager.Stake.Cents, string(wager.Stake.Currency), int32(wager.AcceptedOdds), wager.AcceptedTerms,
		wager.PotentialProfit.Cents, string(wager.State), wager.IdempotencyKey, wager.PlacedAt,
		string(wager.PlacedBy))
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

// stakeLimits is what bounds this member on this market and on this side of
// it, together with what they already have riding on each.
type stakeLimits struct {
	MarketMax         int64
	MarketExisting    int64
	SelectionMax      int64
	SelectionExisting int64
	// TotalCap bounds every member together on this side, and TotalExisting is
	// what the whole book already has on it.
	TotalCap      int64
	TotalExisting int64
}

// loadStakeLimits reads both caps and, for whichever are set, how much this
// member already has against them. Pending wagers count: money waiting for
// approval is money they are trying to have on.
func loadStakeLimits(ctx context.Context, tx pgx.Tx, marketID, selectionID, userID string) (stakeLimits, error) {
	var limits stakeLimits
	var marketMax, selectionMax, totalCap *int64
	// FOR UPDATE OF s locks this side for the rest of the transaction. The
	// total cap is a sum over every member's wagers, so without the lock two
	// placements arriving together would both read the same total, both find
	// room, and both commit — putting the book over the cap it was promised.
	// Taking the lock before the sum is what makes the cap real rather than
	// advisory.
	if err := tx.QueryRow(ctx, `
		SELECT m.max_stake_cents, s.max_stake_cents, s.total_stake_cap_cents
		FROM markets m
		JOIN selections s ON s.market_id = m.id AND s.id = $2::uuid
		WHERE m.id = $1::uuid
		FOR UPDATE OF s`, marketID, selectionID).Scan(&marketMax, &selectionMax, &totalCap); err != nil {
		return stakeLimits{}, fmt.Errorf("load stake limits: %w", err)
	}
	if marketMax != nil {
		limits.MarketMax = *marketMax
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(stake_cents), 0) FROM wagers
			WHERE market_id = $1::uuid AND user_id = $2::uuid AND state IN ('pending', 'accepted')`,
			marketID, userID).Scan(&limits.MarketExisting); err != nil {
			return stakeLimits{}, fmt.Errorf("load member stake on market: %w", err)
		}
	}
	if selectionMax != nil {
		limits.SelectionMax = *selectionMax
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(stake_cents), 0) FROM wagers
			WHERE selection_id = $1::uuid AND user_id = $2::uuid AND state IN ('pending', 'accepted')`,
			selectionID, userID).Scan(&limits.SelectionExisting); err != nil {
			return stakeLimits{}, fmt.Errorf("load member stake on selection: %w", err)
		}
	}
	if totalCap != nil {
		limits.TotalCap = *totalCap
		// Every member, not just this one. Pending counts for the same reason
		// it counts against a member's own limit: it is money trying to get on,
		// and approving it later must not be what breaches the cap.
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(stake_cents), 0) FROM wagers
			WHERE selection_id = $1::uuid AND state IN ('pending', 'accepted')`,
			selectionID).Scan(&limits.TotalExisting); err != nil {
			return stakeLimits{}, fmt.Errorf("load book stake on selection: %w", err)
		}
	}
	return limits, nil
}
