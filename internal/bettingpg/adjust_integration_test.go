package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRecordSettlementClearsWhatAMemberOwes walks the real flow: a member loses
// a wager, owes the book, pays by e-transfer, and ends square — with their
// betting result untouched by the payment.
func TestRecordSettlementClearsWhatAMemberOwes(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	houseBefore := systemAccountBalance(t, ctx, pool, "house_clearing", f.Currency)
	// The betting-only baseline is read with the same filter it is later
	// asserted against; mixing a filtered sum with an unfiltered snapshot
	// would drift the moment any other test records a settlement.
	wagerHouseBefore := wagerOnlyHouseBalance(t, ctx, pool, f.Currency)

	// The member bets $100 and loses it, leaving them $100 down.
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 10_000, 1)
	// A market must stop taking action before it can be graded.
	if err := store.CloseMarket(ctx, f.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "match played, graded by hand",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultLoss, f.SelectionBID: betting.ResultWin,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}

	balanceAfterLoss := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)
	if balanceAfterLoss != 0 {
		t.Fatalf("balance after losing the full deposit = %d, want 0", balanceAfterLoss)
	}
	houseAfterLoss := systemAccountBalance(t, ctx, pool, "house_clearing", f.Currency)
	if houseAfterLoss-houseBefore != 10_000 {
		t.Fatalf("house delta after the loss = %d, want 10000", houseAfterLoss-houseBefore)
	}

	// A second market lets them bet on credit and lose again, so they end the
	// day genuinely owing the book money.
	// The second fixture's own users are funded with a token amount; only
	// f.UserA's balance matters here, and it is untouched by that funding.
	g := buildFixture(t, ctx, pool, 1_000)
	if _, err := pool.Exec(ctx, `UPDATE users SET credit_limit_cents = 100000 WHERE id = $1::uuid`, f.UserA); err != nil {
		t.Fatal(err)
	}
	placeAndAccept(t, ctx, store, g, f.UserA, g.SelectionAID, 5_000, 2)
	if err := store.CloseMarket(ctx, g.MarketID, f.UserB); err != nil {
		t.Fatalf("CloseMarket(second) error = %v", err)
	}
	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: g.MarketID, ActorUserID: f.UserB, Reason: "match played, graded by hand",
		Outcome: map[string]betting.SettlementResult{
			g.SelectionAID: betting.ResultLoss, g.SelectionBID: betting.ResultWin,
		},
	}); err != nil {
		t.Fatalf("SettleMarket(second) error = %v", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != -5_000 {
		t.Fatalf("balance after betting on credit and losing = %d, want -5000", balance)
	}

	// Snapshot immediately before the payment: the second market's settlement
	// has moved house clearing since the earlier reading, so the delta must be
	// measured from here, not from a stale figure.
	houseBeforePayment := systemAccountBalance(t, ctx, pool, "house_clearing", f.Currency)

	adjustmentID := mustNewUUID(t, ctx, store)
	recorded, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: adjustmentID, UserID: f.UserA, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPaymentReceived, AmountCents: 5_000,
		Currency: f.Currency, Reason: "e-transfer received",
	})
	if err != nil {
		t.Fatalf("RecordSettlement() error = %v", err)
	}
	if recorded.Amount.Cents != 5_000 || recorded.Direction != betting.AdjustmentPaymentReceived {
		t.Fatalf("recorded = %+v", recorded)
	}

	// The member is square, and the house's outstanding position dropped by
	// the same amount.
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != 0 {
		t.Fatalf("balance after settling up = %d, want 0", balance)
	}
	houseAfterPayment := systemAccountBalance(t, ctx, pool, "house_clearing", f.Currency)
	if houseBeforePayment-houseAfterPayment != 5_000 {
		t.Fatalf("house clearing fell by %d, want 5000", houseBeforePayment-houseAfterPayment)
	}

	// The payment is an admin adjustment, so anything that sums wager
	// transactions — the dashboard's house result and player standings —
	// is untouched by settling up.
	wagerHouseAfter := wagerOnlyHouseBalance(t, ctx, pool, f.Currency)
	if wagerHouseAfter-wagerHouseBefore != 15_000 {
		t.Fatalf("betting-only house result moved %d, want the full 15000 of losses and nothing from the payment",
			wagerHouseAfter-wagerHouseBefore)
	}

	// A resubmitted form must not post a second time.
	again, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: adjustmentID, UserID: f.UserA, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPaymentReceived, AmountCents: 5_000,
		Currency: f.Currency, Reason: "e-transfer received",
	})
	if err != nil {
		t.Fatalf("repeat RecordSettlement() error = %v", err)
	}
	if again.AdjustmentID != recorded.AdjustmentID {
		t.Fatal("a repeated settlement created a second record")
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != 0 {
		t.Fatalf("balance after a repeated settlement = %d, want 0", balance)
	}
}

