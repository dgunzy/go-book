// Package events defines durable event contracts without coupling domain code to a
// dispatcher or database driver.
package events

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidEnvelope = errors.New("invalid event envelope")
	eventTypePattern   = regexp.MustCompile(`^[A-Z][A-Za-z0-9]+\.v[1-9][0-9]*$`)
)

type Type string

const (
	MatchResultSubmitted      Type = "MatchResultSubmitted.v1"
	MatchResultDisputed       Type = "MatchResultDisputed.v1"
	MatchResultVerified       Type = "MatchResultVerified.v1"
	MatchResultCorrected      Type = "MatchResultCorrected.v1"
	PlayerStatisticsProjected Type = "PlayerStatisticsProjected.v1"
	PlayerLinkedToUser        Type = "PlayerLinkedToUser.v1"
	PlayerUnlinkedFromUser    Type = "PlayerUnlinkedFromUser.v1"
	MarketCreated             Type = "MarketCreated.v1"
	MarketOpened              Type = "MarketOpened.v1"
	MarketClosed              Type = "MarketClosed.v1"
	MarketSettlementRequested Type = "MarketSettlementRequested.v1"
	MarketSettled             Type = "MarketSettled.v1"
	WagerAccepted             Type = "WagerAccepted.v1"
	WagerSettled              Type = "WagerSettled.v1"
	LedgerTransactionPosted   Type = "LedgerTransactionPosted.v1"
	MediaPublished            Type = "MediaPublished.v1"
)

func (t Type) Validate() error {
	if !eventTypePattern.MatchString(string(t)) {
		return fmt.Errorf("%w: event type %q must include a positive version", ErrInvalidEnvelope, t)
	}
	return nil
}

// Envelope is the versioned, serializable contract written alongside a domain
// change. Payload must be a JSON object and must not contain transport metadata.
type Envelope struct {
	ID               string          `json:"id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	Type             Type            `json:"type"`
	Payload          json.RawMessage `json:"payload"`
	OccurredAt       time.Time       `json:"occurred_at"`
	CorrelationID    string          `json:"correlation_id,omitempty"`
	CausationID      string          `json:"causation_id,omitempty"`
}

func (e Envelope) Validate() error {
	if !validUUID(e.ID) || !validUUID(e.AggregateID) {
		return fmt.Errorf("%w: event and aggregate IDs must be UUIDs", ErrInvalidEnvelope)
	}
	if strings.TrimSpace(e.AggregateType) == "" || len(e.AggregateType) > 80 {
		return fmt.Errorf("%w: aggregate type is required and limited to 80 characters", ErrInvalidEnvelope)
	}
	if e.AggregateVersion <= 0 {
		return fmt.Errorf("%w: aggregate version must be positive", ErrInvalidEnvelope)
	}
	if err := e.Type.Validate(); err != nil {
		return err
	}
	if e.OccurredAt.IsZero() || e.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("%w: occurrence time must be a non-zero UTC timestamp", ErrInvalidEnvelope)
	}
	if e.CorrelationID != "" && !validUUID(e.CorrelationID) {
		return fmt.Errorf("%w: correlation ID must be a UUID", ErrInvalidEnvelope)
	}
	if e.CausationID != "" && !validUUID(e.CausationID) {
		return fmt.Errorf("%w: causation ID must be a UUID", ErrInvalidEnvelope)
	}
	if !json.Valid(e.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrInvalidEnvelope)
	}
	trimmed := bytes.TrimSpace(e.Payload)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidEnvelope)
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	_, err := hex.DecodeString(compact)
	return err == nil
}
