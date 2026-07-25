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
	testMatchID  = "66666666-6666-4666-8666-666666666666"
	testSide1ID  = "77777777-7777-4777-8777-777777777777"
	testSide2ID  = "88888888-8888-4888-8888-888888888888"
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
	matches     []bettingpg.MatchMarketOption
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
func (f *fakeMarkets) ListMarketableMatches(context.Context) ([]bettingpg.MatchMarketOption, error) {
	return f.matches, nil
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
	placeErr      error
	acceptErr     error
	rejectErr     error
	overrideCents int64
	hasOverride   bool
	placed        []bettingpg.PlaceWagerRequest
	acceptCalls   []string
	rejectCalls   []struct{ id, reason string }
	cancelErr     error
	cancelCalls   []struct{ id, user string }
	pending       []bettingpg.AdminWagerRow
	mine          []bettingpg.UserWagerRow
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
func (f *fakeWagers) CancelWager(_ context.Context, wagerID, userID string) (betting.Wager, error) {
	f.cancelCalls = append(f.cancelCalls, struct{ id, user string }{wagerID, userID})
	if f.cancelErr != nil {
		return betting.Wager{}, f.cancelErr
	}
	return betting.Wager{ID: betting.ID(wagerID), State: betting.WagerRejected,
		Stake: ledger.Money{Cents: 2500, Currency: ledger.CAD}}, nil
}
func (f *fakeWagers) ListWagersByState(context.Context, betting.WagerState) ([]bettingpg.AdminWagerRow, error) {
	return f.pending, nil
}
func (f *fakeWagers) ListWagersForUser(context.Context, string) ([]bettingpg.UserWagerRow, error) {
	return f.mine, nil
}
func (f *fakeWagers) AutoApproveLimitForUser(context.Context, string) (int64, bool, error) {
	return f.overrideCents, f.hasOverride, nil
}

type fakeSettlements struct {
	balances    []bettingpg.MemberBalanceRow
	history     []bettingpg.SettlementRow
	recordErr   error
	reverseErr  error
	recorded    []bettingpg.RecordSettlementRequest
	reversals   []struct{ id, actor, reason string }
	listedLimit int
}

func (f *fakeSettlements) RecordSettlement(_ context.Context, req bettingpg.RecordSettlementRequest) (bettingpg.SettlementRow, error) {
	f.recorded = append(f.recorded, req)
	if f.recordErr != nil {
		return bettingpg.SettlementRow{}, f.recordErr
	}
	return bettingpg.SettlementRow{
		AdjustmentID: req.AdjustmentID, UserID: req.UserID, MemberName: "Dan Guns",
		Direction: req.Direction, Amount: ledger.Money{Cents: req.AmountCents, Currency: req.Currency},
		Reason: req.Reason,
	}, nil
}

func (f *fakeSettlements) ReverseSettlement(_ context.Context, adjustmentID, actorUserID, reason string) (bettingpg.SettlementRow, error) {
	f.reversals = append(f.reversals, struct{ id, actor, reason string }{adjustmentID, actorUserID, reason})
	if f.reverseErr != nil {
		return bettingpg.SettlementRow{}, f.reverseErr
	}
	return bettingpg.SettlementRow{
		AdjustmentID: adjustmentID, MemberName: "Dan Guns", Reversed: true,
		Direction: betting.AdjustmentPaymentReceived,
		Amount:    ledger.Money{Cents: 50_000, Currency: ledger.CAD},
	}, nil
}

func (f *fakeSettlements) ListSettlements(_ context.Context, limit int) ([]bettingpg.SettlementRow, error) {
	f.listedLimit = limit
	return f.history, nil
}

func (f *fakeSettlements) ListMemberBalances(context.Context, ledger.Currency) ([]bettingpg.MemberBalanceRow, error) {
	return f.balances, nil
}

func newTestHandler(t *testing.T, session privateweb.Session, markets *fakeMarkets, wagers *fakeWagers) *Handler {
	t.Helper()
	return newTestHandlerWithSettlements(t, session, markets, wagers, &fakeSettlements{})
}

func newTestHandlerWithSettlements(t *testing.T, session privateweb.Session, markets *fakeMarkets,
	wagers *fakeWagers, settlements *fakeSettlements) *Handler {
	t.Helper()
	handler, err := New(Dependencies{
		Sessions:    fakeSessions{session: session},
		Markets:     markets,
		Wagers:      wagers,
		Settlements: settlements,
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

func TestAdminNewMarketRendersReadableMatchAndMobileSafeOdds(t *testing.T) {
	markets := &fakeMarkets{matches: []bettingpg.MatchMarketOption{{
		MatchID: testMatchID, EventName: "Cabot Cup", SeasonYear: 2026, MatchNumber: 3, Format: "fourball",
		Side1ID: testSide1ID, Side1TeamName: "Links", Side1Players: "Dan, Will",
		Side2ID: testSide2ID, Side2TeamName: "Cliffs", Side2Players: "Mike, Pat",
	}}}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})
	r := httptest.NewRequest(http.MethodGet, "/admin/markets/new", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Cabot Cup 2026 · Match 3 · Links — Dan, Will vs Cliffs — Mike, Pat", "Dan, Will", "Mike, Pat",
		`data-match-title="Cabot Cup 2026 · Match 3 · Dan, Will vs Mike, Pat"`,
		`data-side-one-name="Links"`, `data-side-two-players="Mike, Pat"`,
		`name="match_sign_1"`, `type="number" name="match_odds_1"`, "Closes at (Atlantic)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("new market page missing %q", want)
		}
	}
	if strings.Contains(body, "Semantic result key") || strings.Contains(body, "Match ID (UUID") {
		t.Fatal("new market page exposes internal settlement identifiers")
	}
}

func TestAdminCreateMatchMarketBuildsCanonicalSelections(t *testing.T) {
	markets := &fakeMarkets{matches: []bettingpg.MatchMarketOption{{
		MatchID: testMatchID, EventName: "Cabot Cup", SeasonYear: 2026, MatchNumber: 3, Format: "fourball",
		Side1ID: testSide1ID, Side1TeamName: "Links", Side1Players: "Dan, Will",
		Side2ID: testSide2ID, Side2TeamName: "Cliffs", Side2Players: "Mike, Pat",
	}}}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})
	closesAt := time.Now().Add(72 * time.Hour).Format("2006-01-02T15:04")
	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "market_type": {"match"}, "match_id": {testMatchID},
		"currency": {"CAD"}, "closes_at": {closesAt}, "dynamic_pricing": {"1"}, "pricing_liquidity": {"500"},
		"match_sign_1": {"-"}, "match_odds_1": {"125"}, "match_sign_2": {"+"}, "match_odds_2": {"140"},
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
	if req.Title != "Cabot Cup 2026 · Match 3 · Dan, Will vs Mike, Pat" || req.MatchID != testMatchID {
		t.Fatalf("canonical match identity = %q/%q", req.Title, req.MatchID)
	}
	if len(req.Selections) != 2 || req.Selections[0].OfferedAmericanOdds != -125 || req.Selections[1].OfferedAmericanOdds != 140 {
		t.Fatalf("match selections = %+v", req.Selections)
	}
	if req.Selections[0].SemanticResultKey != "side:"+testSide1ID || req.Selections[1].SemanticResultKey != "side:"+testSide2ID {
		t.Fatalf("match semantic mappings = %+v", req.Selections)
	}
	if req.Selections[0].DisplayTerms != "Dan, Will to win" || req.Selections[1].DisplayTerms != "Mike, Pat to win" {
		t.Fatalf("match player labels = %+v", req.Selections)
	}
}

