package privatepg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type expectedCall struct {
	kind     string
	contains string
	args     []any
	rows     pgx.Rows
	row      pgx.Row
}

type scriptedDB struct {
	t     *testing.T
	calls []expectedCall
	next  int
}

func (db *scriptedDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	db.t.Helper()
	expectation := db.take("query", query, args)
	return expectation.rows, nil
}

func (db *scriptedDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	db.t.Helper()
	expectation := db.take("row", query, args)
	return expectation.row
}

func (db *scriptedDB) take(kind, query string, args []any) expectedCall {
	db.t.Helper()
	if db.next >= len(db.calls) {
		db.t.Fatalf("unexpected %s: %s", kind, query)
	}
	expectation := db.calls[db.next]
	db.next++
	if expectation.kind != kind {
		db.t.Fatalf("call %d kind = %s, want %s", db.next, kind, expectation.kind)
	}
	if !strings.Contains(query, expectation.contains) {
		db.t.Errorf("query does not contain %q: %s", expectation.contains, query)
	}
	if !reflect.DeepEqual(args, expectation.args) {
		db.t.Errorf("query args = %#v, want %#v", args, expectation.args)
	}
	return expectation
}

func (db *scriptedDB) done() {
	db.t.Helper()
	if db.next != len(db.calls) {
		db.t.Fatalf("used %d of %d expected calls", db.next, len(db.calls))
	}
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assign(dest, r.values)
}

type fakeRows struct {
	values [][]any
	index  int
	err    error
	closed bool
}

func rows(values ...[]any) *fakeRows { return &fakeRows{values: values, index: -1} }

func (r *fakeRows) Close()                                       { r.closed = true }
func (r *fakeRows) Err() error                                   { return r.err }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return r.values[r.index], nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) Next() bool {
	r.index++
	if r.index >= len(r.values) {
		r.closed = true
		return false
	}
	return true
}
func (r *fakeRows) Scan(dest ...any) error { return assign(dest, r.values[r.index]) }

func assign(dest, values []any) error {
	if len(dest) != len(values) {
		return errors.New("scan arity mismatch")
	}
	for i := range dest {
		target := reflect.ValueOf(dest[i])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("scan destination is not a pointer")
		}
		value := reflect.ValueOf(values[i])
		if !value.Type().AssignableTo(target.Elem().Type()) {
			if !value.Type().ConvertibleTo(target.Elem().Type()) {
				return errors.New("scan type mismatch")
			}
			value = value.Convert(target.Elem().Type())
		}
		target.Elem().Set(value)
	}
	return nil
}

func TestDashboardSummaryIsUserScoped(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	db := &scriptedDB{t: t, calls: []expectedCall{
		{kind: "query", contains: "WHERE b.owner_user_id = $1::uuid", args: []any{"user-7"}, rows: rows(
			[]any{"user_cash", "Member cash", "CAD", int64(12_345)},
			[]any{"user_free_play", "Member free play", "CAD", int64(500)},
		)},
		{kind: "row", contains: "FROM wagers", args: []any{"user-7"}, row: fakeRow{values: []any{int64(2), int64(1), int64(8)}}},
		{kind: "query", contains: "WHERE a.owner_user_id = $1::uuid", args: []any{"user-7", 5}, rows: rows(
			[]any{now, "Wager accepted", "wager_acceptance", "wager:abc", "Member cash", int64(-2_000), "CAD", int64(10_345), true},
		)},
	}}
	reader, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := reader.DashboardSummary(context.Background(), "user-7")
	if err != nil {
		t.Fatal(err)
	}
	db.done()
	if len(summary.Balances) != 2 || summary.Balances[0].Label != "Available cash" || summary.Balances[0].Amount.Cents != 12_345 {
		t.Fatalf("balances = %+v", summary.Balances)
	}
	if summary.OpenWagers != 2 || summary.PendingWagers != 1 || summary.SettledWagers != 8 {
		t.Fatalf("wager counts = %+v", summary)
	}
	if len(summary.RecentActivity) != 1 || summary.RecentActivity[0].RunningBalance.Cents != 10_345 {
		t.Fatalf("activity = %+v", summary.RecentActivity)
	}
}

