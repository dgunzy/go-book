package betting

import (
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// The three-way tie these tests are built around is the one the book actually
// hit: three members tied for most points, and each of them holds a winning
// ticket that can only be paid a third of the way.

const (
	deadHeatSelectionAID ID = "20000000-0000-4000-8000-000000000001"
	deadHeatSelectionBID ID = "20000000-0000-4000-8000-000000000002"
	deadHeatSelectionCID ID = "20000000-0000-4000-8000-000000000003"
	deadHeatSelectionDID ID = "20000000-0000-4000-8000-000000000004"
	deadHeatWagerAID     ID = "20000000-0000-4000-8000-000000000005"
	deadHeatWagerBID     ID = "20000000-0000-4000-8000-000000000006"
	deadHeatWagerDID     ID = "20000000-0000-4000-8000-000000000007"
)

func deadHeatSelections() []Selection {
	return []Selection{
		{ID: deadHeatSelectionAID, MarketID: settleMarketID, Key: "sel-a", DisplayTerms: "A most points", OfferedAmericanOdds: 150, Active: true},
		{ID: deadHeatSelectionBID, MarketID: settleMarketID, Key: "sel-b", DisplayTerms: "B most points", OfferedAmericanOdds: 150, Active: true},
		{ID: deadHeatSelectionCID, MarketID: settleMarketID, Key: "sel-c", DisplayTerms: "C most points", OfferedAmericanOdds: 150, Active: true},
		{ID: deadHeatSelectionDID, MarketID: settleMarketID, Key: "sel-d", DisplayTerms: "D most points", OfferedAmericanOdds: -110, Active: true},
	}
}

// deadHeatCommand grades a four-runner market in which A, B and C tie and D
// loses outright.
func deadHeatCommand() SettleMarketCommand {
	refs := SettlementAccountRefs{UserFundingAccountID: "user-cash", EscrowAccountID: "escrow", HouseClearingAccountID: "house"}
	return SettleMarketCommand{
		Market:     closedMarket(),
		Selections: deadHeatSelections(),
		Outcome: MarketOutcome{
			deadHeatSelectionAID: ResultWin,
			deadHeatSelectionBID: ResultWin,
			deadHeatSelectionCID: ResultWin,
			deadHeatSelectionDID: ResultLoss,
		},
		Wagers: []Wager{
			acceptedWager(deadHeatWagerAID, deadHeatSelectionAID, 3000, 150),
			acceptedWager(deadHeatWagerBID, deadHeatSelectionBID, 3000, 150),
			acceptedWager(deadHeatWagerDID, deadHeatSelectionDID, 1000, -110),
		},
		Refs: map[ID]SettlementAccountRefs{deadHeatWagerAID: refs, deadHeatWagerBID: refs, deadHeatWagerDID: refs},
		WagerSettlementIDs: idMap(
			deadHeatWagerAID, "20000000-0000-4000-8000-0000000000f1",
			deadHeatWagerBID, "20000000-0000-4000-8000-0000000000f2",
			deadHeatWagerDID, "20000000-0000-4000-8000-0000000000f3",
		),
		WagerEventIDs: idMap(
			deadHeatWagerAID, "20000000-0000-4000-8000-0000000000e1",
			deadHeatWagerBID, "20000000-0000-4000-8000-0000000000e2",
			deadHeatWagerDID, "20000000-0000-4000-8000-0000000000e3",
		),
		SettlementID: settlementRowID, Version: 1, Actor: settleActorID,
		OccurredAt: time.Date(2027, time.May, 13, 9, 0, 0, 0, time.UTC), MarketEventID: marketEventRowID,
		DeadHeat: true,
	}
}

func settlementsByWager(t *testing.T, result SettleMarketResult) map[ID]WagerSettlement {
	t.Helper()
	byWager := make(map[ID]WagerSettlement, len(result.Settlements))
	for _, settlement := range result.Settlements {
		if err := settlement.Transaction.Validate(); err != nil {
			t.Fatalf("settlement transaction for %s Validate() error = %v", settlement.WagerID, err)
		}
		byWager[settlement.WagerID] = settlement
	}
	return byWager
}

func TestSettleMarketDeadHeatPaysAThirdOfTheStake(t *testing.T) {
	t.Parallel()
	result, err := SettleMarket(deadHeatCommand())
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if result.DeadHeatDivisor != 3 {
		t.Fatalf("dead heat divisor = %d, want 3 derived from the three winning selections", result.DeadHeatDivisor)
	}

	byWager := settlementsByWager(t, result)
	// $30 at +150 with a three-way tie: $10 rides and wins $15, so $25 comes
	// back and the other $20 of stake is lost.
	win := byWager[deadHeatWagerAID]
	if win.Result != ResultWin {
		t.Fatalf("dead heat result = %s, want win", win.Result)
	}
	if win.Profit.Cents != 1500 || win.Returned.Cents != 2500 || win.Stake.Cents != 3000 {
		t.Fatalf("dead heat settlement = %+v, want stake 3000 profit 1500 returned 2500", win)
	}
	if win.DeadHeatDivisor != 3 {
		t.Fatalf("settlement divisor = %d, want 3", win.DeadHeatDivisor)
	}
	// Full stake out of escrow, $25 to the member, and the house nets the $20
	// lost stake against the $15 profit it owes.
	if got := postingFor(t, win.Transaction, "escrow"); got != -3000 {
		t.Fatalf("escrow posting = %d, want -3000", got)
	}
	if got := postingFor(t, win.Transaction, "user-cash"); got != 2500 {
		t.Fatalf("member posting = %d, want 2500", got)
	}
	if got := postingFor(t, win.Transaction, "house"); got != 500 {
		t.Fatalf("house posting = %d, want 500 (2000 lost stake less 1500 profit)", got)
	}
}

// The loser in a dead-heat market is still a plain loser: nothing about
// somebody else's tie changes what they staked or what they get back.
func TestSettleMarketDeadHeatLeavesLosersWhole(t *testing.T) {
	t.Parallel()
	result, err := SettleMarket(deadHeatCommand())
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	loss := settlementsByWager(t, result)[deadHeatWagerDID]
	if loss.Result != ResultLoss || loss.Returned.Cents != 0 || loss.Profit.Cents != 0 {
		t.Fatalf("loss settlement = %+v, want a plain loss", loss)
	}
	if loss.DeadHeatDivisor != 1 {
		t.Fatalf("loss divisor = %d, want 1: a dead heat only ever divides a win", loss.DeadHeatDivisor)
	}
}

// Not declaring the tie must leave settlement exactly as it was, because a
// market may grade several winners without any tie at all.
func TestSettleMarketWithoutDeadHeatPaysEveryWinnerInFull(t *testing.T) {
	t.Parallel()
	command := deadHeatCommand()
	command.DeadHeat = false
	result, err := SettleMarket(command)
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if result.DeadHeatDivisor != 1 {
		t.Fatalf("divisor = %d, want 1 when no dead heat is declared", result.DeadHeatDivisor)
	}
	win := settlementsByWager(t, result)[deadHeatWagerAID]
	if win.Profit.Cents != 4500 || win.Returned.Cents != 7500 {
		t.Fatalf("undeclared settlement = %+v, want the full 4500 profit and 7500 returned", win)
	}
}

func TestSettleMarketDeadHeatNeedsTwoWinners(t *testing.T) {
	t.Parallel()
	command := deadHeatCommand()
	command.Outcome[deadHeatSelectionBID] = ResultLoss
	command.Outcome[deadHeatSelectionCID] = ResultLoss
	if _, err := SettleMarket(command); err == nil {
		t.Fatal("SettleMarket() error = nil, want a refusal to call one winner a dead heat")
	}
}

func TestSettleMarketDivisorWithoutDeadHeatIsRefused(t *testing.T) {
	t.Parallel()
	command := deadHeatCommand()
	command.DeadHeat = false
	command.DeadHeatDivisor = 3
	if _, err := SettleMarket(command); err == nil {
		t.Fatal("SettleMarket() error = nil, want a refusal of a divisor with no dead heat declared")
	}
}

// A regrade replays the divisor the market recorded rather than counting
// winners again, which is what keeps a stranded wager paid like its peers.
func TestSettleMarketDeadHeatHonoursAnExplicitDivisor(t *testing.T) {
	t.Parallel()
	command := deadHeatCommand()
	command.DeadHeatDivisor = 2
	result, err := SettleMarket(command)
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if result.DeadHeatDivisor != 2 {
		t.Fatalf("divisor = %d, want the supplied 2", result.DeadHeatDivisor)
	}
	// $30 halved: $15 rides at +150 for $22.50 profit, returning $37.50.
	win := settlementsByWager(t, result)[deadHeatWagerAID]
	if win.Profit.Cents != 2250 || win.Returned.Cents != 3750 {
		t.Fatalf("half-stake settlement = %+v, want profit 2250 returned 3750", win)
	}
}

// The cent that will not divide by three stays with the house. Inventing it
// for one winner and not another is how a settlement stops balancing.
func TestDeadHeatSplitKeepsTheOddCentWithTheHouse(t *testing.T) {
	t.Parallel()
	stake := ledger.Money{Cents: 1000, Currency: ledger.CAD}
	winning, lost := deadHeatSplit(stake, 3)
	if winning.Cents != 333 || lost.Cents != 667 {
		t.Fatalf("split of 1000 three ways = %d/%d, want 333/667", winning.Cents, lost.Cents)
	}
	if winning.Cents+lost.Cents != stake.Cents {
		t.Fatalf("split does not conserve the stake: %d + %d != %d", winning.Cents, lost.Cents, stake.Cents)
	}
	whole, none := deadHeatSplit(stake, 1)
	if whole.Cents != 1000 || none.Cents != 0 {
		t.Fatalf("split at divisor 1 = %d/%d, want the whole stake riding", whole.Cents, none.Cents)
	}
}

// A stake too small to divide leaves nothing riding. It must still balance
// rather than emit an illegal zero posting.
func TestSettleMarketDeadHeatOnAStakeTooSmallToDivide(t *testing.T) {
	t.Parallel()
	command := deadHeatCommand()
	command.Wagers = []Wager{acceptedWager(deadHeatWagerAID, deadHeatSelectionAID, 2, 150)}
	result, err := SettleMarket(command)
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	win := settlementsByWager(t, result)[deadHeatWagerAID]
	if win.Returned.Cents != 0 || win.Profit.Cents != 0 {
		t.Fatalf("two-cent settlement = %+v, want nothing returned", win)
	}
	if len(win.Transaction.Postings) != 2 {
		t.Fatalf("postings = %d, want 2 with no zero posting", len(win.Transaction.Postings))
	}
}