func TestParseFormTimeUsesAtlantic(t *testing.T) {
	got, err := parseFormTime("2026-07-22T08:00")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.July, 22, 11, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parsed time = %s, want %s", got, want)
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

func TestPlaceWagerAutoApprovesUnderThreshold(t *testing.T) {
	wagers := &fakeWagers{}
	h, err := New(Dependencies{
		Sessions: fakeSessions{session: memberSession()},
		Markets:  &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}},
		Wagers:   wagers, Settlements: &fakeSettlements{}, AutoApproveMaxCents: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.00"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if len(wagers.acceptCalls) != 1 {
		t.Fatalf("AcceptWager calls = %d, want 1 (auto-approved)", len(wagers.acceptCalls))
	}
}

func TestPlaceWagerOverThresholdStaysPending(t *testing.T) {
	wagers := &fakeWagers{}
	h, err := New(Dependencies{
		Sessions: fakeSessions{session: memberSession()},
		Markets:  &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}},
		Wagers:   wagers, Settlements: &fakeSettlements{}, AutoApproveMaxCents: 1_000, // $10 threshold
	})
	if err != nil {
		t.Fatal(err)
	}
	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.00"}, // $25 > $10
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", w.Code)
	}
	if len(wagers.acceptCalls) != 0 {
		t.Fatalf("AcceptWager calls = %d, want 0 (over threshold, manual)", len(wagers.acceptCalls))
	}
}

