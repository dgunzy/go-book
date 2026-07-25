package pricing

import (
	"math"
	"testing"

	"github.com/dgunzy/go-book/internal/ledger"
)

func TestImpliedProbabilityRoundTrips(t *testing.T) {
	t.Parallel()
	// -100 and +100 both mean even money (probability 0.5), so they are
	// intentionally excluded: the round trip canonicalizes even money to +100.
	for _, odds := range []ledger.AmericanOdds{-500, -200, -110, 110, 200, 500, 10000} {
		p := ImpliedProbability(odds)
		if p <= 0 || p >= 1 {
			t.Fatalf("ImpliedProbability(%d) = %v, want in (0,1)", odds, p)
		}
		back, err := AmericanFromProbability(p)
		if err != nil {
			t.Fatalf("AmericanFromProbability(%v) error = %v", p, err)
		}
		// Round-trip is exact for canonical lines except at the clamp edge.
		if diff := int64(back) - int64(odds); diff < -1 || diff > 1 {
			t.Fatalf("round trip %d -> %v -> %d drifted by %d", odds, p, back, diff)
		}
	}
}

func TestEvenMoneyCanonicalizes(t *testing.T) {
	t.Parallel()
	if p := ImpliedProbability(-100); p != 0.5 {
		t.Fatalf("ImpliedProbability(-100) = %v, want 0.5", p)
	}
	odds, err := AmericanFromProbability(0.5)
	if err != nil {
		t.Fatal(err)
	}
	if odds != 100 {
		t.Fatalf("AmericanFromProbability(0.5) = %d, want +100 (canonical even money)", odds)
	}
}

func TestImpliedProbabilityDirection(t *testing.T) {
	t.Parallel()
	if ImpliedProbability(-200) <= 0.5 {
		t.Fatal("a favorite (-200) must imply probability above 0.5")
	}
	if ImpliedProbability(150) >= 0.5 {
		t.Fatal("an underdog (+150) must imply probability below 0.5")
	}
	if p := ImpliedProbability(100); math.Abs(p-0.5) > 1e-9 {
		t.Fatalf("even money (+100) implied probability = %v, want 0.5", p)
	}
}

func TestRepriceNoStakeIsNoOp(t *testing.T) {
	t.Parallel()
	in := []SelectionInput{
		{OpeningOdds: -110, StakeCents: 0},
		{OpeningOdds: -110, StakeCents: 0},
	}
	out, err := Reprice(in, 500_000)
	if err != nil {
		t.Fatalf("Reprice() error = %v", err)
	}
	for i, r := range out {
		if r.Odds != in[i].OpeningOdds {
			t.Fatalf("selection %d moved with no stake: %d -> %d", i, in[i].OpeningOdds, r.Odds)
		}
	}
}

func TestRepriceShortensBackedSideLengthensOther(t *testing.T) {
	t.Parallel()
	// $1000 on selection A of an even two-way market, $50 liquidity sensitivity.
	in := []SelectionInput{
		{OpeningOdds: -110, StakeCents: 100_000},
		{OpeningOdds: -110, StakeCents: 0},
	}
	out, err := Reprice(in, 500_000)
	if err != nil {
		t.Fatalf("Reprice() error = %v", err)
	}
	// The backed side's price must shorten (odds more negative / less payout).
	if out[0].Odds >= in[0].OpeningOdds {
		t.Fatalf("backed side did not shorten: %d -> %d", in[0].OpeningOdds, out[0].Odds)
	}
	// The other side must lengthen (more attractive: less negative or positive).
	if out[1].Odds <= in[1].OpeningOdds {
		t.Fatalf("light side did not lengthen: %d -> %d", in[1].OpeningOdds, out[1].Odds)
	}
}

func TestRepricePreservesOverround(t *testing.T) {
	t.Parallel()
	in := []SelectionInput{
		{OpeningOdds: -110, StakeCents: 250_000},
		{OpeningOdds: 120, StakeCents: 10_000},
		{OpeningOdds: 300, StakeCents: 0},
	}
	before := 0.0
	for _, s := range in {
		before += ImpliedProbability(s.OpeningOdds)
	}
	out, err := Reprice(in, 400_000)
	if err != nil {
		t.Fatalf("Reprice() error = %v", err)
	}
	after := 0.0
	for _, r := range out {
		after += ImpliedProbability(r.Odds)
	}
	// The overround (house margin) must be preserved within integer-rounding
	// tolerance across the three selections.
	if math.Abs(after-before) > 0.02 {
		t.Fatalf("overround changed: before %.4f after %.4f", before, after)
	}
}

