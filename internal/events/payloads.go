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