func TestPlaceWagerPerPlayerOverrideRaisesThreshold(t *testing.T) {
	// Global threshold is $10, but this player's override is $100, so a $25 bet
	// auto-approves.
	wagers := &fakeWagers{overrideCents: 10_000, hasOverride: true}
	h, err := New(Dependencies{
		Sessions: fakeSessions{session: memberSession()},
		Markets:  &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}},
		Wagers:   wagers, Settlements: &fakeSettlements{}, AutoApproveMaxCents: 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.00"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if len(wagers.acceptCalls) != 1 {
		t.Fatalf("AcceptWager calls = %d, want 1 (per-player override raised the limit)", len(wagers.acceptCalls))
	}
}

func pendingWagerFixture(accepted, current int32, active bool, state betting.MarketState) bettingpg.AdminWagerRow {
	acceptedOdds, _ := ledger.NewAmericanOdds(accepted)
	currentOdds, _ := ledger.NewAmericanOdds(current)
	stake := ledger.Money{Cents: 30_000, Currency: ledger.CAD}
	profit, _ := acceptedOdds.Profit(stake)
	return bettingpg.AdminWagerRow{
		ID: testWagerID, UserID: testUserID, UserDisplayName: "Dan Guns",
		MarketID: testMarketID, MarketTitle: "Match 4", SelectionID: testSelID,
		SelectionTerms: "Bill, DC to win", Odds: acceptedOdds, Stake: stake,
		PotentialProfit: profit, State: betting.WagerPending, PlacedAt: time.Now().UTC(),
		CurrentOdds: currentOdds, SelectionActive: active, MarketState: state,
	}
}

func TestCompareLinesRanksByPayout(t *testing.T) {
	cases := []struct {
		name             string
		accepted, curren int32
		want             lineMove
	}{
		{"locked price pays more than the board", -208, -271, lineToMember},
		{"board pays more than the locked price", -271, -208, lineToBook},
		{"same price", -208, -208, lineUnchanged},
		{"crossing zero toward the member", 120, -110, lineToMember},
		{"crossing zero toward the book", -110, 120, lineToBook},
		{"+100 and -100 pay the same", 100, -100, lineUnchanged},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			accepted, err := ledger.NewAmericanOdds(testCase.accepted)
			if err != nil {
				t.Fatal(err)
			}
			current, err := ledger.NewAmericanOdds(testCase.curren)
			if err != nil {
				t.Fatal(err)
			}
			if got := compareLines(accepted, current); got != testCase.want {
				t.Fatalf("compareLines(%d, %d) = %q, want %q", testCase.accepted, testCase.curren, got, testCase.want)
			}
		})
	}
}

