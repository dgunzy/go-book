package privatepg

import (
	"context"
	"strings"
	"testing"
	"time"
)

// pulseScript builds the four scripted calls BookPulse makes, in order.
func pulseScript(t *testing.T, now time.Time) *scriptedDB {
	t.Helper()
	return &scriptedDB{t: t, calls: []expectedCall{
		{kind: "row", contains: "house_clearing", args: []any{"CAD"}, row: fakeRow{values: []any{
			now,                // as of
			int64(25_000),      // house clearing balance
			int64(80_000),      // escrow
			int64(300_000),     // handle
			int64(30_000),      // pending stake
			int64(2), int64(1), // open markets, pending wagers
			int64(3), // accepted wagers
		}}},
		{kind: "query", contains: "FROM markets m", args: []any{"CAD"}, rows: rows(
			// Market 1: $500 on the favourite (pays $740), $200 on the dog (pays $596).
			[]any{"m-1", "Match 4", "open", now, "Bill, DC to win", int64(2), int64(50_000), int64(74_000)},
			[]any{"m-1", "Match 4", "open", now, "Alex, Mau to win", int64(1), int64(20_000), int64(59_600)},
			// Market 2 has no action at all.
			[]any{"m-2", "Match 5", "closed", now, "Side A", int64(0), int64(0), int64(0)},
			[]any{"m-2", "Match 5", "closed", now, "Side B", int64(0), int64(0), int64(0)},
		)},
		{kind: "query", contains: "w.state = 'accepted'", args: []any{openWagerLimit}, rows: rows(
			[]any{now, "user-dan", "Dan Guns", "Match 4", "Bill, DC to win", int32(-208), int64(30_000), int64(14_423), "CAD"},
		)},
		{kind: "query", contains: "transaction_type IN", args: []any(nil), rows: rows(
			[]any{"user-dan", "Dan Guns", int64(40_000), int64(6), int64(2), int64(1), int64(1), int64(120_000)},
			[]any{"user-bill", "Bill C", int64(-10_000), int64(1), int64(3), int64(0), int64(2), int64(60_000)},
		)},
		// Cash positions: Bill is down $150 and has not paid; the book still
		// owes Dan $300 of winnings.
		{kind: "query", contains: "account_type = 'user_cash'", args: []any{"CAD"}, rows: rows(
			[]any{"user-bill", "Bill C", int64(-15_000)},
			[]any{"user-dan", "Dan Guns", int64(30_000)},
		)},
	}}
}

func TestBookPulseComputesExposureAndSwing(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	db := pulseScript(t, now)
	reader, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	pulse, err := reader.BookPulse(context.Background())
	if err != nil {
		t.Fatalf("BookPulse() error = %v", err)
	}
	db.done()

	if pulse.HouseResult.Cents != 25_000 || pulse.Escrow.Cents != 80_000 || pulse.Handle.Cents != 300_000 {
		t.Fatalf("headline totals = %+v", pulse)
	}
	if pulse.HouseResult.Currency != bookCurrency {
		t.Fatalf("house result currency = %q, want %q", pulse.HouseResult.Currency, bookCurrency)
	}
	if pulse.OpenMarkets != 2 || pulse.PendingWagers != 1 || pulse.OpenWagerCount != 3 {
		t.Fatalf("counts = %+v", pulse)
	}

	if len(pulse.Exposure) != 2 {
		t.Fatalf("exposure markets = %d, want 2", len(pulse.Exposure))
	}
	match4 := pulse.Exposure[0]
	if match4.Market != "Match 4" || match4.Wagers != 3 || match4.Stake.Cents != 70_000 {
		t.Fatalf("market roll-up = %+v", match4)
	}
	// $700 is in the market. Favourite wins: 700 - 740 = -40. Dog wins: 700 - 596 = +104.
	if match4.Outcomes[0].HouseNet.Cents != -4_000 || match4.Outcomes[1].HouseNet.Cents != 10_400 {
		t.Fatalf("house net per outcome = %d, %d; want -4000, 10400",
			match4.Outcomes[0].HouseNet.Cents, match4.Outcomes[1].HouseNet.Cents)
	}
	if !match4.Outcomes[0].Worst || match4.Outcomes[1].Worst {
		t.Fatal("the favourite winning is the outcome that costs the house most; it should be the marked row")
	}
	// Match 5 has no action: every outcome nets zero, so no row costs the book
	// more than another and none may be marked. Marking one would paint an idle
	// market as the book's biggest risk.
	idle := pulse.Exposure[1]
	for i, outcome := range idle.Outcomes {
		if outcome.Worst {
			t.Errorf("idle market outcome %d (%s) is marked worst with nothing at stake", i, outcome.Selection)
		}
	}

	// The empty market swings zero either way, so it must not move the totals.
	if pulse.WorstCase.Cents != -4_000 || pulse.BestCase.Cents != 10_400 {
		t.Fatalf("swing = worst %d best %d, want -4000 and 10400", pulse.WorstCase.Cents, pulse.BestCase.Cents)
	}

	if len(pulse.OpenWagers) != 1 || pulse.OpenWagers[0].Member != "Dan Guns" || pulse.OpenWagers[0].Odds != -208 {
		t.Fatalf("open wagers = %+v", pulse.OpenWagers)
	}
	// Every row carries its member ID so the dashboard can link straight to
	// that member's book.
	if pulse.OpenWagers[0].MemberID != "user-dan" {
		t.Fatalf("open wager member ID = %q, want it carried through", pulse.OpenWagers[0].MemberID)
	}
	for _, player := range pulse.Players {
		if player.UserID == "" {
			t.Fatalf("player %q has no ID to link to", player.Name)
		}
	}
	for _, row := range pulse.Outstanding {
		if row.UserID == "" {
			t.Fatalf("outstanding row %q has no ID to link to", row.Name)
		}
	}

	// Owed and owing is a cash position, reported apart from the standings:
	// both sides are positive figures and neither touches player P&L.
	if pulse.OwedToBook.Cents != 15_000 || pulse.OwedByBook.Cents != 30_000 {
		t.Fatalf("owed to the book = %d, owed by the book = %d; want 15000 and 30000",
			pulse.OwedToBook.Cents, pulse.OwedByBook.Cents)
	}
	if len(pulse.Outstanding) != 2 || !pulse.Outstanding[0].Owes() || pulse.Outstanding[1].Owes() {
		t.Fatalf("outstanding = %+v", pulse.Outstanding)
	}
}

