package events

import "fmt"

type OutboxState string

const (
	OutboxPending    OutboxState = "pending"
	OutboxProcessing OutboxState = "processing"
	OutboxCompleted  OutboxState = "completed"
	OutboxFailed     OutboxState = "failed"
)

func (s OutboxState) Validate() error {
	switch s {
	case OutboxPending, OutboxProcessing, OutboxCompleted, OutboxFailed:
		return nil
	default:
		return fmt.Errorf("unknown outbox state %q", s)
	}
}

// CanTransitionTo describes dispatcher lifecycle changes. A processing event may
// return to pending after a bounded retry; completed and terminally failed events
// cannot be reused.
func (s OutboxState) CanTransitionTo(next OutboxState) bool {
	if s.Validate() != nil || next.Validate() != nil || s == next {
		return false
	}
	switch s {
	case OutboxPending:
		return next == OutboxProcessing || next == OutboxFailed
	case OutboxProcessing:
		return next == OutboxPending || next == OutboxCompleted || next == OutboxFailed
	case OutboxCompleted, OutboxFailed:
		return false
	default:
		return false
	}
}
