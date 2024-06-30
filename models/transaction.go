package models

import "time"

type Transaction struct {
	Amount          float64   `json:"amount"`
	Type            string    `json:"type"`
	Description     string    `json:"description"`
	TransactionDate time.Time `json:"transaction_date"`
}
