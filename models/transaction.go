package models

import "time"

type Transaction struct {
	TransactionID   int       `json:"transaction_id"`
	UserID          int       `json:"user_id"`
	Amount          float64   `json:"amount"`
	Type            string    `json:"type"`
	Description     string    `json:"description"`
	TransactionDate time.Time `json:"transaction_date"`
}
