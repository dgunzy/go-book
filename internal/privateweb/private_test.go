package privateweb

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	pulse           BookPulse
	pulses          int
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

func (f *fakeReader) BookPulse(context.Context) (BookPulse, error) {
	f.pulses++
	return f.pulse, f.err
}

func testDependencies(session Session) (Dependencies, *fakeReader) {
	if session.Active && session.CSRFToken == "" {
		session.CSRFToken = "test-csrf-token"
	}
	now := time.Date(2026, time.July, 16, 14, 30, 0, 0, time.UTC)
	reader := &fakeReader{
		dashboard: DashboardSummary{
			Balances: []BalanceRow{
				{Label: "Available", Account: "Cash", Note: "Settled cash held with the book", Amount: ledger.Money{Cents: 12_345, Currency: ledger.CAD}},
				{Label: "At risk", Account: "Escrow", Amount: ledger.Money{Cents: 2_000, Currency: ledger.CAD}},
			},
			OpenWagers: 2, PendingWagers: 1, SettledWagers: 7,
			RecentActivity:   []LedgerRow{{OccurredAt: now, Description: "Wager accepted", TransactionType: "wager_acceptance", Reference: "W-1042", ReferenceLabel: "2026 Singles — Alex to win", Amount: ledger.Money{Cents: -2_000, Currency: ledger.CAD}}},
			CreditLimit:      ledger.Money{Cents: 300_000, Currency: ledger.CAD},
			CreditAvailable:  ledger.Money{Cents: 196_364, Currency: ledger.CAD},
			AutoApproveLimit: ledger.Money{Cents: 10_000, Currency: ledger.CAD},
			OpenStake:        ledger.Money{Cents: 80_000, Currency: ledger.CAD},
			OpenToWin:        ledger.Money{Cents: 120_000, Currency: ledger.CAD},
			PendingStake:     ledger.Money{Cents: 50_000, Currency: ledger.CAD},
			ActiveWagers: []WagerRow{{
				PlacedAt: now, Market: "2026 Singles", Selection: "Alex to win", Odds: ledger.AmericanOdds(150),
				Stake: ledger.Money{Cents: 50_000, Currency: ledger.CAD}, PotentialProfit: ledger.Money{Cents: 75_000, Currency: ledger.CAD},
				Status: "Pending approval", Pending: true, ClosesAt: now.Add(time.Hour),
			}},
			MoreActiveWagers: 2,
		},
		ledger:         []LedgerRow{{OccurredAt: now, Description: "Opening balance", TransactionType: "migration_adjustment", Reference: "M-1", Amount: ledger.Money{Cents: 12_345, Currency: ledger.CAD}, RunningBalance: ledger.Money{Cents: 12_345, Currency: ledger.CAD}}},
		wagers:         []WagerRow{{PlacedAt: now, Market: "2026 Singles", Selection: "Alex to win", Odds: ledger.AmericanOdds(150), Stake: ledger.Money{Cents: 2_000, Currency: ledger.CAD}, PotentialProfit: ledger.Money{Cents: 3_000, Currency: ledger.CAD}, Status: "accepted"}},
		reconciliation: AdminReconciliationSummary{AsOf: now, LedgerBalanced: true, LedgerTransactions: 91, PendingOutboxEvents: 3, MigrationDifference: ledger.Money{Cents: -28_200, Currency: ledger.CAD}},
	}
	reader.pulse = BookPulse{
		AsOf:        now,
		HouseResult: ledger.Money{Cents: 25_000, Currency: ledger.CAD},
		Escrow:      ledger.Money{Cents: 80_000, Currency: ledger.CAD},
		Handle:      ledger.Money{Cents: 300_000, Currency: ledger.CAD},
		WorstCase:   ledger.Money{Cents: -4_000, Currency: ledger.CAD},
		BestCase:    ledger.Money{Cents: 10_400, Currency: ledger.CAD},
		OpenMarkets: 2, PendingWagers: 1, OpenWagerCount: 3,
		PendingStake: ledger.Money{Cents: 30_000, Currency: ledger.CAD},
		Exposure: []MarketExposure{{
			Market: "Cabot Cup 2026 Match 4", State: "open", ClosesAt: now, Wagers: 3,
			Stake: ledger.Money{Cents: 70_000, Currency: ledger.CAD},
			Outcomes: []ExposureOutcome{
				{Selection: "Bill, DC to win", Wagers: 2, Stake: ledger.Money{Cents: 50_000, Currency: ledger.CAD},
					Payout: ledger.Money{Cents: 74_000, Currency: ledger.CAD}, HouseNet: ledger.Money{Cents: -4_000, Currency: ledger.CAD}, Worst: true},
				{Selection: "Alex, Mau to win", Wagers: 1, Stake: ledger.Money{Cents: 20_000, Currency: ledger.CAD},
					Payout: ledger.Money{Cents: 59_600, Currency: ledger.CAD}, HouseNet: ledger.Money{Cents: 10_400, Currency: ledger.CAD}},
			},
		}},
		OpenWagers: []OpenWagerRow{{
			PlacedAt: now, MemberID: "user-dan", Member: "Dan Guns", Market: "Cabot Cup 2026 Match 4", Selection: "Bill, DC to win",
			Odds: ledger.AmericanOdds(-208), Stake: ledger.Money{Cents: 30_000, Currency: ledger.CAD},
			ToWin: ledger.Money{Cents: 14_423, Currency: ledger.CAD},
		}},
		Players: []PlayerResult{
			{UserID: "user-dan", Name: "Dan Guns", Net: ledger.Money{Cents: 40_000, Currency: ledger.CAD}, Won: 6, Lost: 2, Pushed: 1, Open: 1,
				Handle: ledger.Money{Cents: 120_000, Currency: ledger.CAD}, BarPercent: 100},
			{UserID: "user-bill", Name: "Bill C", Net: ledger.Money{Cents: -10_000, Currency: ledger.CAD}, Won: 1, Lost: 3, Open: 2,
				Handle: ledger.Money{Cents: 60_000, Currency: ledger.CAD}, BarPercent: 25},
		},
		PlayerScale: ledger.Money{Cents: 40_000, Currency: ledger.CAD},
		Outstanding: []OutstandingRow{
			{UserID: "user-bill", Name: "Bill C", Balance: ledger.Money{Cents: -15_000, Currency: ledger.CAD}},
			{UserID: "user-dan", Name: "Dan Guns", Balance: ledger.Money{Cents: 30_000, Currency: ledger.CAD}},
		},
		OwedToBook: ledger.Money{Cents: 15_000, Currency: ledger.CAD},
		OwedByBook: ledger.Money{Cents: 30_000, Currency: ledger.CAD},
	}
	return Dependencies{Sessions: &fakeSessions{session: session}, Dashboard: reader, Ledger: reader,
		Wagers: reader, Reconciliation: reader, BookPulse: reader}, reader
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

func TestLoginRedirectURLDoesNotReturnToMutation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		want   string
	}{
		{
			name: "get preserves requested page", method: http.MethodGet,
			target: "/book/wagers?status=open",
			want:   "/login?next=%2Fbook%2Fwagers%3Fstatus%3Dopen",
		},
		{
			name: "head preserves requested page", method: http.MethodHead,
			target: "/admin/matches",
			want:   "/login?next=%2Fadmin%2Fmatches",
		},
		{
			name: "post returns to member landing", method: http.MethodPost,
			target: "/admin/events/event-id/teams/team-id/roster",
			want:   "/login?next=%2Fbook",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, test.target, nil)
			if got := LoginRedirectURL(request); got != test.want {
				t.Fatalf("LoginRedirectURL() = %q, want %q", got, test.want)
			}
		})
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
		{path: "/book", contains: []string{"<h1>Member book</h1>", "CA$123.45", "Wager accepted", "Dan &amp; Co"}, forbidden: []string{"href=\"/admin\"", "test-book-banner"}},
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

