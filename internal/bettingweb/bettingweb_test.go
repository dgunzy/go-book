package bettingweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/privateweb"
)

const (
	testCSRF     = "csrf-secret-token"
	testUserID   = "11111111-1111-1111-1111-111111111111"
	testMarketID = "22222222-2222-2222-2222-222222222222"
	testSelID    = "33333333-3333-3333-3333-333333333333"
	testWagerID  = "44444444-4444-4444-4444-444444444444"
	testIdem     = "55555555-5555-5555-5555-555555555555"
)

type fakeSessions struct {
	session privateweb.Session
	err     error
}

func (f fakeSessions) CurrentSession(*http.Request) (privateweb.Session, error) {
	return f.session, f.err
}

type fakeMarkets struct {
	open        []bettingpg.MarketRow
	all         []bettingpg.MarketRow
	createErr   error
	openErr     error
	closeErr    error
	settleErr   error
	voidErr     error
	createCalls []bettingpg.CreateMarketRequest
	openCalls   []string
	settleCalls []bettingpg.SettleMarketRequest
	voidCalls   []bettingpg.VoidMarketRequest
}

func (f *fakeMarkets) ListMarkets(context.Context) ([]bettingpg.MarketRow, error) {
	return f.all, nil
}
func (f *fakeMarkets) ListOpenMarkets(context.Context) ([]bettingpg.MarketRow, error) {
	return f.open, nil
}
func (f *fakeMarkets) CreateMarket(_ context.Context, req bettingpg.CreateMarketRequest) (betting.Market, error) {
	f.createCalls = append(f.createCalls, req)
	if f.createErr != nil {
		return betting.Market{}, f.createErr
	}
	return betting.Market{ID: betting.ID(req.MarketID), State: betting.MarketDraft}, nil
}
func (f *fakeMarkets) OpenMarket(_ context.Context, marketID, _ string) error {
	f.openCalls = append(f.openCalls, marketID)
	return f.openErr
}
func (f *fakeMarkets) CloseMarket(context.Context, string, string) error { return f.closeErr }
func (f *fakeMarkets) SettleMarket(_ context.Context, req bettingpg.SettleMarketRequest) (bettingpg.SettleReport, error) {
	f.settleCalls = append(f.settleCalls, req)
	return bettingpg.SettleReport{}, f.settleErr
}
func (f *fakeMarkets) VoidMarket(_ context.Context, req bettingpg.VoidMarketRequest) (bettingpg.SettleReport, error) {
	f.voidCalls = append(f.voidCalls, req)
	return bettingpg.SettleReport{}, f.voidErr
}

type fakeWagers struct {
	placeErr    error
	acceptErr   error
	rejectErr   error
	placed      []bettingpg.PlaceWagerRequest
	acceptCalls []string
	rejectCalls []struct{ id, reason string }
}

func (f *fakeWagers) PlaceWager(_ context.Context, req bettingpg.PlaceWagerRequest) (betting.Wager, error) {
	f.placed = append(f.placed, req)
	if f.placeErr != nil {
		return betting.Wager{}, f.placeErr
	}
	odds, _ := ledger.NewAmericanOdds(-110)
	return betting.Wager{
		ID:            betting.ID(req.WagerID),
		State:         betting.WagerPending,
		AcceptedTerms: "Team A to win",
		AcceptedOdds:  odds,
		Stake:         ledger.Money{Cents: req.StakeCents, Currency: req.Currency},
	}, nil
}
func (f *fakeWagers) AcceptWager(_ context.Context, wagerID, _ string) (betting.Wager, error) {
	f.acceptCalls = append(f.acceptCalls, wagerID)
	if f.acceptErr != nil {
		return betting.Wager{}, f.acceptErr
	}
	return betting.Wager{ID: betting.ID(wagerID), State: betting.WagerAccepted, Stake: ledger.Money{Cents: 1000, Currency: ledger.CAD}}, nil
}
func (f *fakeWagers) RejectWager(_ context.Context, wagerID, _, reason string) (betting.Wager, error) {
	f.rejectCalls = append(f.rejectCalls, struct{ id, reason string }{wagerID, reason})
	if f.rejectErr != nil {
		return betting.Wager{}, f.rejectErr
	}
	return betting.Wager{ID: betting.ID(wagerID), State: betting.WagerRejected}, nil
}
func (f *fakeWagers) ListWagersByState(context.Context, betting.WagerState) ([]bettingpg.AdminWagerRow, error) {
	return nil, nil
}
func (f *fakeWagers) ListWagersForUser(context.Context, string) ([]bettingpg.UserWagerRow, error) {
	return nil, nil
}

