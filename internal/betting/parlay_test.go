package betting

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

func matchMarket(t *testing.T, n int, price int32) (Market, Selection) {
	t.Helper()
	o, err := ledger.NewAmericanOdds(price)
	if err != nil {
		t.Fatal(err)
	}
	id := func(tag string) ID {
		return ID(fmt.Sprintf("%08d-0000-0000-0000-%012d", n, len(tag)*1_000_000+n))
	}
	market := Market{
		ID: id("market"), Type: MarketMatch, MatchID: id("match"), Title: fmt.Sprintf("Match %d", n),
		State: MarketOpen, Currency: ledger.CAD,
		OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour),
	}
	selection := Selection{
		ID: id("selection"), MarketID: market.ID, Key: "side-a",
		DisplayTerms: fmt.Sprintf("Side A of match %d", n), OfferedAmericanOdds: o, Active: true,
	}
	return market, selection
}

func parlayCommand(t *testing.T, stakeCents int64, prices ...int32) PlaceParlayCommand {
	t.Helper()
	markets := make([]Market, 0, len(prices))
	selections := make([]Selection, 0, len(prices))
	for i, price := range prices {
		m, s := matchMarket(t, i+1, price)
		markets = append(markets, m)
		selections = append(selections, s)
	}
	return PlaceParlayCommand{
		ParlayID: "11111111-1111-1111-1111-111111111111", UserID: "22222222-2222-2222-2222-222222222222",
		Markets: markets, Selections: selections, JuiceBasisPoints: DefaultParlayJuiceBasisPoints,
		MaxPayoutCents: 500_000, FundingAccountType: FundingUserCash,
		Stake: ledger.Money{Cents: stakeCents, Currency: ledger.CAD}, IdempotencyKey: "k", Now: time.Now(),
	}
}

func TestPlaceParlayPricesEveryLegTogether(t *testing.T) {
	parlay, err := PlaceParlay(parlayCommand(t, 10_000, -110, -110))
	if err != nil {
		t.Fatalf("PlaceParlay() error = %v", err)
	}
	if len(parlay.Legs) != 2 || parlay.State != WagerPending {
		t.Fatalf("parlay = %+v", parlay)
	}
	if parlay.PotentialProfit.Cents < 25_000 {
		t.Fatalf("profit = %d, want a parlay price not a single one", parlay.PotentialProfit.Cents)
	}
	// The legs snapshot their price, so a later line move cannot change them.
	for _, leg := range parlay.Legs {
		if leg.AcceptedOdds != -110 || leg.AcceptedTerms == "" {
			t.Fatalf("leg did not snapshot its terms: %+v", leg)
		}
	}
}

// Only match markets. Props and futures are entangled with match results and
// with each other, which is exactly how a book gets picked off.
func TestPlaceParlayRefusesAnythingButMatchMarkets(t *testing.T) {
	for _, kind := range []MarketType{MarketProp, MarketFuture} {
		command := parlayCommand(t, 10_000, -110, -110)
		command.Markets[1].Type = kind
		command.Markets[1].MatchID = ""
		if _, err := PlaceParlay(command); !errors.Is(err, ErrParlayMarketNotEligible) {
			t.Errorf("%s leg error = %v, want ErrParlayMarketNotEligible", kind, err)
		}
	}
}

// Both sides of one match can never both win; the same side twice is just a
// bigger single bet at a parlay price. Neither may be written.
func TestPlaceParlayRefusesTwoLegsFromOneMatch(t *testing.T) {
	command := parlayCommand(t, 10_000, -110, -110)
	command.Markets[1] = command.Markets[0]
	command.Selections[1].MarketID = command.Markets[0].ID
	if _, err := PlaceParlay(command); !errors.Is(err, ErrParlayDuplicateMarket) && !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("duplicate match error = %v, want it refused", err)
	}
}

// The $5,000 ceiling is where a parlay most needs it: a small stake at a
// compounded price is exactly how a book ends up owing more than it holds.
func TestPlaceParlayHonoursThePayoutCeiling(t *testing.T) {
	// Four +200 legs multiply to roughly 80x; $100 would win far past $5,000.
	command := parlayCommand(t, 10_000, 200, 200, 200, 200)
	if _, err := PlaceParlay(command); !errors.Is(err, ErrPayoutAboveLimit) {
		t.Fatalf("error = %v, want ErrPayoutAboveLimit", err)
	}
	// A stake small enough to stay under it is fine.
	small := parlayCommand(t, 100, 200, 200, 200, 200)
	parlay, err := PlaceParlay(small)
	if err != nil {
		t.Fatalf("small stake error = %v", err)
	}
	if parlay.PotentialProfit.Cents > 500_000 {
		t.Fatalf("profit %d is over the ceiling", parlay.PotentialProfit.Cents)
	}
}

