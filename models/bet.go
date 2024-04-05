package models

import "time"

type Bet struct {
	BetID          int       `json:"bet_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	OddsMultiplier float64   `json:"odds_multiplier"`
	Status         string    `json:"status"`
	Category       string    `json:"category"`
	CreatedBy      int       `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiryTime     time.Time `json:"expiry_time`
}
