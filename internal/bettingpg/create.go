package bettingpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CreateMarketSelection is one bettable outcome supplied with a new market.
type CreateMarketSelection struct {
	Key                 string
	DisplayTerms        string
	OfferedAmericanOdds int32
	SemanticResultKey   string
}

// CreateMarketRequest inserts a draft market with its selections in one
// transaction. MarketID is a caller-supplied UUID (generated server-side per
// form render) that doubles as the idempotency token: a retried request with
// the same MarketID is a no-op returning the stored market, and a reused
// MarketID describing a different market returns ErrIdempotencyConflict.
// ActorUserID is the creating admin and is recorded as markets.created_by.
type CreateMarketRequest struct {
	MarketID    string
	Type        betting.MarketType
	MatchID     string
	Title       string
	Currency    ledger.Currency
	OpensAt     time.Time
	ClosesAt    time.Time
	Selections  []CreateMarketSelection
	ActorUserID string
	// DynamicPricing enables exposure-based line movement for this market;
	// PricingLiquidityCents is the sensitivity "b" (larger = smaller moves)
	// and must be positive when DynamicPricing is set.
	DynamicPricing        bool
	PricingLiquidityCents int64
}

// maxSelectionsPerMarket bounds the selection list on one market so a
// malformed request cannot insert an unbounded number of rows.
const maxSelectionsPerMarket = 20

// DefaultPricingLiquidityCents is the line-movement sensitivity ("b") applied
// when dynamic pricing is enabled without an explicit value: $500.
const DefaultPricingLiquidityCents = 50_000