func TestPlaceParlayEnforcesLegCountsMarketStateAndRestrictions(t *testing.T) {
	if _, err := PlaceParlay(parlayCommand(t, 10_000, -110)); !errors.Is(err, ErrParlayTooFewLegs) {
		t.Errorf("one leg error = %v, want ErrParlayTooFewLegs", err)
	}
	closed := parlayCommand(t, 10_000, -110, -110)
	closed.Markets[1].State = MarketClosed
	if _, err := PlaceParlay(closed); !errors.Is(err, ErrMarketNotOpen) {
		t.Errorf("closed leg error = %v, want ErrMarketNotOpen", err)
	}
	restricted := parlayCommand(t, 10_000, -110, -110)
	restricted.Restrictions = []Restriction{{UserID: restricted.UserID, SelectionID: restricted.Selections[1].ID}}
	if _, err := PlaceParlay(restricted); !errors.Is(err, ErrUserRestricted) {
		t.Errorf("restricted leg error = %v, want ErrUserRestricted", err)
	}
	inactive := parlayCommand(t, 10_000, -110, -110)
	inactive.Selections[0].Active = false
	if _, err := PlaceParlay(inactive); !errors.Is(err, ErrSelectionInactive) {
		t.Errorf("inactive leg error = %v, want ErrSelectionInactive", err)
	}
}

func gradedParlay(t *testing.T, results ...SettlementResult) Parlay {
	t.Helper()
	prices := make([]int32, len(results))
	for i := range prices {
		prices[i] = -110
	}
	parlay, err := PlaceParlay(parlayCommand(t, 10_000, prices...))
	if err != nil {
		t.Fatal(err)
	}
	for i := range parlay.Legs {
		parlay.Legs[i].Result = results[i]
	}
	return parlay
}

func TestGradeParlayPaysOnlyWhenEveryLegWins(t *testing.T) {
	outcome, err := GradeParlay(gradedParlay(t, ResultWin, ResultWin), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result != ResultWin || outcome.Profit.Cents <= 0 {
		t.Fatalf("all-win outcome = %+v", outcome)
	}
	if outcome.Returns.Cents != outcome.Stake.Cents+outcome.Profit.Cents {
		t.Fatalf("returns %d != stake %d + profit %d", outcome.Returns.Cents, outcome.Stake.Cents, outcome.Profit.Cents)
	}
}

// One dead leg kills the bet, whatever the others did.
func TestGradeParlayLosesOnAnySingleLosingLeg(t *testing.T) {
	for _, results := range [][]SettlementResult{
		{ResultLoss, ResultWin},
		{ResultWin, ResultLoss},
		{ResultPush, ResultLoss},
	} {
		outcome, err := GradeParlay(gradedParlay(t, results...), DefaultParlayJuiceBasisPoints)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Result != ResultLoss || outcome.Profit.Cents != 0 || outcome.Returns.Cents != 0 {
			t.Fatalf("%v outcome = %+v, want a total loss", results, outcome)
		}
	}
}

// A pushed leg drops out and the parlay pays as the smaller parlay it became,
// which is how every book treats a tie or a postponement.
func TestGradeParlayRepricesWhenALegPushesOut(t *testing.T) {
	threeAllWin, err := GradeParlay(gradedParlay(t, ResultWin, ResultWin, ResultWin), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	twoAllWin, err := GradeParlay(gradedParlay(t, ResultWin, ResultWin), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := GradeParlay(gradedParlay(t, ResultWin, ResultWin, ResultPush), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	if pushed.Result != ResultWin {
		t.Fatalf("pushed-leg outcome = %+v, want a win on the rest", pushed)
	}
	if pushed.Profit.Cents != twoAllWin.Profit.Cents {
		t.Fatalf("three legs with one push paid %d, want the two-leg price %d", pushed.Profit.Cents, twoAllWin.Profit.Cents)
	}
	if pushed.Profit.Cents >= threeAllWin.Profit.Cents {
		t.Fatal("a pushed leg must not pay the full three-leg price")
	}
}

// Reduced to one surviving leg it is not a parlay any more, so it pays that
// leg's own price with no parlay juice on top.
func TestGradeParlayReducedToOneLegPaysThatLegsPrice(t *testing.T) {
	outcome, err := GradeParlay(gradedParlay(t, ResultWin, ResultPush), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	single, err := ledger.NewAmericanOdds(-110)
	if err != nil {
		t.Fatal(err)
	}
	want, err := single.Profit(ledger.Money{Cents: 10_000, Currency: ledger.CAD})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result != ResultWin || outcome.Profit.Cents != want.Cents {
		t.Fatalf("one surviving leg paid %d, want the straight -110 price %d", outcome.Profit.Cents, want.Cents)
	}
}

// Everything pushed: the stake comes back and nobody is up.
func TestGradeParlayReturnsTheStakeWhenEveryLegDropsOut(t *testing.T) {
	outcome, err := GradeParlay(gradedParlay(t, ResultPush, ResultVoid), DefaultParlayJuiceBasisPoints)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result != ResultPush || outcome.Profit.Cents != 0 || outcome.Returns.Cents != outcome.Stake.Cents {
		t.Fatalf("all-push outcome = %+v, want the stake back", outcome)
	}
}

// A parlay with a leg still open must not grade at all.
func TestGradeParlayRefusesWhileALegIsUnresolved(t *testing.T) {
	parlay := gradedParlay(t, ResultWin, ResultWin)
	parlay.Legs[1].Result = ""
	if _, err := GradeParlay(parlay, DefaultParlayJuiceBasisPoints); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid while a leg is open", err)
	}
}