func newTestHandler(t *testing.T, session privateweb.Session, markets *fakeMarkets, wagers *fakeWagers) *Handler {
	t.Helper()
	handler, err := New(Dependencies{
		Sessions: fakeSessions{session: session},
		Markets:  markets,
		Wagers:   wagers,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func memberSession() privateweb.Session {
	return privateweb.Session{UserID: testUserID, Role: privateweb.RoleMember, Active: true, CSRFToken: testCSRF}
}

func adminSession() privateweb.Session {
	return privateweb.Session{UserID: testUserID, Role: privateweb.RoleAdmin, Active: true, CSRFToken: testCSRF}
}

func openMarketFixture() bettingpg.MarketRow {
	return bettingpg.MarketRow{
		ID: testMarketID, Type: betting.MarketMatch, Title: "Match winner",
		State: betting.MarketOpen, Currency: ledger.CAD, ClosesAt: time.Now().Add(time.Hour),
		Selections: []bettingpg.MarketSelectionRow{
			{ID: testSelID, Key: "side-a", DisplayTerms: "Team A to win", Active: true},
		},
	}
}

func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	handler := newTestHandler(t, privateweb.Session{}, &fakeMarkets{}, &fakeWagers{})
	handler.deps.Sessions = fakeSessions{err: privateweb.ErrNoSession}

	req := httptest.NewRequest(http.MethodGet, "/book/markets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if location := rec.Header().Get("Location"); !strings.HasPrefix(location, "/login") {
		t.Fatalf("redirect location = %q, want /login prefix", location)
	}
}

func TestMemberForbiddenFromAdminRoutes(t *testing.T) {
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, &fakeWagers{})

	req := httptest.NewRequest(http.MethodGet, "/admin/markets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestAdminMarketsRenders(t *testing.T) {
	handler := newTestHandler(t, adminSession(), &fakeMarkets{all: []bettingpg.MarketRow{openMarketFixture()}}, &fakeWagers{})

	req := httptest.NewRequest(http.MethodGet, "/admin/markets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Match winner") {
		t.Fatal("admin markets page did not render the market title")
	}
}

func TestPostWithoutCSRFTokenIsForbidden(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}, wagers)

	body := url.Values{
		"market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.00"},
		// no csrf_token
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(wagers.placed) != 0 {
		t.Fatal("PlaceWager was called despite missing CSRF token")
	}
}

func TestPlaceWagerUsesStoreCurrencyNotForm(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}, wagers)

	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.50"},
		"currency": {"USD"}, // hostile: must be ignored in favor of the market's CAD
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(wagers.placed) != 1 {
		t.Fatalf("PlaceWager calls = %d, want 1", len(wagers.placed))
	}
	placed := wagers.placed[0]
	if placed.Currency != ledger.CAD {
		t.Fatalf("placed currency = %q, want CAD (from store, not the USD form value)", placed.Currency)
	}
	if placed.StakeCents != 2550 {
		t.Fatalf("placed stake = %d cents, want 2550", placed.StakeCents)
	}
	if placed.UserID != testUserID {
		t.Fatalf("placed user = %q, want session user %q", placed.UserID, testUserID)
	}
}

func TestPlaceWagerRejectedForClosedMarket(t *testing.T) {
	wagers := &fakeWagers{}
	// No open markets: the selection cannot be found.
	handler := newTestHandler(t, memberSession(), &fakeMarkets{open: nil}, wagers)

	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.00"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if len(wagers.placed) != 0 {
		t.Fatal("PlaceWager called for a market that is not open")
	}
}

func TestAdminAcceptWagerHTMXReturnsFragment(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/wagers/"+testWagerID+"/accept", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if w.Header().Get("Location") != "" {
		t.Fatal("HTMX request should get a fragment, not a redirect")
	}
	if len(wagers.acceptCalls) != 1 || wagers.acceptCalls[0] != testWagerID {
		t.Fatalf("AcceptWager calls = %v, want [%s]", wagers.acceptCalls, testWagerID)
	}
}

