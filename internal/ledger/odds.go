package ledger

import (
	"errors"
	"math"
)

var (
	ErrInvalidAmericanOdds = errors.New("American odds must be at least +100 or at most -100")
	ErrInvalidStake        = errors.New("stake must be greater than zero")
)

// AmericanOdds excludes the ambiguous range between -100 and +100.
type AmericanOdds int32

func NewAmericanOdds(value int32) (AmericanOdds, error) {
	odds := AmericanOdds(value)
	if err := odds.Validate(); err != nil {
		return 0, err
	}
	return odds, nil
}

func (o AmericanOdds) Validate() error {
	if o > -100 && o < 100 {
		return ErrInvalidAmericanOdds
	}
	return nil
}

// Profit returns net winnings, rounded to the nearest cent with exact half cents
// rounded up. The algorithm uses only integer arithmetic and is reproducible from
// the accepted stake and odds snapshot.
func (o AmericanOdds) Profit(stake Money) (Money, error) {
	if err := o.Validate(); err != nil {
		return Money{}, err
	}
	if err := stake.Validate(); err != nil {
		return Money{}, err
	}
	if stake.Cents <= 0 {
		return Money{}, ErrInvalidStake
	}

	var numerator, denominator int64
	if o > 0 {
		numerator, denominator = int64(o), 100
	} else {
		numerator, denominator = 100, -int64(o)
	}

	if stake.Cents > math.MaxInt64/numerator {
		return Money{}, ErrAmountOverflow
	}
	product := stake.Cents * numerator
	profit := product / denominator
	if product%denominator >= (denominator+1)/2 {
		if profit == math.MaxInt64 {
			return Money{}, ErrAmountOverflow
		}
		profit++
	}
	return Money{Cents: profit, Currency: stake.Currency}, nil
}

// Return returns the total amount paid back for a winning wager: stake plus net
// profit. Loss, push, and void behavior belong to settlement state handling.
func (o AmericanOdds) Return(stake Money) (Money, error) {
	profit, err := o.Profit(stake)
	if err != nil {
		return Money{}, err
	}
	return stake.Add(profit)
}
