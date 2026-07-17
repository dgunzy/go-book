package privateweb

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

type fakeSessions struct {
	session Session
	err     error
}

func (f *fakeSessions) CurrentSession(*http.Request) (Session, error) {
	return f.session, f.err
}

type fakeReader struct {
	dashboard       DashboardSummary
	ledger          []LedgerRow
	wagers          []WagerRow
	reconciliation  AdminReconciliationSummary
	err             error
	userIDs         []string
	reconciliations int
}

func (f *fakeReader) DashboardSummary(_ context.Context, userID string) (DashboardSummary, error) {
	f.userIDs = append(f.userIDs, userID)
	return f.dashboard, f.err
}

func (f *fakeReader) LedgerRows(_ context.Context, userID string) ([]LedgerRow, error) {
	f.userIDs = append(f.userIDs, userID)
	return f.ledger, f.err
}

func (f *fakeReader) WagerRows(_ context.Context, userID string) ([]WagerRow, error) {
	f.userIDs = append(f.userIDs, userID)
	return f.wagers, f.err
}

func (f *fakeReader) ReconciliationSummary(context.Context) (AdminReconciliationSummary, error) {
	f.reconciliations++
	return f.reconciliation, f.err
}

func testDependencies(session Session) (Dependencies, *fakeReader) {
	if session.Active && session.CSRFToken == "" {
		session.CSRFToken = "test-csrf-token"
	}
	now := time.Date(2026, time.July, 16, 14, 30, 0, 0, time.UTC)
	reader := &fakeReader{
		dashboard: DashboardSummary{
			Balances: []BalanceRow{
				{Label: "Available", Account: "Cash", Amount: ledger.Money{Cents: 12_345, Currency: ledger.CAD}},
				{Label: "At risk", Account: "Escrow", Amount: ledger.Money{Cents: 2_000, Currency: ledger.CAD}},
			},
			OpenWagers: 2, PendingWagers: 1, SettledWagers: 7,
			RecentActivity: []LedgerRow{{OccurredAt: now, Description: "Wager accepted", TransactionType: "wager_acceptance", Reference: "W-1042", Amount: ledger.Money{Cents: -2_000, Currency: ledger.CAD}}},
		},
		ledger:         []LedgerRow{{OccurredAt: now, Description: "Opening balance", TransactionType: "migration_adjustment", Reference: "M-1", Amount: ledger.Money{Cents: 12_345, Currency: ledger.CAD}, RunningBalance: ledger.Money{Cents: 12_345, Currency: ledger.CAD}}},
		wagers:         []WagerRow{{PlacedAt: now, Market: "2026 Singles", Selection: "Alex to win", Odds: ledger.AmericanOdds(150), Stake: ledger.Money{Cents: 2_000, Currency: ledger.CAD}, PotentialProfit: ledger.Money{Cents: 3_000, Currency: ledger.CAD}, Status: "accepted"}},
		reconciliation: AdminReconciliationSummary{AsOf: now, LedgerBalanced: true, LedgerTransactions: 91, PendingOutboxEvents: 3, MigrationDifference: ledger.Money{Cents: -28_200, Currency: ledger.CAD}},
	}
	return Dependencies{Sessions: &fakeSessions{session: session}, Dashboard: reader, Ledger: reader, Wagers: reader, Reconciliation: reader}, reader
}

