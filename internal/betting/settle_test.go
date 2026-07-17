package betting

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

const (
	settleMarketID   ID = "10000000-0000-4000-8000-000000000001"
	selectionWinID   ID = "10000000-0000-4000-8000-000000000002"
	selectionLossID  ID = "10000000-0000-4000-8000-000000000003"
	selectionPushID  ID = "10000000-0000-4000-8000-000000000004"
	selectionVoidID  ID = "10000000-0000-4000-8000-000000000005"
	wagerWinID       ID = "10000000-0000-4000-8000-000000000006"
	wagerLossID      ID = "10000000-0000-4000-8000-000000000007"
	wagerPushID      ID = "10000000-0000-4000-8000-000000000008"
	wagerVoidID      ID = "10000000-0000-4000-8000-000000000009"
	settlementRowID  ID = "10000000-0000-4000-8000-00000000000a"
	marketEventRowID ID = "10000000-0000-4000-8000-00000000000b"
	settleUserID     ID = "10000000-0000-4000-8000-00000000000c"
	settleActorID    ID = "10000000-0000-4000-8000-00000000000d"
)

func closedMarket() Market {
	closes := time.Date(2027, time.May, 12, 18, 0, 0, 0, time.UTC)
	return Market{
		ID: settleMarketID, Type: MarketMatch, MatchID: testMatchID,
		Title: "Team A vs Team B", State: MarketClosed, Currency: ledger.CAD,
		ClosesAt: closes,
	}
}

func fourWaySelections() []Selection {
	return []Selection{
		{ID: selectionWinID, MarketID: settleMarketID, Key: "sel-win", DisplayTerms: "wins", OfferedAmericanOdds: 150, Active: true},
		{ID: selectionLossID, MarketID: settleMarketID, Key: "sel-loss", DisplayTerms: "loses", OfferedAmericanOdds: -110, Active: true},
		{ID: selectionPushID, MarketID: settleMarketID, Key: "sel-push", DisplayTerms: "pushes", OfferedAmericanOdds: 100, Active: true},
		{ID: selectionVoidID, MarketID: settleMarketID, Key: "sel-void", DisplayTerms: "voids", OfferedAmericanOdds: -100, Active: true},
	}
}

func acceptedWager(id, selectionID ID, stakeCents int64, odds ledger.AmericanOdds) Wager {
	stake := ledger.Money{Cents: stakeCents, Currency: ledger.CAD}
	profit, err := odds.Profit(stake)
	if err != nil {
		panic(err)
	}
	return Wager{
		ID: id, UserID: settleUserID, MarketID: settleMarketID, SelectionID: selectionID,
		FundingAccountType: FundingUserCash, Stake: stake, AcceptedOdds: odds,
		AcceptedTerms: "snapshot terms", PotentialProfit: profit, State: WagerAccepted,
		IdempotencyKey: "place:" + string(id), PlacedAt: time.Now().UTC(),
	}
}

func fourWayWagers() []Wager {
	return []Wager{
		acceptedWager(wagerWinID, selectionWinID, 3333, 150),
		acceptedWager(wagerLossID, selectionLossID, 1000, -110),
		acceptedWager(wagerPushID, selectionPushID, 500, 100),
		acceptedWager(wagerVoidID, selectionVoidID, 200, -100),
	}
}

func fourWayOutcome() MarketOutcome {
	return MarketOutcome{
		selectionWinID:  ResultWin,
		selectionLossID: ResultLoss,
		selectionPushID: ResultPush,
		selectionVoidID: ResultVoid,
	}
}

func fourWayRefs() map[ID]SettlementAccountRefs {
	refs := SettlementAccountRefs{UserFundingAccountID: "user-cash", EscrowAccountID: "escrow", HouseClearingAccountID: "house"}
	return map[ID]SettlementAccountRefs{
		wagerWinID: refs, wagerLossID: refs, wagerPushID: refs, wagerVoidID: refs,
	}
}

func idMap(ids ...ID) map[ID]ID {
	out := make(map[ID]ID, len(ids)/2)
	for i := 0; i+1 < len(ids); i += 2 {
		out[ids[i]] = ids[i+1]
	}
	return out
}

