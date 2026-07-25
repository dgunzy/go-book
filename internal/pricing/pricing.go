// Package pricing implements exposure-based line movement for betting
// markets. It is a pure, deterministic function of a market's opening line and
// the accumulated stake on each selection; it holds no database or money
// state. Monetary amounts are integer cents; only the intermediate
// probability arithmetic uses floating point, and the output is always a
// validated integer American-odds value, so the result is reproducible.
//
// The model works in implied-probability space. Each selection's opening odds
// give a prior implied probability; the priors sum to more than 1 by the house
// margin (the overround). When money accumulates on a selection its weight is
// tilted up by exp(stake / liquidity), which shortens its price (making it less
// attractive) and lengthens the others (making them more attractive to balance
// the action). The total overround is preserved, so the house margin is
// unchanged by repricing — only its distribution across selections moves.
package pricing

import (
	"errors"
	"math"

	"github.com/dgunzy/go-book/internal/ledger"
)

// MaxOdds bounds how long or short a repriced line may become, in American
// odds magnitude. It caps both the payout on a lightly-backed side and the
// implied probability on a heavily-backed one.
const MaxOdds = 10000

// maxTiltExponent caps stake/liquidity before exponentiation so an extreme
// stake relative to the liquidity cannot overflow to +Inf; combined with the
// odds clamp this bounds the per-market line move.
const maxTiltExponent = 40.0

// arbitrageSafetyFraction is how much of the opening margin the line is
// allowed to spend on movement. The rest is kept as a cushion, so a market
// cannot be walked to exactly break-even by rounding.
const arbitrageSafetyFraction = 0.8

// maxRoundingSteps bounds the walk back to a safe integer price after
// rounding. One or two steps is always enough in practice.
const maxRoundingSteps = 32

var (
	// ErrTooFewSelections is returned when repricing is asked to balance a
	// market with fewer than two selections; there is nothing to balance.
	ErrTooFewSelections = errors.New("pricing: at least two selections are required")
	// ErrLiquidityNotPositive is returned when the liquidity parameter is not
	// strictly positive.
	ErrLiquidityNotPositive = errors.New("pricing: liquidity must be positive")
)

// SelectionInput is one selection's stable opening line and its accumulated
// accepted stake, in cents.
type SelectionInput struct {
	OpeningOdds ledger.AmericanOdds
	StakeCents  int64
}

// SelectionResult is the repriced line for one selection, in input order.
type SelectionResult struct {
	Odds ledger.AmericanOdds
}

// Reprice returns the new offered line for each selection given its opening
// line and accumulated stake, tilting toward the heavily-backed side while
// preserving the total overround. With no stake anywhere the result equals the
// opening line exactly. liquidityCents is the sensitivity "b": larger values
// move the line less per dollar of action.
func Reprice(selections []SelectionInput, liquidityCents int64) ([]SelectionResult, error) {
	if len(selections) < 2 {
		return nil, ErrTooFewSelections
	}
	if liquidityCents <= 0 {
		return nil, ErrLiquidityNotPositive
	}

	priors := make([]float64, len(selections))
	var overround float64
	for i, selection := range selections {
		if err := selection.OpeningOdds.Validate(); err != nil {
			return nil, err
		}
		if selection.StakeCents < 0 {
			return nil, errors.New("pricing: stake must not be negative")
		}
		priors[i] = ImpliedProbability(selection.OpeningOdds)
		overround += priors[i]
	}

	weights := make([]float64, len(selections))
	var weightSum float64
	liquidity := float64(liquidityCents)
	for i, selection := range selections {
		exponent := float64(selection.StakeCents) / liquidity
		if exponent > maxTiltExponent {
			exponent = maxTiltExponent
		}
		weights[i] = priors[i] * math.Exp(exponent)
		weightSum += weights[i]
	}

	probabilities := make([]float64, len(selections))
	for i := range selections {
		// Rescale so the tilted probabilities sum back to the original
		// overround, keeping the house margin constant.
		probabilities[i] = weights[i] * (overround / weightSum)
	}
	floors := driftFloors(priors, overround)
	applyDriftFloors(probabilities, floors, overround)

	results := make([]SelectionResult, len(selections))
	for i := range selections {
		odds, err := AmericanFromProbability(probabilities[i])
		if err != nil {
			return nil, err
		}
		// Rounding to an integer price can land a shade more generous than the
		// floor allows, so walk it back to the first price that still clears.
		for step := 0; step < maxRoundingSteps && ImpliedProbability(odds) < floors[i]; step++ {
			shorter, err := shortenOnePoint(odds)
			if err != nil || shorter == odds {
				break
			}
			odds = shorter
		}
		results[i] = SelectionResult{Odds: odds}
	}
	return results, nil
}

