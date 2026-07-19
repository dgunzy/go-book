package bettingpg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/dgunzy/go-book/internal/events"
)

// matchSettlementActor is the system actor recorded on markets and ledger
// transactions this consumer settles. It intentionally is not a UUID:
// insertLedgerTransaction leaves actor_user_id NULL for non-UUID actors,
// which is how the ledger distinguishes automated settlement from an
// admin's own action.
const matchSettlementActor = "system:settlement-worker"

// MatchSettlementConsumer drives betting settlement from verified match
// results published by internal/competition. It implements events.Consumer.
type MatchSettlementConsumer struct {
	Store  *Store
	Logger *slog.Logger
}

func (c *MatchSettlementConsumer) Name() string { return "betting-match-settlement" }

func (c *MatchSettlementConsumer) Handles(t events.Type) bool { return t == events.MatchResultVerified }

// Handle parses the MatchResultVerified payload and settles every
// automatically-gradeable market for the match. Any markets skipped because
// their selections lack a recognized semantic_result_key are logged (by
// market and match ID only, never wager or user data) for manual follow-up
// rather than treated as an error, since SettleMatchMarkets already made a
// deliberate, correct decision not to guess. A returned error is unwrapped
// so the dispatcher retries the whole event, which is safe because
// SettleMatchMarkets and SettleMarket are idempotent per market.
func (c *MatchSettlementConsumer) Handle(ctx context.Context, envelope events.Envelope) error {
	if c.Store == nil {
		return fmt.Errorf("betting-match-settlement: store is required")
	}
	var payload events.MatchResultVerifiedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("parse MatchResultVerified payload: %w", err)
	}

	report, err := c.Store.SettleMatchMarkets(ctx, payload.MatchID, payload.Outcome, payload.WinningSideID, payload.VerificationID, matchSettlementActor)
	if err != nil {
		return err
	}
	if len(report.Skipped) > 0 {
		c.logger().Warn("betting markets skipped for manual settlement: unrecognized semantic_result_key",
			"match_id", payload.MatchID, "skipped_market_ids", report.Skipped)
	}
	return nil
}

func (c *MatchSettlementConsumer) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