func fourWaySettlementIDs() map[ID]ID {
	return idMap(
		wagerWinID, "10000000-0000-4000-8000-0000000000f1",
		wagerLossID, "10000000-0000-4000-8000-0000000000f2",
		wagerPushID, "10000000-0000-4000-8000-0000000000f3",
		wagerVoidID, "10000000-0000-4000-8000-0000000000f4",
	)
}

func fourWayEventIDs() map[ID]ID {
	return idMap(
		wagerWinID, "10000000-0000-4000-8000-0000000000e1",
		wagerLossID, "10000000-0000-4000-8000-0000000000e2",
		wagerPushID, "10000000-0000-4000-8000-0000000000e3",
		wagerVoidID, "10000000-0000-4000-8000-0000000000e4",
	)
}

func baseSettleCommand() SettleMarketCommand {
	return SettleMarketCommand{
		Market: closedMarket(), Selections: fourWaySelections(), Outcome: fourWayOutcome(),
		Wagers: fourWayWagers(), Refs: fourWayRefs(),
		WagerSettlementIDs: fourWaySettlementIDs(), WagerEventIDs: fourWayEventIDs(),
		SettlementID: settlementRowID, Version: 1, Actor: settleActorID,
		OccurredAt: time.Date(2027, time.May, 13, 9, 0, 0, 0, time.UTC), MarketEventID: marketEventRowID,
	}
}

func TestSettleMarketWinLossPushVoid(t *testing.T) {
	t.Parallel()
	result, err := SettleMarket(baseSettleCommand())
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if result.Market.State != MarketSettled {
		t.Fatalf("market state = %s, want settled", result.Market.State)
	}
	if len(result.Settlements) != 4 || len(result.Wagers) != 4 || len(result.WagerEvents) != 4 {
		t.Fatalf("settlement counts = %d/%d/%d, want 4/4/4", len(result.Settlements), len(result.Wagers), len(result.WagerEvents))
	}
	if err := result.MarketEvent.Validate(); err != nil {
		t.Fatalf("market event Validate() error = %v", err)
	}

	byWager := make(map[ID]WagerSettlement, len(result.Settlements))
	for _, settlement := range result.Settlements {
		byWager[settlement.WagerID] = settlement
		if err := settlement.Transaction.Validate(); err != nil {
			t.Fatalf("settlement transaction for %s Validate() error = %v", settlement.WagerID, err)
		}
	}
	byWagerState := make(map[ID]WagerState, len(result.Wagers))
	for _, wager := range result.Wagers {
		byWagerState[wager.ID] = wager.State
	}

	win := byWager[wagerWinID]
	if win.Result != ResultWin || win.Profit.Cents != 5000 || win.Returned.Cents != 8333 {
		t.Fatalf("win settlement = %+v, want profit 5000 returned 8333", win)
	}
	if len(win.Transaction.Postings) != 3 || win.Transaction.Type != ledger.TransactionWagerWin {
		t.Fatalf("win transaction = %+v, want 3 postings of type wager_win", win.Transaction)
	}
	if byWagerState[wagerWinID] != WagerSettled {
		t.Fatalf("win wager state = %s, want settled", byWagerState[wagerWinID])
	}

	loss := byWager[wagerLossID]
	if loss.Result != ResultLoss || loss.Profit.Cents != 0 || loss.Returned.Cents != 0 {
		t.Fatalf("loss settlement = %+v, want zero profit and returned", loss)
	}
	if len(loss.Transaction.Postings) != 2 || loss.Transaction.Type != ledger.TransactionWagerLoss {
		t.Fatalf("loss transaction = %+v, want 2 postings of type wager_loss", loss.Transaction)
	}
	if byWagerState[wagerLossID] != WagerSettled {
		t.Fatalf("loss wager state = %s, want settled", byWagerState[wagerLossID])
	}

	push := byWager[wagerPushID]
	if push.Result != ResultPush || push.Profit.Cents != 0 || push.Returned.Cents != 500 {
		t.Fatalf("push settlement = %+v, want returned 500", push)
	}
	if len(push.Transaction.Postings) != 2 || push.Transaction.Type != ledger.TransactionWagerRefund {
		t.Fatalf("push transaction = %+v, want 2 postings of type wager_refund", push.Transaction)
	}
	if byWagerState[wagerPushID] != WagerSettled {
		t.Fatalf("push wager state = %s, want settled", byWagerState[wagerPushID])
	}

	void := byWager[wagerVoidID]
	if void.Result != ResultVoid || void.Profit.Cents != 0 || void.Returned.Cents != 200 {
		t.Fatalf("void settlement = %+v, want returned 200", void)
	}
	if byWagerState[wagerVoidID] != WagerVoided {
		t.Fatalf("void wager state = %s, want voided", byWagerState[wagerVoidID])
	}
}