// The house result is the book's betting performance. Settling up posts against
// the same clearing account, so the query must filter to wager transactions or
// a member paying what they owe would look like the book losing money.
func TestBookPulseHouseResultCountsOnlyWagerTransactions(t *testing.T) {
	if !strings.Contains(bookTotalsSQL, "transaction_type IN ('wager_acceptance', 'wager_win', 'wager_loss', 'wager_refund')") {
		t.Fatal("the house result no longer filters to wager transactions; settle-ups would distort it")
	}
	if strings.Contains(bookTotalsSQL, "WHERE account_type = 'house_clearing' AND currency::text = $1), 0)::bigint,") {
		t.Fatal("the house result reads the raw clearing balance, which includes settled-up cash")
	}
}

func TestBookPulseScalesStandingsBars(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	db := pulseScript(t, now)
	reader, _ := New(db)

	pulse, err := reader.BookPulse(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(pulse.Players) != 2 {
		t.Fatalf("players = %d, want 2", len(pulse.Players))
	}
	leader, trailer := pulse.Players[0], pulse.Players[1]
	if !leader.Ahead() || trailer.Ahead() {
		t.Fatalf("Ahead() = %v, %v; want true, false", leader.Ahead(), trailer.Ahead())
	}
	// The largest absolute result sets the scale, so the leader fills the track
	// and the player down $100 against the leader's $400 fills a quarter of it.
	if pulse.PlayerScale.Cents != 40_000 {
		t.Fatalf("player scale = %d, want 40000", pulse.PlayerScale.Cents)
	}
	if leader.BarPercent != 100 || trailer.BarPercent != 25 {
		t.Fatalf("bar percents = %d, %d; want 100, 25", leader.BarPercent, trailer.BarPercent)
	}
	if leader.Won != 6 || leader.Lost != 2 || leader.Pushed != 1 || leader.Open != 1 {
		t.Fatalf("leader record = %+v", leader)
	}
}

func TestBookPulseHandlesAnEmptyBook(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	db := &scriptedDB{t: t, calls: []expectedCall{
		{kind: "row", contains: "house_clearing", args: []any{"CAD"}, row: fakeRow{values: []any{
			now, int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0),
		}}},
		{kind: "query", contains: "FROM markets m", args: []any{"CAD"}, rows: rows()},
		{kind: "query", contains: "w.state = 'accepted'", args: []any{openWagerLimit}, rows: rows()},
		{kind: "query", contains: "transaction_type IN", args: []any(nil), rows: rows()},
		{kind: "query", contains: "account_type = 'user_cash'", args: []any{"CAD"}, rows: rows()},
	}}
	reader, _ := New(db)

	pulse, err := reader.BookPulse(context.Background())
	if err != nil {
		t.Fatalf("BookPulse() error = %v", err)
	}
	db.done()
	if pulse.WorstCase.Cents != 0 || pulse.BestCase.Cents != 0 || pulse.PlayerScale.Cents != 0 {
		t.Fatalf("an empty book should swing nothing: %+v", pulse)
	}
	if len(pulse.Exposure) != 0 || len(pulse.Players) != 0 || len(pulse.OpenWagers) != 0 {
		t.Fatal("an empty book should return empty lists, not nil-derived panics")
	}
}

// The dashboard is a read model: it must never write, and it must never widen
// beyond the book currency by accident.
func TestBookPulseQueriesAreReadOnly(t *testing.T) {
	for name, query := range map[string]string{
		"totals": bookTotalsSQL, "exposure": exposureSQL,
		"open wagers": openWagersSQL, "player results": playerResultsSQL,
	} {
		upper := strings.ToUpper(query)
		for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "FOR UPDATE", "LOCK "} {
			if strings.Contains(upper, forbidden) {
				t.Errorf("%s query contains %q", name, forbidden)
			}
		}
	}
}