// The overview must name the wagers a member has riding and state the limit
// that decides whether a stake fills instantly, so nobody has to decode a bare
// ledger reference to know what they bet on.
func TestOverviewNamesActiveWagersAndApprovalLimit(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "user-7", DisplayName: "Dan Guns", Role: RoleMember, Active: true})
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/book", nil))
	body := response.Body.String()
	for _, expected := range []string{
		"What you have riding", "2026 Singles", "Alex to win", "Pending approval",
		"CA$500.00", "Auto-approve limit", "CA$100.00",
		"CA$800.00", "CA$1200.00", // open stake at risk and profit if it all lands
		"2 more open or pending",
		"2026 Singles — Alex to win", // the ledger reference, named
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("overview does not contain %q", expected)
		}
	}
	if strings.Contains(body, "W-1042") {
		t.Error("overview shows a raw ledger reference in place of the wager it names")
	}
}

func TestAcceptanceSessionRendersPersistentTestBanner(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "test-owner", DisplayName: "Test Owner", Role: RoleOwner, Active: true, Acceptance: true})
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/book", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{`class="test-book-banner"`, "Test book", "No live money", "isolated acceptance data"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("body does not contain %q", expected)
		}
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
		if reader.reconciliations != 0 || reader.pulses != 0 {
			t.Fatal("a member request reached the book-wide admin readers")
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
			for _, expected := range []string{"<h1>Dashboard</h1>", "Balanced", "91", "-CA$282.00", "href=\"/admin\""} {
				if !strings.Contains(response.Body.String(), expected) {
					t.Errorf("body does not contain %q", expected)
				}
			}
			if reader.reconciliations != 1 || reader.pulses != 1 {
				t.Fatalf("reconciliation calls = %d, book pulse calls = %d", reader.reconciliations, reader.pulses)
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

func TestAdminDashboardRendersTheBookPulse(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"CA$250.00",              // house result
		"CA$800.00",              // escrow
		"CA$3000.00",             // handle
		"-CA$40.00",              // worst case
		"CA$104.00",              // best case
		"Cabot Cup 2026 Match 4", // exposure roll-up
		"Bill, DC to win",
		"is-worst-outcome", // the outcome that costs the house most is marked
		"Dan Guns",         // player standings
		"CA$400.00",        // leading player's net
		"6&ndash;2",        // record
		"width:100%",       // the leader's bar fills the track
		"width:25%",        // the trailing player's bar is scaled against it
		"pulse-bar is-ahead", "pulse-bar is-behind",
		"Longest bar = CA$400.00",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("dashboard does not contain %q", expected)
		}
	}
}