func TestAdminWagersShowsLiveLineWarningWhenLineMovedToMember(t *testing.T) {
	wagers := &fakeWagers{pending: []bettingpg.AdminWagerRow{
		pendingWagerFixture(-208, -271, true, betting.MarketOpen),
	}}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	r := httptest.NewRequest(http.MethodGet, "/admin/wagers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "-271") {
		t.Fatal("pending wagers page did not render the live line")
	}
	if !strings.Contains(body, "Moved in member's favour") {
		t.Fatal("pending wagers page did not warn that the line moved in the member's favour")
	}
	if !strings.Contains(body, "is-line-moved") {
		t.Fatal("the stale-price row was not flagged")
	}
}

func TestAdminWagersDoesNotWarnWhenLineMovedToBook(t *testing.T) {
	wagers := &fakeWagers{pending: []bettingpg.AdminWagerRow{
		pendingWagerFixture(-271, -208, true, betting.MarketOpen),
	}}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	r := httptest.NewRequest(http.MethodGet, "/admin/wagers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	body := w.Body.String()
	if strings.Contains(body, "Moved in member's favour") {
		t.Fatal("warned about a line that moved in the book's favour")
	}
	if !strings.Contains(body, "Moved to the book") {
		t.Fatal("pending wagers page did not report the favourable move")
	}
}

func TestAdminWagersReportsOffBoardSelection(t *testing.T) {
	wagers := &fakeWagers{pending: []bettingpg.AdminWagerRow{
		pendingWagerFixture(-208, -271, false, betting.MarketOpen),
	}}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	r := httptest.NewRequest(http.MethodGet, "/admin/wagers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "Off the board") {
		t.Fatal("an inactive selection should not be compared against a live line")
	}
	if strings.Contains(body, "Moved in member's favour") {
		t.Fatal("warned using a line that is no longer offered")
	}
}

func myWagerFixture(state betting.WagerState, accepted, current int32, reason string) bettingpg.UserWagerRow {
	acceptedOdds, _ := ledger.NewAmericanOdds(accepted)
	currentOdds, _ := ledger.NewAmericanOdds(current)
	stake := ledger.Money{Cents: 2_500, Currency: ledger.CAD}
	profit, _ := acceptedOdds.Profit(stake)
	return bettingpg.UserWagerRow{
		ID: testWagerID, MarketTitle: "Match 4", SelectionTerms: "Bill, DC to win",
		Odds: acceptedOdds, Stake: stake, PotentialProfit: profit, State: state,
		RejectionReason: reason, PlacedAt: time.Now().UTC(),
		CurrentOdds: currentOdds, SelectionActive: true, MarketState: betting.MarketOpen,
	}
}

func TestMemberWagersOffersCancelOnlyWhilePending(t *testing.T) {
	wagers := &fakeWagers{mine: []bettingpg.UserWagerRow{
		myWagerFixture(betting.WagerPending, -208, -208, ""),
	}}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	r := httptest.NewRequest(http.MethodGet, "/book/wagers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/book/wagers/"+testWagerID+"/cancel") {
		t.Fatal("a pending wager should offer the member a cancel button")
	}

	// An accepted wager is in escrow: only the book can unwind it.
	wagers.mine = []bettingpg.UserWagerRow{myWagerFixture(betting.WagerAccepted, -208, -208, "")}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/book/wagers", nil))
	if strings.Contains(w.Body.String(), "/cancel") {
		t.Fatal("an accepted wager must not offer a cancel button")
	}
}

func TestMemberWagersFlagsLineMovedAgainstMember(t *testing.T) {
	// Locked in at -271; the same side is now offered at -208, so the board
	// pays more than the member's pending price does.
	wagers := &fakeWagers{mine: []bettingpg.UserWagerRow{
		myWagerFixture(betting.WagerPending, -271, -208, ""),
	}}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/book/wagers", nil))

	body := w.Body.String()
	if !strings.Contains(body, "Moved against you") {
		t.Fatal("member page did not flag that the line moved against the pending wager")
	}
	if !strings.Contains(body, "-208") {
		t.Fatal("member page did not show the live line")
	}
}

func TestMemberWagersShowsCancelledDistinctFromRejected(t *testing.T) {
	wagers := &fakeWagers{mine: []bettingpg.UserWagerRow{
		myWagerFixture(betting.WagerRejected, -208, -208, betting.CancelWagerReason),
	}}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/book/wagers", nil))

	body := w.Body.String()
	if !strings.Contains(body, "cancelled") {
		t.Fatal("a wager the member pulled should read as cancelled, not rejected")
	}
	if strings.Contains(body, betting.CancelWagerReason) {
		t.Fatal("the internal cancellation reason should not be shown back to the member")
	}
}

func TestCancelWagerUsesSessionUserAndWagerPath(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers/"+testWagerID+"/cancel", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(wagers.cancelCalls) != 1 {
		t.Fatalf("CancelWager calls = %d, want 1", len(wagers.cancelCalls))
	}
	call := wagers.cancelCalls[0]
	if call.id != testWagerID || call.user != testUserID {
		t.Fatalf("CancelWager(%q, %q), want (%q, %q) — the owner comes from the session",
			call.id, call.user, testWagerID, testUserID)
	}
}

