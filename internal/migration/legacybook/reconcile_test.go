package legacybook

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type staticSource struct {
	users        []LegacyUser
	transactions []LegacyTransaction
	wagers       []LegacyWager
}

func (s staticSource) Users(context.Context) ([]LegacyUser, error) {
	return append([]LegacyUser(nil), s.users...), nil
}
func (s staticSource) Transactions(context.Context) ([]LegacyTransaction, error) {
	return append([]LegacyTransaction(nil), s.transactions...), nil
}
func (s staticSource) Wagers(context.Context) ([]LegacyWager, error) {
	return append([]LegacyWager(nil), s.wagers...), nil
}

var archiveTime = time.Date(2025, 5, 18, 1, 13, 48, 0, time.UTC)

func TestParseCentsRejectsRounding(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		want    int64
		wantErr bool
	}{
		"0": {0, false}, "12.3": {1230, false}, "-241.67": {-24167, false},
		"1.2300": {123, false}, "+4.05": {405, false}, "3914.3399999999997": {0, true},
		"1e2": {0, true}, "1.001": {0, true}, "": {0, true},
	}
	for input, test := range tests {
		input, test := input, test
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCents(input)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("ParseCents(%q) = %d, %v; want %d, error=%v", input, got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestReconcileReportsDifferenceAndRoleMapping(t *testing.T) {
	t.Parallel()
	source := staticSource{
		users: []LegacyUser{
			{ID: 2, DisplayName: " Admin ", Email: "ADMIN@EXAMPLE.CA ", Role: "admin", Balance: "5.00", FreePlayBalance: "1.25"},
			{ID: 1, DisplayName: "Owner", Email: "owner@example.ca", Role: "root", Balance: "10.00", FreePlayBalance: "0"},
		},
		transactions: []LegacyTransaction{
			{ID: 3, UserID: 1, Amount: "-2.00", Type: "debit", OccurredAt: archiveTime},
			{ID: 1, UserID: 1, Amount: "12.00", Type: "credit", OccurredAt: archiveTime},
			{ID: 2, UserID: 2, Amount: "2.18", Type: "credit", OccurredAt: archiveTime},
		},
		wagers: []LegacyWager{{ID: 4, UserID: 2, Amount: "2.50", Description: "Historical terms", Odds: "-125", PlacedAt: archiveTime, Result: "tie", Approved: true}},
	}
	report, err := Reconcile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Promotable() {
		t.Fatalf("expected warning-only report: %+v", report.Issues)
	}
	if report.DifferenceTotalCents != 282 {
		t.Fatalf("difference = %d, want 282", report.DifferenceTotalCents)
	}
	if report.Users[0].TargetRole != "owner" || report.Users[1].TargetRole != "admin" {
		t.Fatalf("unexpected role mapping: %+v", report.Users)
	}
	if report.Users[1].Email != "admin@example.ca" || report.Users[1].ClosingFreePlayCents != 125 {
		t.Fatalf("unexpected normalization: %+v", report.Users[1])
	}
	if len(report.Wagers) != 1 || report.Wagers[0].Result != "push" || report.Wagers[0].StakeCents != 250 {
		t.Fatalf("unexpected normalized wager: %+v", report.Wagers)
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"difference_total_cents": 282`) {
		t.Fatalf("machine report omitted total: %s", data)
	}
	second, err := Reconcile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, second) {
		t.Fatal("reconciliation is not deterministic")
	}
}

func TestReconcileBlocksFractionalCentsSignErrorsAndOrphans(t *testing.T) {
	t.Parallel()
	report, err := Reconcile(context.Background(), staticSource{
		users: []LegacyUser{{ID: 1, DisplayName: "One", Role: "user", Balance: "-241.67000000000007", FreePlayBalance: "0"}},
		transactions: []LegacyTransaction{
			{ID: 1, UserID: 1, Amount: "10", Type: "debit"},
			{ID: 2, UserID: 99, Amount: "10", Type: "credit"},
		},
		wagers: []LegacyWager{
			{ID: 1, UserID: 1, Amount: "1.001", Description: "bad stake", Odds: "-110", PlacedAt: archiveTime, Result: "win"},
			{ID: 2, UserID: 99, Amount: "1.00", Description: "orphan", Odds: "-110", PlacedAt: archiveTime, Result: "lose"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Promotable() {
		t.Fatal("invalid source was promotable")
	}
	codes := map[string]bool{}
	for _, issue := range report.Issues {
		codes[issue.Code] = true
	}
	for _, want := range []string{"invalid_cash_amount", "transaction_sign_mismatch", "orphan_transaction", "invalid_wager_stake", "orphan_wager"} {
		if !codes[want] {
			t.Errorf("missing issue %q: %+v", want, report.Issues)
		}
	}
}

func TestReconcileRejectsZeroValueTransactions(t *testing.T) {
	t.Parallel()

	report, err := Reconcile(context.Background(), staticSource{
		users: []LegacyUser{{ID: 1, DisplayName: "Member", Role: "member", Balance: "0", FreePlayBalance: "0"}},
		transactions: []LegacyTransaction{{
			ID: 1, UserID: 1, Amount: "0.00", Type: "credit", Description: "Invalid zero credit", OccurredAt: archiveTime,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Promotable() || len(report.Transactions) != 0 || len(report.Issues) != 1 || report.Issues[0].Code != "transaction_sign_mismatch" {
		t.Fatalf("zero transaction report = %+v", report)
	}
}

type fakeBalance struct {
	userID, accountType string
	amount              int64
}
type fakeState struct {
	users        map[int64]string
	roles        map[string]string
	balances     map[string]fakeBalance
	settlements  map[string]fakeBalance
	transactions map[int64]TransactionRecord
	wagers       map[int64]WagerRecord
}
type fakeStore struct {
	state       fakeState
	failBalance bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{state: fakeState{users: map[int64]string{}, roles: map[string]string{}, balances: map[string]fakeBalance{}, settlements: map[string]fakeBalance{}, transactions: map[int64]TransactionRecord{}, wagers: map[int64]WagerRecord{}}}
}

func (s *fakeStore) WithinTransaction(_ context.Context, operation func(Repository) error) error {
	clone := fakeState{users: map[int64]string{}, roles: map[string]string{}, balances: map[string]fakeBalance{}, settlements: map[string]fakeBalance{}, transactions: map[int64]TransactionRecord{}, wagers: map[int64]WagerRecord{}}
	for k, v := range s.state.users {
		clone.users[k] = v
	}
	for k, v := range s.state.roles {
		clone.roles[k] = v
	}
	for k, v := range s.state.balances {
		clone.balances[k] = v
	}
	for k, v := range s.state.settlements {
		clone.settlements[k] = v
	}
	for k, v := range s.state.transactions {
		clone.transactions[k] = v
	}
	for k, v := range s.state.wagers {
		clone.wagers[k] = v
	}
	repo := &fakeRepository{state: &clone, failBalance: s.failBalance}
	if err := operation(repo); err != nil {
		return err
	}
	s.state = clone
	return nil
}

type fakeRepository struct {
	state       *fakeState
	failBalance bool
}

func (r *fakeRepository) EnsureApprovedUser(_ context.Context, _ string, input UserInput) (string, error) {
	id := r.state.users[input.LegacyUserID]
	if id == "" {
		id = "user-" + strings.TrimSpace(input.DisplayName)
		r.state.users[input.LegacyUserID] = id
	}
	if role := r.state.roles[id]; role != "" && role != input.Role {
		return "", errors.New("role collision")
	}
	r.state.roles[id] = input.Role
	return id, nil
}
func (r *fakeRepository) EnsureOpeningBalance(_ context.Context, _ string, input OpeningBalanceInput) error {
	if r.failBalance {
		return errors.New("injected balance failure")
	}
	requested := fakeBalance{userID: input.UserID, accountType: input.AccountType, amount: input.AmountCents}
	if existing, ok := r.state.balances[input.IdempotencyKey]; ok && existing != requested {
		return errors.New("idempotency collision")
	}
	r.state.balances[input.IdempotencyKey] = requested
	return nil
}
func (r *fakeRepository) EnsureSeasonSettlement(_ context.Context, _ string, input SeasonSettlementInput) error {
	if input.Reason == "" {
		return errors.New("season settlement requires a reason")
	}
	requested := fakeBalance{userID: input.UserID, accountType: input.AccountType, amount: -input.SettledCents}
	if existing, ok := r.state.settlements[input.IdempotencyKey]; ok && existing != requested {
		return errors.New("settlement idempotency collision")
	}
	r.state.settlements[input.IdempotencyKey] = requested
	return nil
}

func (r *fakeRepository) EnsureLegacyTransaction(_ context.Context, _, _ string, record TransactionRecord) error {
	if existing, ok := r.state.transactions[record.SourceTransactionID]; ok && !reflect.DeepEqual(existing, record) {
		return errors.New("transaction collision")
	}
	r.state.transactions[record.SourceTransactionID] = record
	return nil
}
func (r *fakeRepository) EnsureLegacyWager(_ context.Context, _, _ string, record WagerRecord) error {
	if existing, ok := r.state.wagers[record.SourceWagerID]; ok && !reflect.DeepEqual(existing, record) {
		return errors.New("wager collision")
	}
	r.state.wagers[record.SourceWagerID] = record
	return nil
}

func (r *fakeRepository) CompletePromotion(context.Context, string, CompletionInput) error {
	return nil
}

func TestPromoteIsAtomicBalancedAndIdempotent(t *testing.T) {
	t.Parallel()
	report, err := Reconcile(context.Background(), staticSource{
		users:        []LegacyUser{{ID: 7, DisplayName: "Member", Email: "member@example.ca", Role: "user", Balance: "-5.00", FreePlayBalance: "2.00"}},
		transactions: []LegacyTransaction{{ID: 1, UserID: 7, Amount: "-4.00", Type: "debit", Description: "Historical debit", OccurredAt: archiveTime}},
		wagers:       []LegacyWager{{ID: 9, UserID: 7, Amount: "5.00", Description: "Legacy wager only", Odds: "+110", PlacedAt: archiveTime, Result: "win", Approved: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	options := PromoteOptions{BatchID: "batch-1", Currency: "CAD", ActorUserID: "migration-actor", SourceVersion: strings.Repeat("a", 64)}
	result, err := Promote(context.Background(), store, report, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.CashBalances != 1 || result.FreePlayBalances != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Transactions != 1 || result.Wagers != 1 {
		t.Fatalf("history was not promoted: %+v", result)
	}
	if len(store.state.balances) != 2 {
		t.Fatalf("got %d balance entries", len(store.state.balances))
	}
	if result.SettledBalances != 2 || len(store.state.settlements) != 2 {
		t.Fatalf("legacy balances were not settled: result=%+v settlements=%+v", result, store.state.settlements)
	}
	var userPostings, equityPostings int64
	for _, balance := range store.state.balances {
		userPostings += balance.amount
		equityPostings -= balance.amount
	}
	if userPostings+equityPostings != 0 {
		t.Fatalf("opening postings do not balance: %d + %d", userPostings, equityPostings)
	}
	var settledUserPostings int64
	for _, settlement := range store.state.settlements {
		settledUserPostings += settlement.amount
	}
	if userPostings+settledUserPostings != 0 {
		t.Fatalf("season settlement does not return user accounts to zero: opening %d + settlement %d", userPostings, settledUserPostings)
	}
	if _, err = Promote(context.Background(), store, report, options); err != nil {
		t.Fatal(err)
	}
	if len(store.state.users) != 1 || len(store.state.balances) != 2 || len(store.state.settlements) != 2 || len(store.state.transactions) != 1 || len(store.state.wagers) != 1 {
		t.Fatalf("rerun duplicated data: %+v", store.state)
	}
	changed := report
	changed.Transactions = append([]TransactionRecord(nil), report.Transactions...)
	changed.Transactions[0].AmountCents--
	if _, err = Promote(context.Background(), store, changed, options); err == nil {
		t.Fatal("changed immutable transaction was accepted on rerun")
	}
	if store.state.transactions[1].AmountCents != -400 {
		t.Fatalf("collision changed immutable history: %+v", store.state.transactions[1])
	}

	failing := newFakeStore()
	failing.failBalance = true
	if _, err = Promote(context.Background(), failing, report, options); err == nil {
		t.Fatal("expected injected failure")
	}
	if len(failing.state.users) != 0 || len(failing.state.balances) != 0 {
		t.Fatalf("failed transaction was not rolled back: %+v", failing.state)
	}
}

func TestPromoteRejectsBlockingReport(t *testing.T) {
	t.Parallel()
	report := Report{Version: 1, SourceSystem: SourceSystem, Issues: []Issue{{Severity: SeverityBlocking}}}
	if _, err := Promote(context.Background(), newFakeStore(), report, PromoteOptions{BatchID: "batch", ActorUserID: "migration-actor", SourceVersion: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("expected blocking report rejection")
	}
}

func TestOpeningPostingsAlwaysBalance(t *testing.T) {
	t.Parallel()
	for _, amount := range []int64{-9223372036854775807, -500, 500, 9223372036854775807} {
		user, equity, err := openingPostings(amount)
		if err != nil {
			t.Fatalf("openingPostings(%d): %v", amount, err)
		}
		if user != amount || user+equity != 0 {
			t.Fatalf("openingPostings(%d) = (%d, %d), not balanced", amount, user, equity)
		}
	}
	for _, amount := range []int64{0, -9223372036854775807 - 1} {
		if _, _, err := openingPostings(amount); err == nil {
			t.Fatalf("openingPostings(%d) accepted an unsafe amount", amount)
		}
	}
}

func TestSettlementPostingsInvertOpeningPostings(t *testing.T) {
	t.Parallel()
	for _, amount := range []int64{-9223372036854775807, -500, 500, 9223372036854775807} {
		openingUser, openingEquity, err := openingPostings(amount)
		if err != nil {
			t.Fatalf("openingPostings(%d): %v", amount, err)
		}
		settledUser, settledEquity, err := settlementPostings(amount)
		if err != nil {
			t.Fatalf("settlementPostings(%d): %v", amount, err)
		}
		if settledUser != -openingUser || settledEquity != -openingEquity {
			t.Fatalf("settlementPostings(%d) = (%d, %d) is not the inverse of opening (%d, %d)",
				amount, settledUser, settledEquity, openingUser, openingEquity)
		}
		if openingUser+settledUser != 0 || settledUser+settledEquity != 0 {
			t.Fatalf("settlementPostings(%d) does not zero the account or balance the transaction", amount)
		}
	}
	for _, amount := range []int64{0, -9223372036854775807 - 1} {
		if _, _, err := settlementPostings(amount); err == nil {
			t.Fatalf("settlementPostings(%d) accepted an unsafe amount", amount)
		}
	}
}