func newHandler(t *testing.T, deps Dependencies) http.Handler {
	t.Helper()
	handler, err := New(deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func TestNewRequiresAllDependencies(t *testing.T) {
	if _, err := New(Dependencies{}); err == nil {
		t.Fatal("New() error = nil, want missing dependency error")
	}
}

func TestUnauthenticatedRequestsRedirectToLogin(t *testing.T) {
	deps, _ := testDependencies(Session{})
	deps.Sessions = &fakeSessions{err: ErrNoSession}
	handler := newHandler(t, deps)

	request := httptest.NewRequest(http.MethodGet, "/book/wagers?status=open", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if got, want := response.Header().Get("Location"), "/login?next=%2Fbook%2Fwagers%3Fstatus%3Dopen"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Error("authenticated route response is cacheable")
	}
}

func TestMemberPagesRenderReadModels(t *testing.T) {
	deps, reader := testDependencies(Session{UserID: "user-7", DisplayName: "Dan & Co", Role: RoleMember, Active: true})
	handler := newHandler(t, deps)
	tests := []struct {
		path      string
		contains  []string
		forbidden []string
	}{
		{path: "/book", contains: []string{"<h1>Member book</h1>", "CA$123.45", "Wager accepted", "Dan &amp; Co"}, forbidden: []string{"href=\"/admin\""}},
		{path: "/book/ledger", contains: []string{"<h1>Ledger</h1>", "Opening balance", "migration_adjustment", "CA$123.45"}},
		{path: "/book/wagers", contains: []string{"<h1>Wagers</h1>", "2026 Singles", "Alex to win", "&#43;150", "CA$20.00", "CA$30.00"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			for _, expected := range test.contains {
				if !strings.Contains(response.Body.String(), expected) {
					t.Errorf("body does not contain %q", expected)
				}
			}
			for _, value := range test.forbidden {
				if strings.Contains(response.Body.String(), value) {
					t.Errorf("body unexpectedly contains %q", value)
				}
			}
			if strings.Count(strings.ToLower(response.Body.String()), "<form") != 1 ||
				!strings.Contains(response.Body.String(), `action="/logout"`) ||
				!strings.Contains(response.Body.String(), `value="test-csrf-token"`) {
				t.Error("member page does not contain exactly one CSRF-protected logout form")
			}
		})
	}
	if got, want := strings.Join(reader.userIDs, ","), "user-7,user-7,user-7"; got != want {
		t.Errorf("reader user IDs = %q, want %q", got, want)
	}
}

func TestAdminAuthorization(t *testing.T) {
	t.Run("member forbidden", func(t *testing.T) {
		deps, reader := testDependencies(Session{UserID: "member-1", DisplayName: "Member", Role: RoleMember, Active: true})
		response := httptest.NewRecorder()
		newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "Access denied") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		if reader.reconciliations != 0 {
			t.Fatal("member request reached reconciliation reader")
		}
	})

	for _, role := range []Role{RoleAdmin, RoleOwner} {
		t.Run(string(role), func(t *testing.T) {
			deps, reader := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: role, Active: true})
			response := httptest.NewRecorder()
			newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			for _, expected := range []string{"<h1>Reconciliation</h1>", "Balanced", "91", "-CA$282.00", "href=\"/admin\""} {
				if !strings.Contains(response.Body.String(), expected) {
					t.Errorf("body does not contain %q", expected)
				}
			}
			if reader.reconciliations != 1 {
				t.Fatalf("reconciliation calls = %d", reader.reconciliations)
			}
		})
	}
}

func TestInactiveAndUnknownMembersAreForbidden(t *testing.T) {
	for _, session := range []Session{
		{UserID: "pending-1", DisplayName: "Pending", Role: RoleMember, Active: false},
		{UserID: "unknown-1", DisplayName: "Unknown", Role: "root", Active: true},
		{DisplayName: "Missing ID", Role: RoleMember, Active: true},
	} {
		deps, reader := testDependencies(session)
		response := httptest.NewRecorder()
		newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/book", nil))
		if response.Code != http.StatusForbidden {
			t.Errorf("session %#v status = %d", session, response.Code)
		}
		if len(reader.userIDs) != 0 {
			t.Error("forbidden session reached dashboard reader")
		}
	}
}

func TestProviderErrorsAreNotDisclosed(t *testing.T) {
	deps, reader := testDependencies(Session{UserID: "user-1", Role: RoleMember, Active: true})
	reader.err = errors.New("postgres secret details")
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/book/ledger", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "postgres") || !strings.Contains(response.Body.String(), "Unable to load") {
		t.Errorf("unsafe error body %q", response.Body.String())
	}
}

func TestSessionErrorsAreNotTreatedAsLoggedOut(t *testing.T) {
	deps, reader := testDependencies(Session{})
	deps.Sessions = &fakeSessions{err: errors.New("session store unavailable")}
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/book", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if response.Header().Get("Location") != "" {
		t.Error("session infrastructure failure redirected to login")
	}
	if len(reader.userIDs) != 0 {
		t.Error("session infrastructure failure reached dashboard reader")
	}
}

func TestStateChangingMethodsAreRejected(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "user-1", Role: RoleOwner, Active: true})
	handler := newHandler(t, deps)
	for _, path := range []string{"/book", "/book/ledger", "/book/wagers", "/admin"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader("ignored=true")))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, response.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestFormattingFinancialValues(t *testing.T) {
	if got, want := formatMoney(ledger.Money{Cents: math.MinInt64, Currency: ledger.CAD}), "-CA$92233720368547758.08"; got != want {
		t.Errorf("formatMoney(MinInt64) = %q, want %q", got, want)
	}
	if got, want := formatMoney(ledger.Money{Cents: 99, Currency: "EUR"}), "EUR 0.99"; got != want {
		t.Errorf("formatMoney(EUR) = %q, want %q", got, want)
	}
	if got, want := formatOdds(ledger.AmericanOdds(-110)), "-110"; got != want {
		t.Errorf("formatOdds() = %q, want %q", got, want)
	}
}