func TestAdminDashboardSurvivesAnEmptyBook(t *testing.T) {
	deps, reader := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	reader.pulse = BookPulse{AsOf: time.Date(2026, time.July, 16, 14, 30, 0, 0, time.UTC)}
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"No market is carrying risk right now.",
		"Nobody has action yet.",
		"No bets are live right now.",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("empty dashboard does not contain %q", expected)
		}
	}
}

func TestAdminDashboardFailsClosedWhenThePulseIsUnavailable(t *testing.T) {
	deps, reader := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	reader.err = errors.New("database is down")
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Owed-and-owing is a cash position and must read as separate from the player
// standings, which are betting results.
func TestAdminDashboardSeparatesOwedFromResults(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	body := response.Body.String()
	for _, expected := range []string{
		"Owed and owing", "Owed to the book", "CA$150.00",
		"Owed by the book", "CA$300.00",
		"Owes the book", "The book owes them",
		"/admin/settle-up",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("dashboard does not contain %q", expected)
		}
	}
	// The standings still report betting results, unchanged by cash owed.
	if !strings.Contains(body, "CA$400.00") {
		t.Error("player standings no longer show the betting result")
	}
}

// The dashboard is one page of everything, which stops being readable once
// there is a lot of action. It is split into panels with a tab strip; every
// panel is still rendered, so nothing is lost without JavaScript.
func TestAdminDashboardIsSplitIntoTabbedPanels(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	body := response.Body.String()
	for _, panel := range []string{"overview", "exposure", "players", "action", "health"} {
		if !strings.Contains(body, `data-panel="`+panel+`"`) {
			t.Errorf("dashboard has no %q panel", panel)
		}
		if !strings.Contains(body, `data-tab="`+panel+`"`) {
			t.Errorf("dashboard has no %q tab", panel)
		}
	}
	// Nothing may be hidden server-side: without JavaScript the whole page
	// must still read top to bottom.
	if strings.Contains(body, `data-panel="exposure" hidden`) || strings.Contains(body, "<div class=\"dashboard-panel\" hidden") {
		t.Error("a panel is hidden in the rendered HTML; it should only be hidden by script")
	}
	// The content is all still there.
	for _, expected := range []string{"House result", "Exposure by market", "Player standings", "Owed and owing", "Action on the board", "Ledger state"} {
		if !strings.Contains(body, expected) {
			t.Errorf("dashboard lost its %q section", expected)
		}
	}
}