func TestAdminRejectWagerRequiresReason(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}}.Encode() // no reason
	r := httptest.NewRequest(http.MethodPost, "/admin/wagers/"+testWagerID+"/reject", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(wagers.rejectCalls) != 0 {
		t.Fatal("RejectWager called without a reason")
	}
}

func TestAdminVoidMarketRequiresReason(t *testing.T) {
	markets := &fakeMarkets{all: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	body := url.Values{"csrf_token": {testCSRF}, "action": {"void"}}.Encode() // no reason
	r := httptest.NewRequest(http.MethodPost, "/admin/markets/"+testMarketID+"/settle", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(markets.voidCalls) != 0 {
		t.Fatal("VoidMarket called without a reason")
	}
}

func TestAdminVoidMarketSucceeds(t *testing.T) {
	markets := &fakeMarkets{all: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	body := url.Values{"csrf_token": {testCSRF}, "action": {"void"}, "reason": {"event cancelled"}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/markets/"+testMarketID+"/settle", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.voidCalls) != 1 {
		t.Fatalf("VoidMarket calls = %d, want 1", len(markets.voidCalls))
	}
	if markets.voidCalls[0].ActorUserID != testUserID {
		t.Fatalf("void actor = %q, want session user %q", markets.voidCalls[0].ActorUserID, testUserID)
	}
	if markets.voidCalls[0].Reason != "event cancelled" {
		t.Fatalf("void reason = %q, want %q", markets.voidCalls[0].Reason, "event cancelled")
	}
}

func TestAdminCreateMarketPassesActorAndSelections(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	closesAt := time.Now().UTC().Add(72 * time.Hour).Format("2006-01-02T15:04")
	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "market_type": {"future"},
		"title": {"Tournament winner"}, "currency": {"CAD"}, "closes_at": {closesAt},
		"selection_key_1": {"team-a"}, "selection_terms_1": {"Team A"}, "selection_odds_1": {"-110"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/markets", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.createCalls) != 1 {
		t.Fatalf("CreateMarket calls = %d, want 1", len(markets.createCalls))
	}
	req := markets.createCalls[0]
	if req.ActorUserID != testUserID {
		t.Fatalf("create actor = %q, want %q", req.ActorUserID, testUserID)
	}
	if len(req.Selections) != 1 || req.Selections[0].OfferedAmericanOdds != -110 {
		t.Fatalf("create selections = %+v, want one at -110", req.Selections)
	}
}

func TestParseStakeCents(t *testing.T) {
	cases := map[string]struct {
		want int64
		ok   bool
	}{
		"25":     {2500, true},
		"25.5":   {2550, true},
		"25.50":  {2550, true},
		"$25.50": {2550, true},
		"0":      {0, false},
		"0.00":   {0, false},
		"-5":     {0, false},
		"abc":    {0, false},
		"25.":    {0, false},
		"25.555": {0, false},
	}
	for input, expected := range cases {
		got, err := parseStakeCents(input)
		if expected.ok && (err != nil || got != expected.want) {
			t.Errorf("parseStakeCents(%q) = %d, %v; want %d, nil", input, got, err, expected.want)
		}
		if !expected.ok && err == nil {
			t.Errorf("parseStakeCents(%q) = %d, nil; want error", input, got)
		}
	}
}

func TestAdminCreateMarketParsesDynamicPricing(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	closesAt := time.Now().UTC().Add(72 * time.Hour).Format("2006-01-02T15:04")
	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "market_type": {"future"},
		"title": {"Tournament winner"}, "currency": {"CAD"}, "closes_at": {closesAt},
		"dynamic_pricing": {"1"}, "pricing_liquidity": {"1500.00"},
		"selection_key_1": {"team-a"}, "selection_terms_1": {"Team A"}, "selection_odds_1": {"-110"},
		"selection_key_2": {"team-b"}, "selection_terms_2": {"Team B"}, "selection_odds_2": {"120"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/markets", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.createCalls) != 1 {
		t.Fatalf("CreateMarket calls = %d, want 1", len(markets.createCalls))
	}
	req := markets.createCalls[0]
	if !req.DynamicPricing {
		t.Fatal("DynamicPricing was not set from the form")
	}
	if req.PricingLiquidityCents != 150000 {
		t.Fatalf("PricingLiquidityCents = %d, want 150000", req.PricingLiquidityCents)
	}
}