// CreateMarket validates the market and its selections through the domain
// rules in internal/betting, then persists them atomically together with a
// MarketCreated outbox event. The market is created in draft state; wagers
// are only possible after OpenMarket.
func (s Store) CreateMarket(ctx context.Context, req CreateMarketRequest) (betting.Market, error) {
	if !isUUID(req.ActorUserID) {
		return betting.Market{}, fmt.Errorf("%w: market creation requires an acting admin user", betting.ErrUnauthorized)
	}
	if !isUUID(req.MarketID) {
		return betting.Market{}, fmt.Errorf("%w: market creation requires a UUID market ID", betting.ErrInvalid)
	}
	market := betting.Market{
		ID:       betting.ID(req.MarketID),
		Type:     req.Type,
		MatchID:  betting.ID(strings.TrimSpace(req.MatchID)),
		Title:    strings.TrimSpace(req.Title),
		State:    betting.MarketDraft,
		Currency: req.Currency,
		OpensAt:  req.OpensAt,
		ClosesAt: req.ClosesAt,
	}
	if err := market.Validate(); err != nil {
		return betting.Market{}, err
	}
	if len(req.Selections) == 0 || len(req.Selections) > maxSelectionsPerMarket {
		return betting.Market{}, fmt.Errorf("%w: a market requires between 1 and %d selections", betting.ErrInvalid, maxSelectionsPerMarket)
	}
	if req.DynamicPricing {
		if req.PricingLiquidityCents <= 0 {
			// Default the line-movement sensitivity so admins never have to
			// enter it: $500 moves the line a moderate amount per bet.
			req.PricingLiquidityCents = DefaultPricingLiquidityCents
		}
		if len(req.Selections) < 2 {
			return betting.Market{}, fmt.Errorf("%w: dynamic pricing requires at least two selections", betting.ErrInvalid)
		}
	}
	selections := make([]betting.Selection, 0, len(req.Selections))
	seenKeys := make(map[string]bool, len(req.Selections))
	for _, request := range req.Selections {
		selectionID, err := betting.NewEventID()
		if err != nil {
			return betting.Market{}, err
		}
		selection := betting.Selection{
			ID:                  selectionID,
			MarketID:            market.ID,
			Key:                 strings.TrimSpace(request.Key),
			DisplayTerms:        strings.TrimSpace(request.DisplayTerms),
			OfferedAmericanOdds: ledger.AmericanOdds(request.OfferedAmericanOdds),
			SemanticResultKey:   strings.TrimSpace(request.SemanticResultKey),
			Active:              true,
		}
		if err := selection.Validate(); err != nil {
			return betting.Market{}, err
		}
		if seenKeys[selection.Key] {
			return betting.Market{}, fmt.Errorf("%w: selection key %q is duplicated", betting.ErrInvalid, selection.Key)
		}
		seenKeys[selection.Key] = true
		selections = append(selections, selection)
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return betting.Market{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pricingLiquidity any
	if req.DynamicPricing {
		pricingLiquidity = req.PricingLiquidityCents
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO markets (id, market_type, match_id, title, state, currency, opens_at, closes_at, created_by, dynamic_pricing, pricing_liquidity_cents)
		VALUES ($1::uuid, $2, nullif($3, '')::uuid, $4, 'draft', $5, $6, $7, $8::uuid, $9, $10)
		ON CONFLICT (id) DO NOTHING
		RETURNING id::text`,
		string(market.ID), string(market.Type), string(market.MatchID), market.Title,
		string(market.Currency), nullableTime(market.OpensAt), market.ClosesAt.UTC(), req.ActorUserID,
		req.DynamicPricing, pricingLiquidity).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := loadMarket(ctx, tx, string(market.ID))
		if err != nil {
			return betting.Market{}, err
		}
		if existing.Type != market.Type || existing.MatchID != market.MatchID ||
			existing.Title != market.Title || existing.Currency != market.Currency ||
			!existing.ClosesAt.Equal(market.ClosesAt.UTC()) {
			return betting.Market{}, fmt.Errorf("%w: market %s already exists with different terms", ErrIdempotencyConflict, market.ID)
		}
		if err := tx.Commit(ctx); err != nil {
			return betting.Market{}, fmt.Errorf("commit idempotent market creation: %w", err)
		}
		return existing, nil
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "markets_one_active_match_market_idx" {
			return betting.Market{}, ErrMatchMarketExists
		}
		return betting.Market{}, fmt.Errorf("insert market %s: %w", market.ID, err)
	}
	if market.Type == betting.MarketMatch {
		if err := validateMatchMarketMapping(ctx, tx, string(market.ID), string(market.MatchID), selections); err != nil {
			return betting.Market{}, err
		}
	}

	for _, selection := range selections {
		// opening_american_odds is seeded equal to the offered line and stays
		// fixed as the prior the pricing engine reprices from.
		if _, err := tx.Exec(ctx, `
			INSERT INTO selections (id, market_id, selection_key, display_terms, offered_american_odds, opening_american_odds, semantic_result_key, active)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $5, nullif($6, ''), true)`,
			string(selection.ID), string(selection.MarketID), selection.Key, selection.DisplayTerms,
			int32(selection.OfferedAmericanOdds), selection.SemanticResultKey); err != nil {
			return betting.Market{}, fmt.Errorf("insert selection %q for market %s: %w", selection.Key, market.ID, err)
		}
	}

	if err := publishMarketEvent(ctx, tx, events.MarketCreated, string(market.ID), map[string]string{
		"market_id": string(market.ID), "market_type": string(market.Type),
		"currency": string(market.Currency), "actor_user_id": req.ActorUserID,
	}); err != nil {
		return betting.Market{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return betting.Market{}, fmt.Errorf("commit create market: %w", err)
	}
	return market, nil
}

func validateMatchMarketMapping(ctx context.Context, tx pgx.Tx, marketID, matchID string, selections []betting.Selection) error {
	var side1ID, side2ID string
	err := tx.QueryRow(ctx, `
		SELECT side1.id::text, side2.id::text
		FROM matches m
		JOIN match_sides side1 ON side1.match_id = m.id AND side1.side_number = 1
		JOIN match_sides side2 ON side2.match_id = m.id AND side2.side_number = 2
		WHERE m.id = $1::uuid
		  AND m.state IN ('scheduled', 'open')
		  AND NOT EXISTS (
			SELECT 1 FROM markets existing
			WHERE existing.match_id = m.id
			  AND existing.id <> $2::uuid
			  AND existing.state NOT IN ('voided', 'cancelled')
		  )
		FOR SHARE OF m, side1, side2`, matchID, marketID).Scan(&side1ID, &side2ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: choose an open match without an existing market", betting.ErrInvalid)
	}
	if err != nil {
		return fmt.Errorf("validate match market %s: %w", marketID, err)
	}
	if len(selections) != 2 {
		return fmt.Errorf("%w: a match winner market requires exactly two side selections", betting.ErrInvalid)
	}
	want := map[string]bool{"side:" + side1ID: false, "side:" + side2ID: false}
	for _, selection := range selections {
		if _, ok := want[selection.SemanticResultKey]; !ok || want[selection.SemanticResultKey] {
			return fmt.Errorf("%w: match selections must map exactly once to each match side", betting.ErrInvalid)
		}
		want[selection.SemanticResultKey] = true
	}
	return nil
}

// OpenMarket transitions a draft market to open, mirroring CloseMarket. It
// is idempotent: a market already open is a no-op. Actor is the admin user
// performing the action and is recorded on the MarketOpened event.
func (s Store) OpenMarket(ctx context.Context, marketID, actor string) error {
	if strings.TrimSpace(actor) == "" {
		return fmt.Errorf("%w: opening a market requires an acting user", betting.ErrUnauthorized)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	market, err := loadMarketForUpdate(ctx, tx, marketID)
	if err != nil {
		return err
	}
	if market.State == betting.MarketOpen {
		return tx.Commit(ctx)
	}
	if !market.State.CanTransitionTo(betting.MarketOpen) {
		return fmt.Errorf("%w: market %s state %s cannot open", ErrMarketNotOpenable, marketID, market.State)
	}

	tag, err := tx.Exec(ctx, `UPDATE markets SET state = 'open', updated_at = now() WHERE id = $1::uuid AND state = 'draft'`, marketID)
	if err != nil {
		return fmt.Errorf("open market %s: %w", marketID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("open market %s: market was not draft", marketID)
	}

	if err := publishMarketEvent(ctx, tx, events.MarketOpened, marketID, map[string]string{
		"market_id": marketID, "actor_user_id": actor,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func publishMarketEvent(ctx context.Context, tx pgx.Tx, eventType events.Type, marketID string, payload map[string]string) error {
	eventID, err := betting.NewEventID()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	envelope := events.Envelope{
		ID:               string(eventID),
		AggregateType:    "market",
		AggregateID:      marketID,
		AggregateVersion: 1,
		Type:             eventType,
		Payload:          encoded,
		OccurredAt:       time.Now().UTC(),
	}
	return eventspg.Publish(ctx, tx, envelope, maxOutboxAttempts)
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