// The dashboard is where an admin actually watches the book, so a member's
// name has to be a way into their bets rather than a dead end that sends you
// to the Members page to find them again.
func TestAdminDashboardLinksMembersToTheirBook(t *testing.T) {
	deps, _ := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	body := response.Body.String()
	for _, link := range []string{
		`href="/admin/members/user-dan/book"`,
		`href="/admin/members/user-bill/book"`,
	} {
		if !strings.Contains(body, link) {
			t.Errorf("dashboard does not link to %q", link)
		}
	}
	// The links appear in every place a member is named: standings, owed and
	// owing, and the live action list.
	if strings.Count(body, `href="/admin/members/user-dan/book"`) < 3 {
		t.Errorf("Dan is named in three sections but linked %d times",
			strings.Count(body, `href="/admin/members/user-dan/book"`))
	}
}

// The action list is filtered in the browser, so the whole board has to be in
// the HTML: filtering must never be a reason a bet is missing.
func TestAdminDashboardRendersEveryLiveBetWithAFilter(t *testing.T) {
	deps, reader := testDependencies(Session{UserID: "admin-1", DisplayName: "Book Admin", Role: RoleAdmin, Active: true})
	now := time.Date(2026, time.July, 25, 23, 30, 0, 0, time.UTC)
	// More bets than the old truncated list would have shown.
	pulse := reader.pulse
	pulse.OpenWagers = nil
	for i := 0; i < 40; i++ {
		pulse.OpenWagers = append(pulse.OpenWagers, OpenWagerRow{
			PlacedAt: now, MemberID: "user-" + strconv.Itoa(i), Member: "Member " + strconv.Itoa(i),
			Market: "Leading points getter", Selection: "Runner " + strconv.Itoa(i),
			Odds: ledger.AmericanOdds(150), Stake: ledger.Money{Cents: 5_000, Currency: ledger.CAD},
			ToWin: ledger.Money{Cents: 7_500, Currency: ledger.CAD},
		})
	}
	reader.pulse = pulse

	response := httptest.NewRecorder()
	newHandler(t, deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))
	body := response.Body.String()

	for _, member := range []string{"Member 0", "Member 26", "Member 39"} {
		if !strings.Contains(body, member) {
			t.Errorf("live action list is missing %q", member)
		}
	}
	if !strings.Contains(body, `data-filter-for="open-action"`) || !strings.Contains(body, `id="open-action"`) {
		t.Error("the action list has no filter control wired to it")
	}
	// Nothing may be hidden server-side; the filter only hides in the browser.
	if strings.Contains(body, "<tr hidden") {
		t.Error("a row is hidden in the rendered HTML")
	}
}
