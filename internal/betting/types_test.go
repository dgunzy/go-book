package betting

import (
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

func TestMarketStateTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from  MarketState
		to    MarketState
		allow bool
	}{
		{MarketDraft, MarketOpen, true},
		{MarketDraft, MarketCancelled, true},
		{MarketDraft, MarketClosed, false},
		{MarketDraft, MarketSettled, false},
		{MarketOpen, MarketClosed, true},
		{MarketOpen, MarketCancelled, true},
		{MarketOpen, MarketVoided, false},
		{MarketOpen, MarketDraft, false},
		{MarketClosed, MarketVoided, true},
		{MarketClosed, MarketSettlementPending, true},
		{MarketClosed, MarketSettled, false},
		{MarketClosed, MarketOpen, false},
		{MarketSettlementPending, MarketSettled, true},
		{MarketSettlementPending, MarketVoided, true},
		{MarketSettlementPending, MarketClosed, false},
		{MarketSettled, MarketVoided, false},
		{MarketCancelled, MarketOpen, false},
	}
	for _, test := range tests {
		got := test.from.CanTransitionTo(test.to)
		if got != test.allow {
			t.Errorf("%s.CanTransitionTo(%s) = %v, want %v", test.from, test.to, got, test.allow)
		}
	}
}

func TestWagerStateTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from  WagerState
		to    WagerState
		allow bool
	}{
		{WagerPending, WagerAccepted, true},
		{WagerPending, WagerRejected, true},
		{WagerPending, WagerSettled, false},
		{WagerPending, WagerVoided, false},
		{WagerAccepted, WagerSettled, true},
		{WagerAccepted, WagerVoided, true},
		{WagerAccepted, WagerPending, false},
		{WagerAccepted, WagerRejected, false},
		{WagerRejected, WagerAccepted, false},
		{WagerSettled, WagerVoided, false},
		{WagerVoided, WagerSettled, false},
	}
	for _, test := range tests {
		got := test.from.CanTransitionTo(test.to)
		if got != test.allow {
			t.Errorf("%s.CanTransitionTo(%s) = %v, want %v", test.from, test.to, got, test.allow)
		}
	}
}

func TestStateValidate(t *testing.T) {
	t.Parallel()
	if err := MarketState("bogus").Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("MarketState.Validate() error = %v, want ErrInvalid", err)
	}
	if err := WagerState("bogus").Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("WagerState.Validate() error = %v, want ErrInvalid", err)
	}
	if err := SettlementResult("bogus").Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SettlementResult.Validate() error = %v, want ErrInvalid", err)
	}
	if err := MarketType("bogus").Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("MarketType.Validate() error = %v, want ErrInvalid", err)
	}
	if err := FundingAccountType("bogus").Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("FundingAccountType.Validate() error = %v, want ErrInvalid", err)
	}
}

func testMarketValue(t *testing.T) Market {
	t.Helper()
	closes := time.Date(2027, time.May, 12, 18, 0, 0, 0, time.UTC)
	return Market{
		ID: "market-1", Type: MarketMatch, MatchID: "match-1",
		Title: "Team A vs Team B", State: MarketOpen, Currency: ledger.CAD,
		OpensAt: closes.Add(-48 * time.Hour), ClosesAt: closes,
	}
}

func testSelectionValue(t *testing.T, marketID ID, odds int32) Selection {
	t.Helper()
	return Selection{
		ID: "selection-1", MarketID: marketID, Key: "team-a-win",
		DisplayTerms: "Team A to win the match", OfferedAmericanOdds: ledger.AmericanOdds(odds), Active: true,
	}
}

func TestMarketValidate(t *testing.T) {
	t.Parallel()

	valid := testMarketValue(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid market Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(Market) Market
	}{
		{"missing id", func(m Market) Market { m.ID = ""; return m }},
		{"match type without match id", func(m Market) Market { m.MatchID = ""; return m }},
		{"non-match type with match id", func(m Market) Market { m.Type = MarketFuture; return m }},
		{"blank title", func(m Market) Market { m.Title = "  "; return m }},
		{"missing closes at", func(m Market) Market { m.ClosesAt = time.Time{}; return m }},
		{"opens after closes", func(m Market) Market { m.OpensAt = m.ClosesAt.Add(time.Hour); return m }},
		{"invalid currency", func(m Market) Market { m.Currency = "usd"; return m }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.mutate(valid).Validate(); !errors.Is(err, ErrInvalid) && err == nil {
				t.Fatalf("Validate() error = %v, want an error", err)
			}
		})
	}
}

func TestSelectionValidate(t *testing.T) {
	t.Parallel()
	valid := testSelectionValue(t, "market-1", 150)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid selection Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(Selection) Selection
	}{
		{"missing market id", func(s Selection) Selection { s.MarketID = ""; return s }},
		{"bad key", func(s Selection) Selection { s.Key = "Team A"; return s }},
		{"blank display terms", func(s Selection) Selection { s.DisplayTerms = ""; return s }},
		{"odds in ambiguous range", func(s Selection) Selection { s.OfferedAmericanOdds = 50; return s }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.mutate(valid).Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
}

func TestWagerValidate(t *testing.T) {
	t.Parallel()
	valid := Wager{
		ID: "wager-1", UserID: "user-1", MarketID: "market-1", SelectionID: "selection-1",
		FundingAccountType: FundingUserCash, Stake: ledger.Money{Cents: 1000, Currency: ledger.CAD},
		AcceptedOdds: 150, AcceptedTerms: "Team A to win the match",
		PotentialProfit: ledger.Money{Cents: 1500, Currency: ledger.CAD},
		State:           WagerPending, IdempotencyKey: "idem-1", PlacedAt: time.Now().UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid wager Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(Wager) Wager
	}{
		{"zero stake", func(w Wager) Wager { w.Stake.Cents = 0; return w }},
		{"negative stake", func(w Wager) Wager { w.Stake.Cents = -100; return w }},
		{"invalid odds", func(w Wager) Wager { w.AcceptedOdds = 0; return w }},
		{"blank terms", func(w Wager) Wager { w.AcceptedTerms = ""; return w }},
		{"negative profit", func(w Wager) Wager { w.PotentialProfit.Cents = -1; return w }},
		{"blank idempotency key", func(w Wager) Wager { w.IdempotencyKey = " "; return w }},
		{"bad funding account", func(w Wager) Wager { w.FundingAccountType = "bank"; return w }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.mutate(valid).Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
}
