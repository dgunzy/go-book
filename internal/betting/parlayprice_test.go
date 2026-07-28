package betting

import (
	"errors"
	"testing"

	"github.com/dgunzy/go-book/internal/ledger"
)

func odds(t *testing.T, values ...int32) []ledger.AmericanOdds {
	t.Helper()
	out := make([]ledger.AmericanOdds, 0, len(values))
	for _, v := range values {
		o, err := ledger.NewAmericanOdds(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

// The reference case every bettor knows: two -110 legs. Straight
// multiplication of the posted prices gives +264; a book shades that, and the
// classic card pays +260. The book's margin has to land in that neighbourhood
// rather than paying the raw number or gouging.
func TestTwoStandardLegsPriceLikeABook(t *testing.T) {
	raw, err := ParlayPriceFor(odds(t, -110, -110), 0)
	if err != nil {
		t.Fatal(err)
	}
	rawAmerican, ok := raw.AmericanOdds()
	if !ok || rawAmerican < 263 || rawAmerican > 265 {
		t.Fatalf("unjuiced two-leg price = %v (ok %v), want about +264", rawAmerican, ok)
	}

	juiced, err := ParlayPriceFor(odds(t, -110, -110), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	american, ok := juiced.AmericanOdds()
	if !ok {
		t.Fatalf("juiced two-leg price is not expressible in American odds: %s", juiced.Decimal())
	}
	if american < 250 || american > 262 {
		t.Fatalf("juiced two-leg price = %v, want in the +250 to +262 range books pay", american)
	}
	if juiced >= raw {
		t.Fatal("the book's margin must shorten the price, not lengthen it")
	}
}

// Juice compounds with legs, because that is where the book's risk grows.
func TestJuiceGrowsWithEachAddedLeg(t *testing.T) {
	var previousGap float64
	for legs := 2; legs <= 5; legs++ {
		values := make([]int32, legs)
		for i := range values {
			values[i] = -110
		}
		raw, err := ParlayPriceFor(odds(t, values...), 0)
		if err != nil {
			t.Fatal(err)
		}
		juiced, err := ParlayPriceFor(odds(t, values...), DefaultParlayJuiceBasisPoints)
		if err != nil {
			t.Fatal(err)
		}
		if juiced >= raw {
			t.Fatalf("%d legs: juiced price %d is not shorter than raw %d", legs, juiced, raw)
		}
		gap := float64(raw-juiced) / float64(raw)
		if gap <= previousGap {
			t.Fatalf("%d legs: the book's share %.4f did not grow from %.4f", legs, gap, previousGap)
		}
		previousGap = gap
	}
}

// A parlay must always pay more than a single leg of it, or there would be no
// reason to write the bet.
func TestParlayPaysMoreThanItsLongestLeg(t *testing.T) {
	price, err := ParlayPriceFor(odds(t, -180, 140), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	stake := ledger.Money{Cents: 10_000, Currency: ledger.CAD}
	parlayProfit, err := price.Profit(stake)
	if err != nil {
		t.Fatal(err)
	}
	single := odds(t, 140)[0]
	singleProfit, err := single.Profit(stake)
	if err != nil {
		t.Fatal(err)
	}
	if parlayProfit.Cents <= singleProfit.Cents {
		t.Fatalf("parlay pays %d, the +140 leg alone pays %d", parlayProfit.Cents, singleProfit.Cents)
	}
}

// Two short favourites combine to less than even money, which American odds
// cannot express. The price still has to be exact and still has to pay.
func TestShortFavouritesStillPriceAndPay(t *testing.T) {
	price, err := ParlayPriceFor(odds(t, -400, -400), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatalf("ParlayPriceFor() on two short favourites error = %v", err)
	}
	if price <= decimalScale {
		t.Fatalf("price %d does not pay more than the stake", price)
	}
	profit, err := price.Profit(ledger.Money{Cents: 100_000, Currency: ledger.CAD})
	if err != nil {
		t.Fatal(err)
	}
	if profit.Cents <= 0 {
		t.Fatalf("profit on two -400s = %d, want positive", profit.Cents)
	}
	// Around 1.52 decimal after juice: a real number the betslip can show.
	if decimal := price.Decimal(); decimal == "" || decimal[0] != '1' {
		t.Fatalf("decimal = %q, want something just over 1", decimal)
	}
}

func TestParlayPriceRejectsBadLegCounts(t *testing.T) {
	if _, err := ParlayPriceFor(odds(t, -110), DefaultParlayJuiceBasisPoints); !errors.Is(err, ErrInvalid) {
		t.Errorf("one leg error = %v, want ErrInvalid", err)
	}
	tooMany := make([]int32, MaxParlayLegs+1)
	for i := range tooMany {
		tooMany[i] = -110
	}
	if _, err := ParlayPriceFor(odds(t, tooMany...), DefaultParlayJuiceBasisPoints); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d legs error = %v, want ErrInvalid", len(tooMany), err)
	}
}

// Profit rounds the same way single wagers do, and a parlay price is never
// allowed to return only the stake.
func TestParlayProfitRoundingAndFloor(t *testing.T) {
	price, err := ParlayPriceFor(odds(t, -110, -110), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	profit, err := price.Profit(ledger.Money{Cents: 10_000, Currency: ledger.CAD})
	if err != nil {
		t.Fatal(err)
	}
	if profit.Cents < 25_000 || profit.Cents > 26_200 {
		t.Fatalf("$100 on a two-leg parlay wins %d cents, want about 25800", profit.Cents)
	}
	if _, err := ParlayPrice(decimalScale).Profit(ledger.Money{Cents: 100, Currency: ledger.CAD}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("even-money price error = %v, want ErrInvalid", err)
	}
}
