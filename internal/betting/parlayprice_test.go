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
// multiplication of the posted prices gives +264; books shade that, and the
// classic card pays +260. The book's margin has to land in that
// neighbourhood rather than paying the raw number or gouging.
func TestTwoStandardLegsPriceLikeABook(t *testing.T) {
	raw, err := ParlayOddsFor(odds(t, -110, -110), 0)
	if err != nil {
		t.Fatal(err)
	}
	if raw < 263 || raw > 265 {
		t.Fatalf("unjuiced two-leg price = %v, want about +264", raw)
	}
	juiced, err := ParlayOddsFor(odds(t, -110, -110), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	if juiced < 250 || juiced > 262 {
		t.Fatalf("juiced two-leg price = %v, want the +250 to +262 range books pay", juiced)
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
		raw, err := ParlayOddsFor(odds(t, values...), 0)
		if err != nil {
			t.Fatal(err)
		}
		juiced, err := ParlayOddsFor(odds(t, values...), DefaultParlayJuiceBasisPoints)
		if err != nil {
			t.Fatal(err)
		}
		if juiced >= raw {
			t.Fatalf("%d legs: juiced %v is not shorter than raw %v", legs, juiced, raw)
		}
		gap := float64(raw-juiced) / float64(raw)
		if gap <= previousGap {
			t.Fatalf("%d legs: the book's share %.4f did not grow from %.4f", legs, gap, previousGap)
		}
		previousGap = gap
	}
}

// Every parlay the book writes has a real American price, never a decimal or
// a number inside the -100/+100 gap the notation cannot express.
func TestEveryWritableParlayHasAValidAmericanPrice(t *testing.T) {
	for _, legs := range [][]int32{
		{-110, -110}, {-180, 140}, {-200, -200}, {150, 150},
		{-110, -110, -110}, {-300, -110}, {200, -150, 120},
	} {
		price, err := ParlayOddsFor(odds(t, legs...), DefaultParlayJuiceBasisPoints)
		if err != nil {
			t.Errorf("%v error = %v", legs, err)
			continue
		}
		if err := price.Validate(); err != nil {
			t.Errorf("%v priced at %v, which is not valid American odds: %v", legs, price, err)
		}
		if price < MinParlayOdds {
			t.Errorf("%v priced at %v, below the book's minimum %v", legs, price, MinParlayOdds)
		}
	}
}

// Two heavy favourites combine to less than even money. American odds have no
// room there, so the book refuses the bet rather than inventing a price.
func TestVeryShortLegsAreRefusedRatherThanMispriced(t *testing.T) {
	if _, err := ParlayOddsFor(odds(t, -400, -400), DefaultParlayJuiceBasisPoints); !errors.Is(err, ErrParlayTooShort) {
		t.Fatalf("two -400 legs error = %v, want ErrParlayTooShort", err)
	}
}

// A parlay must always pay more than any single leg of it.
func TestParlayPaysMoreThanItsLongestLeg(t *testing.T) {
	price, err := ParlayOddsFor(odds(t, -180, 140), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	stake := ledger.Money{Cents: 10_000, Currency: ledger.CAD}
	parlayProfit, err := price.Profit(stake)
	if err != nil {
		t.Fatal(err)
	}
	singleProfit, err := odds(t, 140)[0].Profit(stake)
	if err != nil {
		t.Fatal(err)
	}
	if parlayProfit.Cents <= singleProfit.Cents {
		t.Fatalf("parlay pays %d, the +140 leg alone pays %d", parlayProfit.Cents, singleProfit.Cents)
	}
}

func TestParlayPriceRejectsBadLegCounts(t *testing.T) {
	if _, err := ParlayOddsFor(odds(t, -110), DefaultParlayJuiceBasisPoints); !errors.Is(err, ErrParlayTooFewLegs) {
		t.Errorf("one leg error = %v, want ErrParlayTooFewLegs", err)
	}
	tooMany := make([]int32, MaxParlayLegs+1)
	for i := range tooMany {
		tooMany[i] = -110
	}
	if _, err := ParlayOddsFor(odds(t, tooMany...), DefaultParlayJuiceBasisPoints); !errors.Is(err, ErrParlayTooManyLegs) {
		t.Errorf("%d legs error = %v, want ErrParlayTooManyLegs", len(tooMany), err)
	}
}
