// Package ledger contains financial value types and validation that are independent
// of storage and transport concerns.
package ledger

import (
	"errors"
	"fmt"
	"math"
)

var (
	ErrInvalidCurrency  = errors.New("currency must be a three-letter uppercase ISO-style code")
	ErrCurrencyMismatch = errors.New("money currencies do not match")
	ErrAmountOverflow   = errors.New("money amount overflows int64 cents")
)

// Currency is a validated three-letter uppercase currency code.
type Currency string

const CAD Currency = "CAD"

func ParseCurrency(value string) (Currency, error) {
	if len(value) != 3 {
		return "", ErrInvalidCurrency
	}
	for i := range value {
		if value[i] < 'A' || value[i] > 'Z' {
			return "", ErrInvalidCurrency
		}
	}
	return Currency(value), nil
}

func (c Currency) Validate() error {
	_, err := ParseCurrency(string(c))
	return err
}

// Money stores a signed number of the smallest currency unit. Cents is never a
// floating-point value.
type Money struct {
	Cents    int64
	Currency Currency
}

func NewMoney(cents int64, currency Currency) (Money, error) {
	if err := currency.Validate(); err != nil {
		return Money{}, err
	}
	return Money{Cents: cents, Currency: currency}, nil
}

func (m Money) Validate() error {
	return m.Currency.Validate()
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if err := other.Validate(); err != nil {
		return Money{}, err
	}
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	if (other.Cents > 0 && m.Cents > math.MaxInt64-other.Cents) ||
		(other.Cents < 0 && m.Cents < math.MinInt64-other.Cents) {
		return Money{}, ErrAmountOverflow
	}
	return Money{Cents: m.Cents + other.Cents, Currency: m.Currency}, nil
}

func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.Cents == math.MinInt64 {
		return Money{}, ErrAmountOverflow
	}
	return Money{Cents: -m.Cents, Currency: m.Currency}, nil
}

func (m Money) String() string {
	return fmt.Sprintf("%s %d cents", m.Currency, m.Cents)
}
