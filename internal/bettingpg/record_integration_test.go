package bettingpg

import (
	"context"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
)

// TestWagerRecordReportsThreePrices proves the record query maps against the
// live schema and reports the opening, taken, and closing prices for a real
// wager, including once its market has stopped taking action.
func TestWagerRecordReportsThreePrices(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)

	rows, err := store.ListWagerRecord(ctx, 0)
	if err != nil {
		t.Fatalf("ListWagerRecord() error = %v", err)
	}
	var found *WagerRecordRow
	for i := range rows {
		if rows[i].ID == string(wager.ID) {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("the accepted wager is missing from the record")
	}
	// The fixture posts both selections at -110 and nothing has repriced it,
	// so all three prices agree.
	if found.OpeningOdds != -110 || found.TakenOdds != -110 || found.ClosingOdds != -110 {
		t.Fatalf("prices = opening %d, taken %d, closing %d; want -110 for each",
			found.OpeningOdds, found.TakenOdds, found.ClosingOdds)
	}
	if found.TakenOdds != wager.AcceptedOdds {
		t.Fatalf("taken price = %d, want the wager's accepted odds %d", found.TakenOdds, wager.AcceptedOdds)
	}
	if found.Stake.Cents != 1_000 || found.MemberName == "" || found.SelectionTerms == "" {
		t.Fatalf("record row = %+v", *found)
	}
	if found.State != betting.WagerAccepted || found.Result != "" {
		t.Fatalf("state = %q, result = %q; want an accepted, ungraded wager", found.State, found.Result)
	}
	// While the market takes action its price is not a closing line.
	if found.MarketClosed() {
		t.Fatal("an open market must not report a final closing line")
	}

	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	rows, err = store.ListWagerRecord(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if rows[i].ID == string(wager.ID) && !rows[i].MarketClosed() {
			t.Fatal("a closed market should report its price as the closing line")
		}
	}
}

// The record covers settled wagers, which the approval queue never shows.
func TestWagerRecordIncludesSettledWagersWithTheirResult(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "graded by hand for the record test",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultLoss, f.SelectionBID: betting.ResultWin,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}

	rows, err := store.ListWagerRecord(ctx, 0)
	if err != nil {
		t.Fatalf("ListWagerRecord() error = %v", err)
	}
	for _, row := range rows {
		if row.ID != string(wager.ID) {
			continue
		}
		if row.State != betting.WagerSettled || row.Result != betting.ResultLoss {
			t.Fatalf("settled row = state %q, result %q; want settled/loss", row.State, row.Result)
		}
		if !row.MarketClosed() {
			t.Fatal("a settled market's price is a closing line")
		}
		return
	}
	t.Fatal("the settled wager is missing from the record")
}
