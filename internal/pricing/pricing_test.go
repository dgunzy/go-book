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
