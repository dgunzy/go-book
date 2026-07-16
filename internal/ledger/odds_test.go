package ledger

import (
	"errors"
	"math"
	"testing"
)

func TestAmericanOddsProfit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stake int64
		odds  AmericanOdds
		want  int64
	}{
		{"positive", 10_00, 150, 15_00},
		{"negative", 10_00, -200, 5_00},
		{"even positive", 10_00, 100, 10_00},
		{"even negative", 10_00, -100, 10_00},
		{"half cent rounds up", 1, 150, 2},
		{"below half cent rounds down", 1, -300, 0},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := test.odds.Profit(Money{Cents: test.stake, Currency: CAD})
			if err != nil || got.Cents != test.want {
				t.Fatalf("Profit() = %+v, %v; want %d cents", got, err, test.want)
			}
		})
	}
}

func TestAmericanOddsReturn(t *testing.T) {
	t.Parallel()
	got, err := (AmericanOdds(-110)).Return(Money{Cents: 11_00, Currency: CAD})
	if err != nil || got.Cents != 21_00 {
		t.Fatalf("Return() = %+v, %v; want 2100 cents", got, err)
	}
}

func TestAmericanOddsRejectsInvalidInputsAndOverflow(t *testing.T) {
	t.Parallel()
	if _, err := NewAmericanOdds(99); !errors.Is(err, ErrInvalidAmericanOdds) {
		t.Fatalf("NewAmericanOdds error = %v", err)
	}
	if _, err := (AmericanOdds(100)).Profit(Money{Currency: CAD}); !errors.Is(err, ErrInvalidStake) {
		t.Fatalf("zero stake error = %v", err)
	}
	if _, err := (AmericanOdds(math.MaxInt32)).Profit(Money{Cents: math.MaxInt64, Currency: CAD}); !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
}
