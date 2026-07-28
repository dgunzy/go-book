package betting

import (
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/pricing"
)

// DefaultParlayJuiceBasisPoints is the extra margin the book takes per leg
// beyond the first, on top of the vig already inside each leg's price.
//
// Multiplying the posted, vig-inclusive prices is what most books do, and it
// already compounds: two -110 legs hold about 4.5% each and roughly 8.9%
// together. On top of that, books have long shaded parlays further — the
// classic Vegas card pays +260 on two -110 legs where straight multiplication
// gives +264. 350 basis points per additional leg shades a little harder than
// the card does, and keeps growing with the number of legs, which is where a
// book's real risk on a parlay is: two -110 legs come back at +252 against
// the +264 the raw multiplication gives.
const DefaultParlayJuiceBasisPoints int64 = 350

// ParlaysMoveLines records a deliberate decision: they do not. A parlay's
// liability does not belong to any one selection — it is contingent on every
// other leg landing — so feeding a leg into the exposure-based pricing engine
// would move a line by an amount the engine cannot attribute or unwind. No
// parlay path calls RepriceMarketAfterWager, and none should.
const ParlaysMoveLines = false

// MinParlayLegs and MaxParlayLegs bound a parlay. Two is the minimum for the
// bet to mean anything; the ceiling keeps the compounded price, and so the
// book's liability per dollar staked, within a range the payout cap can hold.
const (
	MinParlayLegs = 2
	MaxParlayLegs = 8
)

// MinParlayOdds is the shortest price the book will write a parlay at. Two
// heavy favourites can combine to less than even money, which American odds
// cannot express — the notation has no room between -100 and +100 — so a
// parlay that lands there is refused rather than shown in some other format.
// Every parlay on the book therefore has a real American price, the same as
// every single wager.
const MinParlayOdds = ledger.AmericanOdds(100)

// ParlayOddsFor combines the offered prices of every leg into the one price
// the parlay is written at.
//
// Each leg's American price is converted to the probability it implies —
// which already includes that leg's vig — and the probabilities are
// multiplied, because the legs must all land. The combined probability is
// then inflated by juiceBasisPoints for every leg past the first, which is
// the extra hold, and converted back to the nearest American line.
//
// Rounding to a posted line is what a book does, and it keeps a parlay
// working exactly like every other bet here: one American price, profit
// computed by the same integer arithmetic, no second notion of odds anywhere.
//
// juiceBasisPoints of zero is straight multiplication of the posted prices,
// with no shading beyond the vig already in them.
func ParlayOddsFor(legs []ledger.AmericanOdds, juiceBasisPoints int64) (ledger.AmericanOdds, error) {
	if len(legs) < MinParlayLegs {
		return 0, ErrParlayTooFewLegs
	}
	if len(legs) > MaxParlayLegs {
		return 0, ErrParlayTooManyLegs
	}
	if juiceBasisPoints < 0 {
		return 0, invalidf("parlay juice cannot be negative")
	}

	probability := 1.0
	for _, leg := range legs {
		if err := leg.Validate(); err != nil {
			return 0, err
		}
		probability *= pricing.ImpliedProbability(leg)
	}
	// One unit of extra margin per leg beyond the first.
	for range legs[1:] {
		probability *= 1 + float64(juiceBasisPoints)/10_000
	}
	if probability <= 0 || probability >= 1 {
		return 0, ErrParlayTooShort
	}

	odds, err := pricing.AmericanFromProbability(probability)
	if err != nil {
		return 0, err
	}
	// AmericanFromProbability clamps into the representable window, so a
	// price that wanted to sit inside the -100/+100 gap comes back at the
	// edge. Paying that clamped line would hand the member more than the
	// legs are worth, so the parlay is refused instead.
	if odds < MinParlayOdds {
		return 0, ErrParlayTooShort
	}
	return odds, nil
}