// wagerOnlyHouseBalance is the house clearing balance counting wager
// transactions alone — the figure the dashboard reports as the house result.
// Settling up posts to the same account under a different transaction type and
// must never move this number.
func wagerOnlyHouseBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, currency ledger.Currency) int64 {
	t.Helper()
	var total int64
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(sum(p.amount_cents), 0)
		FROM ledger_postings p
		JOIN ledger_accounts a ON a.id = p.account_id
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE a.account_type = 'house_clearing' AND a.currency::text = $1
		  AND t.transaction_type IN ('wager_acceptance', 'wager_win', 'wager_loss', 'wager_refund')`,
		string(currency)).Scan(&total); err != nil {
		t.Fatalf("read betting-only house balance: %v", err)
	}
	return total
}

func TestRecordSettlementPaysAMemberOut(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	adjustmentID := mustNewUUID(t, ctx, store)
	if _, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: adjustmentID, UserID: f.UserA, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPayoutSent, AmountCents: 10_000,
		Currency: f.Currency, Reason: "paid out in cash",
	}); err != nil {
		t.Fatalf("RecordSettlement(payout) error = %v", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != 0 {
		t.Fatalf("balance after being paid out = %d, want 0", balance)
	}
}

func TestReverseSettlementRestoresTheBalanceWithoutDeletingHistory(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	before := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency)

	adjustmentID := mustNewUUID(t, ctx, store)
	if _, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: adjustmentID, UserID: f.UserA, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPaymentReceived, AmountCents: 7_500,
		Currency: f.Currency, Reason: "recorded against the wrong member",
	}); err != nil {
		t.Fatal(err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != before+7_500 {
		t.Fatalf("balance after the mistaken entry = %d, want %d", balance, before+7_500)
	}

	reversed, err := store.ReverseSettlement(ctx, adjustmentID, f.UserB, "wrong member")
	if err != nil {
		t.Fatalf("ReverseSettlement() error = %v", err)
	}
	if !reversed.Reversed {
		t.Fatal("the reversed settlement does not report itself as reversed")
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != before {
		t.Fatalf("balance after the reversal = %d, want the original %d", balance, before)
	}

	// Both entries remain: nothing is ever deleted from the ledger.
	var originals, reversals int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE transaction_type = 'admin_adjustment'),
		       count(*) FILTER (WHERE transaction_type = 'reversal')
		FROM ledger_transactions WHERE source_type = $1 AND source_id = $2::uuid`,
		betting.AdjustmentSourceType, adjustmentID).Scan(&originals, &reversals); err != nil {
		t.Fatal(err)
	}
	if originals != 1 || reversals != 1 {
		t.Fatalf("ledger rows = %d original, %d reversal; want one of each", originals, reversals)
	}
	// The reversal points at the entry it cancels.
	var linked bool
	if err := pool.QueryRow(ctx, `
		SELECT r.reversal_of_transaction_id = o.id
		FROM ledger_transactions r
		JOIN ledger_transactions o ON o.source_id = r.source_id AND o.transaction_type = 'admin_adjustment'
		WHERE r.transaction_type = 'reversal' AND r.source_id = $1::uuid`, adjustmentID).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if !linked {
		t.Fatal("the reversal does not link to the original transaction")
	}

	// A second reversal is refused rather than moving the balance again.
	if _, err := store.ReverseSettlement(ctx, adjustmentID, f.UserB, "again"); !errors.Is(err, ErrAlreadyReversed) {
		t.Fatalf("second ReverseSettlement() error = %v, want ErrAlreadyReversed", err)
	}
	if balance := accountBalanceFor(t, ctx, pool, f.UserA, "user_cash", f.Currency); balance != before {
		t.Fatalf("balance after a refused second reversal = %d, want %d", balance, before)
	}
}

func TestSettlementReadersReportOutstandingAndHistory(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	adjustmentID := mustNewUUID(t, ctx, store)
	if _, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: adjustmentID, UserID: f.UserA, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPayoutSent, AmountCents: 10_000,
		Currency: f.Currency, Reason: "season payout by e-transfer",
	}); err != nil {
		t.Fatal(err)
	}

	history, err := store.ListSettlements(ctx, 50)
	if err != nil {
		t.Fatalf("ListSettlements() error = %v", err)
	}
	var found *SettlementRow
	for i := range history {
		if history[i].AdjustmentID == adjustmentID {
			found = &history[i]
		}
	}
	if found == nil {
		t.Fatal("the recorded settlement is missing from the history")
	}
	if found.Direction != betting.AdjustmentPayoutSent || found.Amount.Cents != 10_000 {
		t.Fatalf("history row = %+v", *found)
	}
	if found.UserID != f.UserA || found.Reason != "season payout by e-transfer" {
		t.Fatalf("history row = %+v", *found)
	}
	if found.Reversed {
		t.Fatal("a fresh settlement should not read as reversed")
	}

	// The payout took the member to zero, so they drop off the outstanding list.
	balances, err := store.ListMemberBalances(ctx, f.Currency)
	if err != nil {
		t.Fatalf("ListMemberBalances() error = %v", err)
	}
	for _, balance := range balances {
		if balance.UserID == f.UserA {
			t.Fatalf("a squared-up member is still listed as outstanding: %+v", balance)
		}
		if balance.Balance.Cents == 0 {
			t.Fatalf("a zero balance should not be listed: %+v", balance)
		}
	}
}

func TestRecordSettlementRefusesUnknownMemberAndBadInput(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	missing := mustNewUUID(t, ctx, store)
	if _, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: mustNewUUID(t, ctx, store), UserID: missing, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPaymentReceived, AmountCents: 1_000,
		Currency: f.Currency, Reason: "cash",
	}); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("settlement for an unknown member error = %v, want ErrNotFound", err)
	}

	if _, err := store.RecordSettlement(ctx, RecordSettlementRequest{
		AdjustmentID: mustNewUUID(t, ctx, store), UserID: f.UserA, ActorUserID: f.UserB,
		Direction: betting.AdjustmentPaymentReceived, AmountCents: 0,
		Currency: f.Currency, Reason: "cash",
	}); !errors.Is(err, betting.ErrInvalid) {
		t.Fatalf("zero-amount settlement error = %v, want ErrInvalid", err)
	}

	if _, err := store.ReverseSettlement(ctx, mustNewUUID(t, ctx, store), f.UserB, "typo"); !errors.Is(err, betting.ErrNotFound) {
		t.Fatalf("reversing an unknown settlement error = %v, want ErrNotFound", err)
	}
}
