package models

import "time"

type Bet struct {
	BetID          int
	Title          string
	Description    string
	OddsMultiplier float64
	Status         string
	Category       string
	CreatedBy      int
	CreatedAt      time.Time
	ExpiryTime     time.Time
}