func TestCancelWagerRequiresCSRFToken(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	r := httptest.NewRequest(http.MethodPost, "/book/wagers/"+testWagerID+"/cancel", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(wagers.cancelCalls) != 0 {
		t.Fatal("CancelWager was called without a CSRF token")
	}
}

func TestCancelWagerRejectsMalformedID(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers/not-a-uuid/cancel", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if len(wagers.cancelCalls) != 0 {
		t.Fatal("CancelWager was called with a malformed wager ID")
	}
}

func TestCancelWagerReportsAlreadyReviewedWager(t *testing.T) {
	wagers := &fakeWagers{cancelErr: betting.ErrInvalidTransition}
	handler := newTestHandler(t, memberSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers/"+testWagerID+"/cancel", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no longer pending") {
		t.Fatalf("body = %q, want an explanation that the book already reviewed it", w.Body.String())
	}
}

func settlementFixtures() *fakeSettlements {
	return &fakeSettlements{
		balances: []bettingpg.MemberBalanceRow{
			{UserID: testUserID, Name: "Dan Guns", Balance: ledger.Money{Cents: -50_000, Currency: ledger.CAD}},
			{UserID: testSelID, Name: "Bill C", Balance: ledger.Money{Cents: 20_000, Currency: ledger.CAD}},
		},
		history: []bettingpg.SettlementRow{{
			AdjustmentID: testIdem, MemberName: "Dan Guns", Direction: betting.AdjustmentPaymentReceived,
			Amount: ledger.Money{Cents: 25_000, Currency: ledger.CAD}, Reason: "e-transfer",
			ActorName: "Book Admin", OccurredAt: time.Now().UTC(),
		}},
	}
}

func TestSettleUpPageShowsWhoOwesAndWhichWayToSettle(t *testing.T) {
	settlements := settlementFixtures()
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, &fakeWagers{}, settlements)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/settle-up", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, expected := range []string{
		"-CA$500.00", "Owes the book", // the member who is down
		"CA$200.00", "The book owes them",
		`value="500.00"`, // the form is pre-filled with what would square them
		"e-transfer",     // history
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("settle-up page does not contain %q", expected)
		}
	}
	// The member who owes should default to paying the book, not being paid.
	if !strings.Contains(body, `<option value="payment_received" selected>`) {
		t.Error("a member who owes the book should default to a payment in")
	}
}

func TestSettleUpIsAdminOnly(t *testing.T) {
	settlements := settlementFixtures()
	handler := newTestHandlerWithSettlements(t, memberSession(), &fakeMarkets{}, &fakeWagers{}, settlements)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/admin/settle-up", nil),
		httptest.NewRequest(http.MethodPost, "/admin/settle-up", strings.NewReader(
			url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID}, "direction": {"payment_received"},
				"amount": {"500.00"}, "reason": {"cash"}}.Encode())),
	} {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403", request.Method, request.URL.Path, w.Code)
		}
	}
	if len(settlements.recorded) != 0 {
		t.Fatal("a member reached the settlement writer")
	}
}

