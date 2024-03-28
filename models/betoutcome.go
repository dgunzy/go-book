package models

type BetOutcome struct {
	OutcomeID   int     `json:"outcome_id"`
	BetID       int     `json:"bet_id"`
	Description string  `json:"description"`
	Odds        float64 `json:"odds"`
}
