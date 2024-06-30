package models

import "time"

type UserBet struct {
	Amount         float64   `json:"amount"`
	PlacedAt       time.Time `json:"placed_at"`
	Result         string    `json:"result"` // 'win', 'loss', or empty if not graded yet
	BetDescription string    `json:"bet_description"`
	Odds           float64   `json:"odds"`
	BetId          int       `json:"bet_id"`
	UserID         int       `json:"user_id"`
	Approved       bool      `json:"approved"`
}