func TestSettleMarketRequiresClosedOrPendingState(t *testing.T) {
	t.Parallel()
	command := baseSettleCommand()
	command.Market.State = MarketOpen
	if _, err := SettleMarket(command); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("SettleMarket() from open error = %v, want ErrInvalidTransition", err)
	}

	pending := baseSettleCommand()
	pending.Market.State = MarketSettlementPending
	if _, err := SettleMarket(pending); err != nil {
		t.Fatalf("SettleMarket() from settlement_pending error = %v", err)
	}
}

func TestSettleMarketOutcomeMustCoverEverySelectionExactlyOnce(t *testing.T) {
	t.Parallel()

	missing := baseSettleCommand()
	delete(missing.Outcome, selectionVoidID)
	if _, err := SettleMarket(missing); !errors.Is(err, ErrIncompleteOutcome) {
		t.Fatalf("missing selection error = %v, want ErrIncompleteOutcome", err)
	}

	extra := baseSettleCommand()
	extra.Outcome["10000000-0000-4000-8000-0000000000ff"] = ResultWin
	if _, err := SettleMarket(extra); !errors.Is(err, ErrIncompleteOutcome) {
		t.Fatalf("extra selection error = %v, want ErrIncompleteOutcome", err)
	}

	badResult := baseSettleCommand()
	badResult.Outcome[selectionWinID] = "half-win"
	if _, err := SettleMarket(badResult); err == nil {
		t.Fatal("invalid result value unexpectedly accepted")
	}
}

func TestSettleMarketDuplicateVersionIsDeterministic(t *testing.T) {
	t.Parallel()
	first, err := SettleMarket(baseSettleCommand())
	if err != nil {
		t.Fatalf("first SettleMarket() error = %v", err)
	}
	second, err := SettleMarket(baseSettleCommand())
	if err != nil {
		t.Fatalf("second SettleMarket() error = %v", err)
	}
	if len(first.Settlements) != len(second.Settlements) {
		t.Fatalf("settlement counts differ: %d vs %d", len(first.Settlements), len(second.Settlements))
	}
	for i := range first.Settlements {
		a, b := first.Settlements[i], second.Settlements[i]
		if a.WagerID != b.WagerID || a.Result != b.Result {
			t.Fatalf("settlement %d differs: %+v vs %+v", i, a, b)
		}
		if a.Transaction.IdempotencyKey != b.Transaction.IdempotencyKey {
			t.Fatalf("idempotency keys differ: %q vs %q", a.Transaction.IdempotencyKey, b.Transaction.IdempotencyKey)
		}
		if a.Stake != b.Stake || a.Profit != b.Profit || a.Returned != b.Returned {
			t.Fatalf("amounts differ for %s: %+v vs %+v", a.WagerID, a, b)
		}
		if len(a.Transaction.Postings) != len(b.Transaction.Postings) {
			t.Fatalf("posting counts differ for %s", a.WagerID)
		}
		for p := range a.Transaction.Postings {
			if a.Transaction.Postings[p] != b.Transaction.Postings[p] {
				t.Fatalf("posting %d differs for %s: %+v vs %+v", p, a.WagerID, a.Transaction.Postings[p], b.Transaction.Postings[p])
			}
		}
	}

	// A different version must change every wager's idempotency key so that
	// a re-grade after correction cannot collide with the original payout.
	repriced := baseSettleCommand()
	repriced.Version = 2
	third, err := SettleMarket(repriced)
	if err != nil {
		t.Fatalf("re-versioned SettleMarket() error = %v", err)
	}
	for i := range first.Settlements {
		if first.Settlements[i].Transaction.IdempotencyKey == third.Settlements[i].Transaction.IdempotencyKey {
			t.Fatalf("version 1 and version 2 idempotency keys collided: %q", first.Settlements[i].Transaction.IdempotencyKey)
		}
	}
}