func TestRepriceLargerLiquidityMovesLess(t *testing.T) {
	t.Parallel()
	mk := func(b int64) ledger.AmericanOdds {
		out, err := Reprice([]SelectionInput{
			{OpeningOdds: -110, StakeCents: 100_000},
			{OpeningOdds: -110, StakeCents: 0},
		}, b)
		if err != nil {
			t.Fatal(err)
		}
		return out[0].Odds
	}
	sensitive := mk(200_000)
	sticky := mk(2_000_000)
	// A larger liquidity parameter must move the backed line less.
	if move(sensitive, -110) <= move(sticky, -110) {
		t.Fatalf("larger liquidity did not move less: sensitive=%d sticky=%d", sensitive, sticky)
	}
}

func move(now, opening ledger.AmericanOdds) int64 {
	d := int64(now) - int64(opening)
	if d < 0 {
		return -d
	}
	return d
}

func TestRepriceRejectsBadInput(t *testing.T) {
	t.Parallel()
	if _, err := Reprice([]SelectionInput{{OpeningOdds: -110}}, 1000); err != ErrTooFewSelections {
		t.Fatalf("one selection: err = %v, want ErrTooFewSelections", err)
	}
	if _, err := Reprice([]SelectionInput{{OpeningOdds: -110}, {OpeningOdds: 100}}, 0); err != ErrLiquidityNotPositive {
		t.Fatalf("zero liquidity: err = %v, want ErrLiquidityNotPositive", err)
	}
	if _, err := Reprice([]SelectionInput{{OpeningOdds: 50}, {OpeningOdds: 100}}, 1000); err == nil {
		t.Fatal("invalid opening odds should error")
	}
	if _, err := Reprice([]SelectionInput{{OpeningOdds: -110, StakeCents: -1}, {OpeningOdds: 100}}, 1000); err == nil {
		t.Fatal("negative stake should error")
	}
}

func TestRepriceExtremeStakeStaysInRange(t *testing.T) {
	t.Parallel()
	out, err := Reprice([]SelectionInput{
		{OpeningOdds: -110, StakeCents: 1_000_000_000_000},
		{OpeningOdds: -110, StakeCents: 0},
	}, 1000)
	if err != nil {
		t.Fatalf("Reprice() error = %v", err)
	}
	for i, r := range out {
		if err := r.Odds.Validate(); err != nil {
			t.Fatalf("selection %d produced invalid odds %d: %v", i, r.Odds, err)
		}
		if r.Odds > MaxOdds || r.Odds < -MaxOdds {
			t.Fatalf("selection %d odds %d exceeded MaxOdds", i, r.Odds)
		}
	}
}

// bestPrice tracks the most generous price a selection has been offered at,
// which is the one a member would combine with the other side to arbitrage.
type bestPrice struct{ probability float64 }

func (b *bestPrice) see(odds ledger.AmericanOdds) {
	p := ImpliedProbability(odds)
	if b.probability == 0 || p < b.probability {
		b.probability = p
	}
}