func TestLedgerRowsMapsSignedMoneyAndRunningBalance(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	db := &scriptedDB{t: t, calls: []expectedCall{{
		kind: "query", contains: "PARTITION BY p.account_id", args: []any{"member-id", 0}, rows: rows(
			[]any{now, "Legacy opening balance", "migration_adjustment", "legacy-cabot-book:tx", "Member cash", int64(-500), "CAD ", int64(-500), true},
		),
	}}}
	reader, _ := New(db)
	result, err := reader.LedgerRows(context.Background(), "member-id")
	if err != nil {
		t.Fatal(err)
	}
	db.done()
	if len(result) != 1 || result[0].Amount.Cents != -500 || result[0].RunningBalance.Cents != -500 || result[0].Amount.Currency != "CAD" {
		t.Fatalf("ledger rows = %+v", result)
	}
}

func TestWagerRowsIncludesExplicitLegacyArchive(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	db := &scriptedDB{t: t, calls: []expectedCall{{
		kind: "query", contains: "FROM legacy_book_wagers", args: []any{"member-id"}, rows: rows(
			[]any{now, "2026 singles", "Alex to win", int32(150), int64(2_000), "CAD", int64(3_000), "accepted", false},
			[]any{now.Add(-time.Hour), "Legacy archive", "Old accepted terms", int32(-110), int64(1_100), "CAD", int64(0), "legacy_win", true},
		),
	}}}
	reader, _ := New(db)
	result, err := reader.WagerRows(context.Background(), "member-id")
	if err != nil {
		t.Fatal(err)
	}
	db.done()
	if len(result) != 2 || result[0].PotentialProfit.Cents != 3_000 {
		t.Fatalf("current wager = %+v", result)
	}
	if result[1].Market != "Legacy archive" || result[1].PotentialProfit.Cents != 1_000 || result[1].Status != "legacy_win" {
		t.Fatalf("legacy wager = %+v", result[1])
	}
}

func TestReconciliationSummary(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	db := &scriptedDB{t: t, calls: []expectedCall{{
		kind: "row", contains: "WITH transaction_checks", row: fakeRow{values: []any{now, int64(91), int64(0), int64(3), int64(1), int64(28_200)}},
	}}}
	reader, _ := New(db)
	summary, err := reader.ReconciliationSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db.done()
	if !summary.LedgerBalanced || summary.LedgerTransactions != 91 || summary.PendingOutboxEvents != 3 || summary.FailedOutboxEvents != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.MigrationDifference.Cents != 28_200 || summary.MigrationDifference.Currency != "CAD" {
		t.Fatalf("migration difference = %+v", summary.MigrationDifference)
	}
}

func TestReadersRejectMissingUserBeforeQuery(t *testing.T) {
	db := &scriptedDB{t: t}
	reader, _ := New(db)
	if _, err := reader.DashboardSummary(context.Background(), "  "); err == nil {
		t.Fatal("DashboardSummary accepted empty user ID")
	}
	if _, err := reader.LedgerRows(context.Background(), ""); err == nil {
		t.Fatal("LedgerRows accepted empty user ID")
	}
	if _, err := reader.WagerRows(context.Background(), ""); err == nil {
		t.Fatal("WagerRows accepted empty user ID")
	}
	db.done()
}

func TestQueriesRemainReadOnly(t *testing.T) {
	for name, query := range map[string]string{
		"balances": balancesSQL, "dashboard": dashboardCountsSQL, "ledger": ledgerRowsSQL,
		"wagers": wagersSQL, "reconciliation": reconciliationSQL,
	} {
		upper := strings.ToUpper(query)
		for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "FOR UPDATE", "LOCK "} {
			if strings.Contains(upper, forbidden) {
				t.Errorf("%s query contains %q", name, forbidden)
			}
		}
	}
}

func TestNewRejectsNilDatabase(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}

// Keep compile-time coverage for pgx.Rows additions in the pinned driver.
var _ pgx.Rows = (*fakeRows)(nil)
