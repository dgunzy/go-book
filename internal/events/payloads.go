package events

// MatchResultVerifiedPayload is the MatchResultVerified.v1 payload contract
// shared between internal/competition (the publisher) and internal/bettingpg
// (the consumer that settles match-type betting markets from it). Outcome is
// "side_win" or "tie"; WinningSideID is empty for a tie.
type MatchResultVerifiedPayload struct {
	MatchID            string `json:"match_id"`
	CompetitionEventID string `json:"competition_event_id"`
	VerificationID     string `json:"verification_id"`
	Outcome            string `json:"outcome"`
	WinningSideID      string `json:"winning_side_id,omitempty"`
	Score              string `json:"score"`
}

// PlayerUserLinkChangedPayload records the one-to-one association between a
// historical/competition player and an authenticated member. Consumers can
// rebuild identity-facing player views without using audit history as a feed.
type PlayerUserLinkChangedPayload struct {
	PlayerID string `json:"player_id"`
	UserID   string `json:"user_id"`
}

// CompetitionSetupDeletedPayload records the deliberate removal of an unused
// event, team, or match. Records with betting, result, media, statistics, or
// imported history are protected and never produce this event.
type CompetitionSetupDeletedPayload struct {
	RecordType string `json:"record_type"`
	RecordID   string `json:"record_id"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
}
