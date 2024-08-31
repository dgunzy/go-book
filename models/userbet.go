package models

import "time"

type UserBet struct {
	UserBetID      int       `json:"user_bet_id"`
	Amount         float64   `json:"amount"`
	PlacedAt       time.Time `json:"placed_at"`
	Result         string    `json:"result"`
	BetDescription string    `json:"bet_description"`
	Odds           float64   `json:"odds"`
	UserID         int       `json:"user_id"`
	Approved       bool      `json:"approved"`
}
