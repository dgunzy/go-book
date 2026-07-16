package competition

import "time"

const (
	EventMatchResultSubmitted = "MatchResultSubmitted.v1"
	EventMatchResultDisputed  = "MatchResultDisputed.v1"
	EventMatchResultVerified  = "MatchResultVerified.v1"
	EventMatchResultCorrected = "MatchResultCorrected.v1"
)

// DomainEvent is a transport-neutral fact emitted by a successful aggregate
// command. The application layer is responsible for assigning an outbox event
// ID and persisting this fact in the same transaction as the aggregate.
type DomainEvent struct {
	Type                   string
	MatchID                ID
	CompetitionEventID     ID
	SubmissionID           ID
	VerificationID         ID
	PreviousVerificationID ID
	ActorUserID            ID
	Result                 Result
	Reason                 string
	OccurredAt             time.Time
}