// TestRepriceNeverOpensAnArbitrage is the property that matters: prices taken
// at different moments can be combined, so the book is only safe if the sum of
// the most generous price it ever posts on every side stays above even money.
// Anything at or below 1 means a member can back both sides and be paid
// whatever happens.
func TestRepriceNeverOpensAnArbitrage(t *testing.T) {
	t.Parallel()
	openings := [][]ledger.AmericanOdds{
		{-130, 100},     // the live Match 1 line
		{-115, -115},    // a balanced two-way
		{-105, -115},    // a thin two-way
		{-200, 160},     // a heavy favourite
		{-1000, 700},    // a lopsided line
		{150, -180},     // reversed order
		{200, 200, 200}, // a three-way
		{-120, 250, 400},
	}
	stakeSteps := []int64{0, 5_000, 25_000, 100_000, 500_000, 2_000_000, 100_000_000}
	liquidities := []int64{100_000, 300_000, 500_000, 1_500_000}

	for _, opening := range openings {
		for _, liquidity := range liquidities {
			// Walk action onto each side in turn, at every size, and remember
			// the best price each side is ever shown.
			best := make([]bestPrice, len(opening))
			for _, side := range []int{0, 1} {
				for _, stake := range stakeSteps {
					inputs := make([]SelectionInput, len(opening))
					for i, odds := range opening {
						inputs[i] = SelectionInput{OpeningOdds: odds}
						if i == side {
							inputs[i].StakeCents = stake
						}
					}
					results, err := Reprice(inputs, liquidity)
					if err != nil {
						t.Fatalf("Reprice(%v, %d) error = %v", opening, liquidity, err)
					}
					for i, result := range results {
						best[i].see(result.Odds)
					}
				}
			}
			var sum, openingSum float64
			for i, b := range best {
				sum += b.probability
				openingSum += ImpliedProbability(opening[i])
			}
			// Below even money the pair pays more than it costs, whatever the
			// result: that is the arbitrage. Exactly even money is a wash and
			// is all a market posted with no margin can offer.
			if sum < 1.0-1e-9 {
				t.Errorf("opening %v at liquidity %d can be arbitraged: best prices imply %.4f, want at least 1",
					opening, liquidity, sum)
			}
			// A market posted with a real margin must keep some of it: an
			// exactly break-even board is one rounding step from a hole.
			if openingSum > 1.0+1e-9 && sum <= 1.0 {
				t.Errorf("opening %v at liquidity %d spent its whole %.2f%% margin: best prices imply %.4f",
					opening, liquidity, (openingSum-1)*100, sum)
			}
		}
	}
}

// TestRepriceHoldsTheLineWhenThereIsNoMargin covers a market posted with no
// overround: there is nothing to spend on movement, so any move at all would
// hand out an arbitrage. The line must stay where it opened.
func TestRepriceHoldsTheLineWhenThereIsNoMargin(t *testing.T) {
	t.Parallel()
	inputs := []SelectionInput{
		{OpeningOdds: 100, StakeCents: 5_000_000},
		{OpeningOdds: -100, StakeCents: 0},
	}
	results, err := Reprice(inputs, 300_000)
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if ImpliedProbability(result.Odds) < ImpliedProbability(inputs[i].OpeningOdds)-1e-9 {
			t.Fatalf("selection %d drifted to %d from an opening line with no margin to spend",
				i, result.Odds)
		}
	}
}

// TestRepriceKeepsAHeavilyBackedMarketSafe is the case that was reported from
// the live book: $1,130 on the favourite against $150 on the dog moved the
// line from -130/+100 to -186/+141, and +141 combined with the -130 someone
// had already taken is a guaranteed profit. The drift floor has to stop the
// dog's price short of that.
func TestRepriceKeepsAHeavilyBackedMarketSafe(t *testing.T) {
	t.Parallel()
	inputs := []SelectionInput{
		{OpeningOdds: -130, StakeCents: 113_000},
		{OpeningOdds: 100, StakeCents: 15_000},
	}
	results, err := Reprice(inputs, 300_000)
	if err != nil {
		t.Fatal(err)
	}
	favourite, dog := results[0].Odds, results[1].Odds

	// Anyone holding the opening -130 must not be able to pair it with the
	// dog's current price for a guaranteed profit.
	locked := ImpliedProbability(-130) + ImpliedProbability(dog)
	if locked <= 1.0 {
		t.Fatalf("opening -130 paired with %d implies %.4f: that is an arbitrage", dog, locked)
	}
	// The same in the other direction, for a member who took the opening +100.
	locked = ImpliedProbability(100) + ImpliedProbability(favourite)
	if locked <= 1.0 {
		t.Fatalf("opening +100 paired with %d implies %.4f: that is an arbitrage", favourite, locked)
	}
	// The line still moved toward the money — this is a floor, not a freeze.
	if favourite >= -130 {
		t.Fatalf("favourite = %d, want a shorter price than the -130 it opened at", favourite)
	}
	if dog <= 100 {
		t.Fatalf("dog = %d, want a longer price than the +100 it opened at", dog)
	}
	// And it stops well short of the +141 that opened the hole.
	if ImpliedProbability(dog) < ImpliedProbability(141) {
		t.Fatalf("dog = %d, which is at least as generous as the +141 that was arbitrageable", dog)
	}
	t.Logf("clamped line: %d / %+d (was -186 / +141 unclamped)", favourite, dog)
}