// driftFloors returns the lowest implied probability each selection may be
// offered at — that is, the longest price it may ever reach.
//
// This is what stops a member arbitraging the book against its own line
// movement. Prices taken at different times can be combined, so the book is
// only safe if the sum of the most generous price it will ever post on every
// side still exceeds even money. Each side starts at its opening probability
// and the sum of those exceeds 1 by the margin, so sharing the margin out
// across the selections and letting no side fall further than its share keeps
// that sum above 1 no matter how the action lands.
func driftFloors(priors []float64, overround float64) []float64 {
	allowance := (overround - 1.0) / float64(len(priors)) * arbitrageSafetyFraction
	if allowance < 0 {
		// A market posted at or below even money has no margin to spend, so
		// any movement at all would open an arbitrage: the line stays put.
		allowance = 0
	}
	floors := make([]float64, len(priors))
	for i, prior := range priors {
		floors[i] = prior - allowance
	}
	return floors
}

// applyDriftFloors lifts any selection that has drifted past its floor back to
// it and rescales the rest so the overround is unchanged. Lifting one side
// pushes the others down, which can put another below its own floor, so it
// repeats until nothing new is clamped — at most once per selection.
func applyDriftFloors(probabilities, floors []float64, overround float64) {
	clamped := make([]bool, len(probabilities))
	for pass := 0; pass <= len(probabilities); pass++ {
		var fixed, free float64
		newlyClamped := false
		for i := range probabilities {
			switch {
			case clamped[i]:
				fixed += floors[i]
			case probabilities[i] < floors[i]:
				probabilities[i] = floors[i]
				clamped[i] = true
				fixed += floors[i]
				newlyClamped = true
			default:
				free += probabilities[i]
			}
		}
		if !newlyClamped || free <= 0 {
			return
		}
		scale := (overround - fixed) / free
		for i := range probabilities {
			if !clamped[i] {
				probabilities[i] *= scale
			}
		}
	}
}

// shortenOnePoint moves a price one American point toward the book, which
// raises its implied probability.
func shortenOnePoint(odds ledger.AmericanOdds) (ledger.AmericanOdds, error) {
	value := int32(odds)
	switch {
	case value > 100:
		value--
	case value == 100:
		value = -100
	case value > -MaxOdds:
		value--
	default:
		return odds, nil
	}
	return ledger.NewAmericanOdds(value)
}

// ImpliedProbability converts American odds to their implied probability,
// including the house margin baked into the price. Favorites (negative odds)
// return a probability above 0.5, underdogs below.
func ImpliedProbability(odds ledger.AmericanOdds) float64 {
	value := float64(odds)
	if odds > 0 {
		return 100.0 / (value + 100.0)
	}
	return -value / (-value + 100.0)
}

// AmericanFromProbability converts an implied probability to the nearest valid
// American-odds line, clamped to MaxOdds. It is the inverse of
// ImpliedProbability up to integer rounding.
func AmericanFromProbability(probability float64) (ledger.AmericanOdds, error) {
	// Clamp the probability to the window that MaxOdds can represent so an
	// extreme tilt cannot produce an out-of-range or infinite line.
	minProb := 100.0 / (float64(MaxOdds) + 100.0)
	maxProb := float64(MaxOdds) / (float64(MaxOdds) + 100.0)
	if probability < minProb {
		probability = minProb
	}
	if probability > maxProb {
		probability = maxProb
	}

	var value int64
	if probability <= 0.5 {
		// Underdog: positive odds = 100 * (1 - p) / p.
		value = int64(math.Round(100.0 * (1.0 - probability) / probability))
		if value < 100 {
			value = 100
		}
	} else {
		// Favorite: negative odds = -100 * p / (1 - p).
		value = -int64(math.Round(100.0 * probability / (1.0 - probability)))
		if value > -100 {
			value = -100
		}
	}
	if value > MaxOdds {
		value = MaxOdds
	}
	if value < -MaxOdds {
		value = -MaxOdds
	}
	return ledger.NewAmericanOdds(int32(value))
}