func TestRecordSettlementPostsWithTheSessionActorAndGeneratedID(t *testing.T) {
	settlements := settlementFixtures()
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, &fakeWagers{}, settlements)

	body := url.Values{
		"csrf_token": {testCSRF}, "user_id": {testUserID}, "direction": {"payment_received"},
		"amount": {"500.00"}, "reason": {"e-transfer received"},
		// Hostile: the adjustment ID must never come from the form, or a
		// replayed page could be aimed at an existing settlement.
		"adjustment_id": {testIdem},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/settle-up", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(settlements.recorded) != 1 {
		t.Fatalf("RecordSettlement calls = %d, want 1", len(settlements.recorded))
	}
	recorded := settlements.recorded[0]
	if recorded.ActorUserID != testUserID {
		t.Fatalf("actor = %q, want the session user", recorded.ActorUserID)
	}
	if recorded.AdjustmentID == testIdem || !isUUID(recorded.AdjustmentID) {
		t.Fatalf("adjustment ID = %q, want a freshly generated one", recorded.AdjustmentID)
	}
	if recorded.AmountCents != 50_000 || recorded.Currency != ledger.CAD {
		t.Fatalf("amount = %d %s", recorded.AmountCents, recorded.Currency)
	}
	if recorded.Direction != betting.AdjustmentPaymentReceived {
		t.Fatalf("direction = %q", recorded.Direction)
	}
}

func TestRecordSettlementRejectsBadInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		form   url.Values
		status int
	}{
		{"no reason", url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID},
			"direction": {"payment_received"}, "amount": {"10.00"}, "reason": {"  "}}, http.StatusBadRequest},
		{"zero amount", url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID},
			"direction": {"payment_received"}, "amount": {"0"}, "reason": {"cash"}}, http.StatusBadRequest},
		{"negative amount", url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID},
			"direction": {"payment_received"}, "amount": {"-25.00"}, "reason": {"cash"}}, http.StatusBadRequest},
		{"unknown direction", url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID},
			"direction": {"write_off"}, "amount": {"10.00"}, "reason": {"cash"}}, http.StatusBadRequest},
		{"malformed member", url.Values{"csrf_token": {testCSRF}, "user_id": {"nope"},
			"direction": {"payment_received"}, "amount": {"10.00"}, "reason": {"cash"}}, http.StatusNotFound},
		{"no csrf token", url.Values{"user_id": {testUserID},
			"direction": {"payment_received"}, "amount": {"10.00"}, "reason": {"cash"}}, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			settlements := settlementFixtures()
			handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, &fakeWagers{}, settlements)
			r := httptest.NewRequest(http.MethodPost, "/admin/settle-up", strings.NewReader(test.form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, test.status, w.Body.String())
			}
			if len(settlements.recorded) != 0 {
				t.Fatal("a rejected settlement still reached the ledger")
			}
		})
	}
}

func TestReverseSettlementNeedsAReasonAndReportsADoubleReversal(t *testing.T) {
	settlements := settlementFixtures()
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, &fakeWagers{}, settlements)
	path := "/admin/settle-up/" + testIdem + "/reverse"

	// No reason: nothing is posted.
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"csrf_token": {testCSRF}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status without a reason = %d, want 400", w.Code)
	}
	if len(settlements.reversals) != 0 {
		t.Fatal("a reversal without a reason reached the ledger")
	}

	// With a reason: the session user is the actor.
	body := url.Values{"csrf_token": {testCSRF}, "reason": {"recorded against the wrong member"}}.Encode()
	r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(settlements.reversals) != 1 || settlements.reversals[0].actor != testUserID {
		t.Fatalf("reversals = %+v", settlements.reversals)
	}

	// Reversing twice is refused rather than moving the balance again.
	settlements.reverseErr = bettingpg.ErrAlreadyReversed
	r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("double reversal status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already been reversed") {
		t.Fatalf("body = %q", w.Body.String())
	}
}
