package models

import "time"

type UserBet struct {
	UserBetID int       `json:"user_bet_id"`
	UserID    int       `json:"user_id"`
	BetID     int       `json:"bet_id"`
	OutcomeID int       `json:"outcome_id"`
	Amount    float64   `json:"amount"`
	PlacedAt  time.Time `json:"placed_at"`
	Result    string    `json:"result"` // 'win', 'loss', or empty if not graded yet
}
