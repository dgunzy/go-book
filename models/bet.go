package models

type Bet struct {
	BetID          int     `json:"bet_id"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	OddsMultiplier float64 `json:"odds_multiplier"`
	Status         string  `json:"status"`
	CreatedBy      int     `json:"created_by"`
	CreatedAt      string  `json:"created_at"`
}