// pointsGetterBoard is the shape that prompted this: a sixteen-runner outright
// with a wide margin, where one member backing their favourite must not swing
// everyone else's price around.
func pointsGetterBoard() []ledger.AmericanOdds {
	return []ledger.AmericanOdds{
		500, 600, 600, 900, 900, 900, 900, 1400,
		1400, 1800, 1800, 2500, 2500, 2500, 2500, 3000,
	}
}

func TestRepriceOnAManyArmBoardStaysSafeAndProportionate(t *testing.T) {
	t.Parallel()
	opening := pointsGetterBoard()
	inputs := make([]SelectionInput, len(opening))
	for i, odds := range opening {
		inputs[i] = SelectionInput{OpeningOdds: odds}
	}
	// A big bet on the favourite, far more than this book would normally see.
	inputs[0].StakeCents = 200_000

	results, err := Reprice(inputs, 500_000)
	if err != nil {
		t.Fatalf("Reprice() error = %v", err)
	}

	// The backed arm shortens; nobody else may drift past their floor.
	if ImpliedProbability(results[0].Odds) <= ImpliedProbability(opening[0]) {
		t.Fatalf("backed arm = %d, want a shorter price than its opening %d", results[0].Odds, opening[0])
	}
	floors := driftFloors(impliedAll(opening), overroundOf(opening))
	for i, result := range results {
		if got := ImpliedProbability(result.Odds); got < floors[i]-1e-9 {
			t.Errorf("arm %d drifted to %d (p %.5f), past its floor %.5f", i, result.Odds, got, floors[i])
		}
	}

	// Every arm moves by a similar proportion of its own price, so a longshot
	// is not swung around by a bet on the favourite.
	for i := 1; i < len(results); i++ {
		before, after := ImpliedProbability(opening[i]), ImpliedProbability(results[i].Odds)
		if change := (before - after) / before; change > 0.25 {
			t.Errorf("arm %d (%d) moved %.1f%% of its own probability, want a proportionate move",
				i, opening[i], change*100)
		}
	}
}

// The arbitrage property has to hold on a many-arm board too: backing every
// runner at the best price each is ever posted must still cost more than it
// can return.
func TestRepriceManyArmBoardCannotBeBackedAcross(t *testing.T) {
	t.Parallel()
	opening := pointsGetterBoard()
	best := make([]bestPrice, len(opening))

	for _, liquidity := range []int64{100_000, 500_000, 2_000_000} {
		for backed := range opening {
			for _, stake := range []int64{0, 50_000, 250_000, 1_000_000, 50_000_000} {
				inputs := make([]SelectionInput, len(opening))
				for i, odds := range opening {
					inputs[i] = SelectionInput{OpeningOdds: odds}
					if i == backed {
						inputs[i].StakeCents = stake
					}
				}
				results, err := Reprice(inputs, liquidity)
				if err != nil {
					t.Fatalf("Reprice() error = %v", err)
				}
				for i, result := range results {
					best[i].see(result.Odds)
				}
			}
		}
	}

	var sum float64
	for _, b := range best {
		sum += b.probability
	}
	if sum <= 1.0 {
		t.Fatalf("backing all sixteen arms at their best prices implies %.4f: that is free money", sum)
	}
	t.Logf("sixteen arms at their most generous: %.4f (opened at %.4f)", sum, overroundOf(opening))
}

func impliedAll(odds []ledger.AmericanOdds) []float64 {
	priors := make([]float64, len(odds))
	for i, o := range odds {
		priors[i] = ImpliedProbability(o)
	}
	return priors
}

func overroundOf(odds []ledger.AmericanOdds) float64 {
	var total float64
	for _, o := range odds {
		total += ImpliedProbability(o)
	}
	return total
}
