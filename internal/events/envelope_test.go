package events

import (
	"errors"
	"testing"
	"time"
)

const (
	eventID     = "0b7f7334-8dc5-43db-a3e8-69cbeb126dea"
	aggregateID = "122051d4-cec9-4c76-9c2b-b5bb42f35a79"
)

func validEnvelope() Envelope {
	return Envelope{
		ID:               eventID,
		AggregateType:    "match",
		AggregateID:      aggregateID,
		AggregateVersion: 1,
		Type:             MatchResultVerified,
		Payload:          []byte(`{"match_id":"122051d4-cec9-4c76-9c2b-b5bb42f35a79"}`),
		OccurredAt:       time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
}

func TestEnvelopeValidate(t *testing.T) {
	t.Parallel()
	if err := validEnvelope().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEnvelopeRejectsMalformedContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*Envelope)
	}{
		{"event id", func(e *Envelope) { e.ID = "not-a-uuid" }},
		{"aggregate version", func(e *Envelope) { e.AggregateVersion = 0 }},
		{"event type", func(e *Envelope) { e.Type = "MatchResultVerified" }},
		{"array payload", func(e *Envelope) { e.Payload = []byte(`[]`) }},
		{"invalid payload", func(e *Envelope) { e.Payload = []byte(`{`) }},
		{"non-UTC time", func(e *Envelope) { e.OccurredAt = e.OccurredAt.In(time.FixedZone("ADT", -3*60*60)) }},
		{"correlation id", func(e *Envelope) { e.CorrelationID = "bad" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			e := validEnvelope()
			test.change(&e)
			if err := e.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("Validate() error = %v, want ErrInvalidEnvelope", err)
			}
		})
	}
}
