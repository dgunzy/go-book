package betting

import (
	"math"

	"github.com/dgunzy/go-book/internal/ledger"
)

// decimalScale is the fixed-point base for parlay prices. A parlay of two
// short favourites can land between -100 and +100, which American odds cannot
// express at all, so the price is carried as decimal odds in millionths and
// converted for display only when it happens to be representable.
const decimalScale = 1_000_000

// ParlayPrice is decimal odds in millionths: 2.6400 is 2_640_000. A price of
// exactly decimalScale would return the stake and no profit, so every valid
// parlay price is strictly greater.
type ParlayPrice int64

// DefaultParlayJuiceBasisPoints is the extra margin the book takes per leg
// beyond the first, on top of the vig already inside each leg's price.
//
// Multiplying the posted, vig-inclusive prices is what most books do, and it
// already compounds: two -110 legs hold about 4.5% each and roughly 8.9%
// together. On top of that, books have long shaded parlays further — the
// classic Vegas card pays +260 on two -110 legs where straight multiplication
// gives +264. 250 basis points per additional leg reproduces that order of
// shading (2 legs land near +258) and keeps growing with the number of legs,
// which is where a book's real risk on a parlay is.
const DefaultParlayJuiceBasisPoints int64 = 250

// MinParlayLegs and MaxParlayLegs bound a parlay. Two is the minimum for the
// bet to mean anything; the ceiling keeps the compounded price, and so the
// book's liability per dollar staked, within a range the payout cap can hold.
const (
	MinParlayLegs = 2
	MaxParlayLegs = 8
)

// ParlayPriceFor combines the offered prices of every leg into one parlay
// price, then adds the book's parlay margin.
//
// Each leg's American price is converted to the probability it implies —
// which already includes that leg's vig — and the probabilities are
// multiplied, because the legs must all land. The combined probability is
// then inflated by juiceBasisPoints for every leg past the first, which is
// the extra hold, and the price is one over the result.
//
// juiceBasisPoints of zero is straight multiplication of the posted prices,
// with no shading beyond the vig already in them.
func ParlayPriceFor(odds []ledger.AmericanOdds, juiceBasisPoints int64) (ParlayPrice, error) {
	if len(odds) < MinParlayLegs {
		return 0, invalidf("a parlay needs at least %d legs", MinParlayLegs)
	}
	if len(odds) > MaxParlayLegs {
		return 0, invalidf("a parlay may not have more than %d legs", MaxParlayLegs)
	}
	if juiceBasisPoints < 0 {
		return 0, invalidf("parlay juice cannot be negative")
	}

	probability := 1.0
	for _, leg := range odds {
		if err := leg.Validate(); err != nil {
			return 0, err
		}
		probability *= impliedProbability(leg)
	}
	// One unit of extra margin per leg beyond the first.
	for range odds[1:] {
		probability *= 1 + float64(juiceBasisPoints)/10_000
	}
	if probability <= 0 {
		return 0, invalidf("parlay price could not be computed")
	}
	// Shading can only ever take the price down, never below an even return
	// of the stake. A parlay that has been juiced into paying nothing is not
	// a bet the book should be writing.
	if probability >= 1 {
		return 0, invalidf("these legs are too short to parlay at the book's margin")
	}

	price := ParlayPrice(math.Round(decimalScale / probability))
	if price <= decimalScale {
		return 0, invalidf("these legs are too short to parlay at the book's margin")
	}
	return price, nil
}

// impliedProbability is what a posted American price says the outcome's
// chance is, vig included.
func impliedProbability(odds ledger.AmericanOdds) float64 {
	if odds > 0 {
		return 100 / (float64(odds) + 100)
	}
	return float64(-odds) / (float64(-odds) + 100)
}

// Profit returns what a winning parlay pays over the stake, rounded to the
// nearest cent with exact halves rounded up, the same rule single wagers use.
func (p ParlayPrice) Profit(stake ledger.Money) (ledger.Money, error) {
	if p <= decimalScale {
		return ledger.Money{}, invalidf("parlay price must pay more than the stake back")
	}
	if err := stake.Validate(); err != nil {
		return ledger.Money{}, err
	}
	if stake.Cents <= 0 {
		return ledger.Money{}, ledger.ErrInvalidStake
	}
	multiplier := int64(p) - decimalScale
	if stake.Cents > math.MaxInt64/multiplier {
		return ledger.Money{}, ledger.ErrAmountOverflow
	}
	product := stake.Cents * multiplier
	profit := product / decimalScale
	if product%decimalScale >= (decimalScale+1)/2 {
		profit++
	}
	return ledger.Money{Cents: profit, Currency: stake.Currency}, nil
}

// Decimal renders the price the way a member reads it on a betslip, e.g.
// "3.64" for a parlay that returns three and a bit times the stake.
func (p ParlayPrice) Decimal() string {
	whole := int64(p) / decimalScale
	hundredths := (int64(p) % decimalScale) / (decimalScale / 100)
	return formatDecimal(whole, hundredths)
}

// AmericanOdds converts the price to American when it can be expressed that
// way. Two short favourites can combine to less than even money, which lands
// in the gap American odds cannot represent; ok is false there and callers
// show the decimal instead.
func (p ParlayPrice) AmericanOdds() (ledger.AmericanOdds, bool) {
	if p <= decimalScale {
		return 0, false
	}
	profitPerUnit := float64(p-decimalScale) / decimalScale
	var value float64
	if profitPerUnit >= 1 {
		value = math.Round(profitPerUnit * 100)
	} else {
		value = -math.Round(100 / profitPerUnit)
	}
	if value > math.MaxInt32 || value < math.MinInt32 {
		return 0, false
	}
	odds, err := ledger.NewAmericanOdds(int32(value))
	if err != nil {
		return 0, false
	}
	return odds, true
}

func formatDecimal(whole, hundredths int64) string {
	digits := []byte{byte('0' + hundredths/10), byte('0' + hundredths%10)}
	return itoa(whole) + "." + string(digits)
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