func TestSettleWagerOddsBoundariesAndRounding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		stakeCents   int64
		odds         ledger.AmericanOdds
		wantProfit   int64
		wantReturned int64
	}{
		{"even positive +100", 1000, 100, 1000, 2000},
		{"even negative -100", 1000, -100, 1000, 2000},
		{"favourite -110 stake 1000", 1000, -110, 909, 1909},
		{"half cent rounds up +150 stake 3333", 3333, 150, 5000, 8333},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			wager := acceptedWager("10000000-0000-4000-8000-0000000000aa", selectionWinID, test.stakeCents, test.odds)
			refs := SettlementAccountRefs{UserFundingAccountID: "user-cash", EscrowAccountID: "escrow", HouseClearingAccountID: "house"}
			settlement, updated, envelope, err := settleWager(wager, ResultWin, refs, settlementRowID, "10000000-0000-4000-8000-0000000000bb", 1, settleActorID, time.Now().UTC(), "10000000-0000-4000-8000-0000000000cc")
			if err != nil {
				t.Fatalf("settleWager() error = %v", err)
			}
			if settlement.Profit.Cents != test.wantProfit || settlement.Returned.Cents != test.wantReturned {
				t.Fatalf("settlement = %+v, want profit %d returned %d", settlement, test.wantProfit, test.wantReturned)
			}
			if err := settlement.Transaction.Validate(); err != nil {
				t.Fatalf("transaction Validate() error = %v", err)
			}
			if updated.State != WagerSettled {
				t.Fatalf("wager state = %s, want settled", updated.State)
			}
			if err := envelope.Validate(); err != nil {
				t.Fatalf("envelope Validate() error = %v", err)
			}
		})
	}
}

func TestSettleWagerRejectsOverflowingPayout(t *testing.T) {
	t.Parallel()
	stake := ledger.Money{Cents: math.MaxInt64 / 10, Currency: ledger.CAD}
	wager := Wager{
		ID: "10000000-0000-4000-8000-0000000000dd", UserID: settleUserID, MarketID: settleMarketID,
		SelectionID: selectionWinID, FundingAccountType: FundingUserCash, Stake: stake,
		AcceptedOdds: 5000, AcceptedTerms: "snapshot terms",
		PotentialProfit: ledger.Money{Currency: ledger.CAD}, State: WagerAccepted,
		IdempotencyKey: "place:overflow", PlacedAt: time.Now().UTC(),
	}
	refs := SettlementAccountRefs{UserFundingAccountID: "user-cash", EscrowAccountID: "escrow", HouseClearingAccountID: "house"}
	_, _, _, err := settleWager(wager, ResultWin, refs, settlementRowID, "10000000-0000-4000-8000-0000000000ee", 1, settleActorID, time.Now().UTC(), "10000000-0000-4000-8000-0000000000ff")
	if !errors.Is(err, ledger.ErrAmountOverflow) {
		t.Fatalf("overflowing settlement error = %v, want ErrAmountOverflow", err)
	}
}

func TestVoidMarketRefundsAcceptedWagers(t *testing.T) {
	t.Parallel()
	command := VoidMarketCommand{
		Market: closedMarket(), Wagers: fourWayWagers(), Refs: fourWayRefs(),
		WagerSettlementIDs: fourWaySettlementIDs(), WagerEventIDs: fourWayEventIDs(),
		SettlementID: settlementRowID, Version: 1, Actor: settleActorID, Reason: "match cancelled",
		OccurredAt: time.Date(2027, time.May, 13, 9, 0, 0, 0, time.UTC), MarketEventID: marketEventRowID,
	}
	result, err := VoidMarket(command)
	if err != nil {
		t.Fatalf("VoidMarket() error = %v", err)
	}
	if result.Market.State != MarketVoided {
		t.Fatalf("market state = %s, want voided", result.Market.State)
	}
	if len(result.Settlements) != 4 {
		t.Fatalf("settlements = %d, want 4", len(result.Settlements))
	}
	for _, settlement := range result.Settlements {
		if settlement.Result != ResultVoid {
			t.Fatalf("settlement result = %s, want void", settlement.Result)
		}
		if settlement.Returned != settlement.Stake {
			t.Fatalf("void settlement returned %+v, want stake %+v", settlement.Returned, settlement.Stake)
		}
		if err := settlement.Transaction.Validate(); err != nil {
			t.Fatalf("void transaction Validate() error = %v", err)
		}
		if settlement.Transaction.Type != ledger.TransactionWagerRefund {
			t.Fatalf("void transaction type = %s, want wager_refund", settlement.Transaction.Type)
		}
	}
	for _, wager := range result.Wagers {
		if wager.State != WagerVoided {
			t.Fatalf("wager %s state = %s, want voided", wager.ID, wager.State)
		}
	}
}

