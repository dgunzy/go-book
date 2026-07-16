package ledger

import (
	"errors"
	"math"
	"testing"
)

func TestParseCurrency(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		ok    bool
	}{{"CAD", true}, {"USD", true}, {"cad", false}, {"CA", false}, {"C4D", false}} {
		_, err := ParseCurrency(test.value)
		if (err == nil) != test.ok {
			t.Errorf("ParseCurrency(%q) error = %v, want ok %v", test.value, err, test.ok)
		}
	}
}

func TestMoneyAddAndNegate(t *testing.T) {
	t.Parallel()
	sum, err := (Money{Cents: 125, Currency: CAD}).Add(Money{Cents: -25, Currency: CAD})
	if err != nil || sum.Cents != 100 {
		t.Fatalf("Add() = %+v, %v", sum, err)
	}
	negated, err := sum.Negate()
	if err != nil || negated.Cents != -100 {
		t.Fatalf("Negate() = %+v, %v", negated, err)
	}
	if _, err := sum.Add(Money{Cents: 1, Currency: "USD"}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch error = %v", err)
	}
	if _, err := (Money{Cents: math.MaxInt64, Currency: CAD}).Add(Money{Cents: 1, Currency: CAD}); !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	if _, err := (Money{Cents: math.MinInt64, Currency: CAD}).Negate(); !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("negate overflow error = %v", err)
	}
}
