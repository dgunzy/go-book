package betting

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/events"
)

// NewEventID generates a random version-4 UUID for use as an event or
// aggregate identifier when the caller has not already minted one (for
// example from a database-generated primary key). Commands in this package
// also accept caller-supplied IDs so that settlement stays a pure,
// deterministic function of its inputs.
func NewEventID() (ID, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return ID(fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])), nil
}

func buildEnvelope(id, aggregateID ID, aggregateType string, aggregateVersion int64, eventType events.Type, occurredAt time.Time, payload any) (events.Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return events.Envelope{}, fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	envelope := events.Envelope{
		ID:               string(id),
		AggregateType:    aggregateType,
		AggregateID:      string(aggregateID),
		AggregateVersion: aggregateVersion,
		Type:             eventType,
		Payload:          raw,
		OccurredAt:       occurredAt.UTC(),
	}
	if err := envelope.Validate(); err != nil {
		return events.Envelope{}, fmt.Errorf("build %s envelope: %w", eventType, err)
	}
	return envelope, nil
}

// wagerAcceptedPayload is the WagerAccepted.v1 payload.
type wagerAcceptedPayload struct {
	WagerID              string `json:"wager_id"`
	UserID               string `json:"user_id"`
	MarketID             string `json:"market_id"`
	SelectionID          string `json:"selection_id"`
	StakeCents           int64  `json:"stake_cents"`
	Currency             string `json:"currency"`
	AcceptedAmericanOdds int32  `json:"accepted_american_odds"`
	PotentialProfitCents int64  `json:"potential_profit_cents"`
}

// wagerSettledPayload is the WagerSettled.v1 payload. It intentionally omits
// terms/odds detail already published on WagerAccepted.
type wagerSettledPayload struct {
	WagerID       string `json:"wager_id"`
	UserID        string `json:"user_id"`
	Result        string `json:"result"`
	StakeCents    int64  `json:"stake_cents"`
	ProfitCents   int64  `json:"profit_cents"`
	ReturnedCents int64  `json:"returned_cents"`
	Currency      string `json:"currency"`
}

// marketSettledPayload is the MarketSettled.v1 payload. It carries only
// aggregate counts and totals, never per-user or per-wager data.
type marketSettledPayload struct {
	MarketID           string `json:"market_id"`
	Version            int    `json:"version"`
	WinCount           int    `json:"win_count"`
	LossCount          int    `json:"loss_count"`
	PushCount          int    `json:"push_count"`
	VoidCount          int    `json:"void_count"`
	TotalStakeCents    int64  `json:"total_stake_cents"`
	TotalProfitCents   int64  `json:"total_profit_cents"`
	TotalReturnedCents int64  `json:"total_returned_cents"`
	Currency           string `json:"currency"`
}