func TestVoidMarketRequiresReason(t *testing.T) {
	t.Parallel()
	command := VoidMarketCommand{
		Market: closedMarket(), Wagers: fourWayWagers(), Refs: fourWayRefs(),
		WagerSettlementIDs: fourWaySettlementIDs(), WagerEventIDs: fourWayEventIDs(),
		SettlementID: settlementRowID, Version: 1, Actor: settleActorID,
		OccurredAt: time.Date(2027, time.May, 13, 9, 0, 0, 0, time.UTC), MarketEventID: marketEventRowID,
	}
	if _, err := VoidMarket(command); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("VoidMarket() error = %v, want ErrReasonRequired", err)
	}
}

func TestVoidMarketAllowsOpenMarket(t *testing.T) {
	t.Parallel()
	market := openMarket()
	market.ID = settleMarketID
	command := VoidMarketCommand{
		Market: market, Wagers: nil, Refs: map[ID]SettlementAccountRefs{},
		WagerSettlementIDs: map[ID]ID{}, WagerEventIDs: map[ID]ID{},
		SettlementID: settlementRowID, Version: 1, Actor: settleActorID, Reason: "opened in error",
		OccurredAt: time.Date(2027, time.May, 13, 9, 0, 0, 0, time.UTC), MarketEventID: marketEventRowID,
	}
	result, err := VoidMarket(command)
	if err != nil {
		t.Fatalf("VoidMarket() error = %v", err)
	}
	if result.Market.State != MarketVoided {
		t.Fatalf("market state = %s, want voided", result.Market.State)
	}
}

func TestSettleWagerZeroProfitWinReturnsStakeOnly(t *testing.T) {
	t.Parallel()
	wager := acceptedWager(wagerWinID, selectionWinID, 4, -1000)
	refs := SettlementAccountRefs{UserFundingAccountID: "user-cash", EscrowAccountID: "escrow", HouseClearingAccountID: "house"}
	at := time.Date(2027, time.May, 13, 9, 0, 0, 0, time.UTC)
	settlement, settled, _, err := settleWager(wager, ResultWin, refs, settlementRowID, "10000000-0000-4000-8000-0000000000f1", 1, settleActorID, at, "10000000-0000-4000-8000-0000000000e1")
	if err != nil {
		t.Fatalf("settleWager() error = %v", err)
	}
	if settlement.Profit.Cents != 0 || settlement.Returned.Cents != 4 || settled.State != WagerSettled {
		t.Fatalf("zero-profit win settlement = %+v state = %s", settlement, settled.State)
	}
	if len(settlement.Transaction.Postings) != 2 {
		t.Fatalf("zero-profit win must not post a zero house amount: %+v", settlement.Transaction.Postings)
	}
	if err := settlement.Transaction.Validate(); err != nil {
		t.Fatalf("zero-profit win transaction is invalid: %v", err)
	}
}

func TestSettleMarketEnvelopeVersionsFollowSettlementVersion(t *testing.T) {
	t.Parallel()
	command := baseSettleCommand()
	command.Market.State = MarketSettlementPending
	command.Version = 2
	result, err := SettleMarket(command)
	if err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}
	if result.MarketEvent.AggregateVersion != 2 {
		t.Fatalf("market event aggregate version = %d, want 2", result.MarketEvent.AggregateVersion)
	}
	for _, envelope := range result.WagerEvents {
		if envelope.AggregateVersion != 2 {
			t.Fatalf("wager event aggregate version = %d, want the settlement version so corrected settlements are not dropped by outbox uniqueness", envelope.AggregateVersion)
		}
	}
}
