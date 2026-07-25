package bettingweb

import (
	"context"
	"fmt"
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
	open         []bettingpg.MarketRow
	all          []bettingpg.MarketRow
	matches      []bettingpg.MatchMarketOption
	createErr    error
	openErr      error
	closeErr     error
	settleErr    error
	voidErr      error
	createCalls  []bettingpg.CreateMarketRequest
	openCalls    []string
	settleCalls  []bettingpg.SettleMarketRequest
	voidCalls    []bettingpg.VoidMarketRequest
	setLineErr   error
	lineCalls    []setLineCall
	closeTimeErr error
	closeTimes   []closeTimeCall
	restrictErr  error
	restricted   []bettingpg.RestrictRequest
	lifted       []struct{ market, user, selection string }
	restrictions []bettingpg.RestrictionRow
	members      []bettingpg.MemberOption
	scopedUsers  []string
}

type closeTimeCall struct {
	marketID string
	closesAt time.Time
	actor    string
	reason   string
}

type setLineCall struct {
	marketID    string
	selectionID string
	odds        ledger.AmericanOdds
	actor       string
	reason      string
}

func (f *fakeMarkets) ListMarkets(context.Context) ([]bettingpg.MarketRow, error) {
	return f.all, nil
}
func (f *fakeMarkets) ListOpenMarkets(context.Context) ([]bettingpg.MarketRow, error) {
	return f.open, nil
}

func (f *fakeMarkets) ListOpenMarketsForUser(_ context.Context, userID string) ([]bettingpg.MarketRow, error) {
	f.scopedUsers = append(f.scopedUsers, userID)
	return f.open, nil
}

func (f *fakeMarkets) RestrictMember(_ context.Context, req bettingpg.RestrictRequest) error {
	f.restricted = append(f.restricted, req)
	return f.restrictErr
}

func (f *fakeMarkets) LiftRestriction(_ context.Context, marketID, userID, selectionID string) error {
	f.lifted = append(f.lifted, struct{ market, user, selection string }{marketID, userID, selectionID})
	return f.restrictErr
}

func (f *fakeMarkets) ListRestrictions(context.Context) ([]bettingpg.RestrictionRow, error) {
	return f.restrictions, nil
}

func (f *fakeMarkets) ListMembers(context.Context) ([]bettingpg.MemberOption, error) {
	return f.members, nil
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
func (f *fakeMarkets) SetOpeningLine(_ context.Context, marketID, selectionID string,
	odds ledger.AmericanOdds, actorUserID, reason string) (bool, error) {
	f.lineCalls = append(f.lineCalls, setLineCall{marketID, selectionID, odds, actorUserID, reason})
	if f.setLineErr != nil {
		return false, f.setLineErr
	}
	return true, nil
}

func (f *fakeMarkets) SetMarketCloseTime(_ context.Context, marketID string, closesAt time.Time, actorUserID, reason string) error {
	f.closeTimes = append(f.closeTimes, closeTimeCall{marketID, closesAt, actorUserID, reason})
	return f.closeTimeErr
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
	record        []bettingpg.WagerRecordRow
	recordLimit   int
	voidErr       error
	voidCalls     []struct{ id, actor, reason string }
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
func (f *fakeWagers) VoidWager(_ context.Context, wagerID, actorUserID, reason string) (betting.Wager, error) {
	f.voidCalls = append(f.voidCalls, struct{ id, actor, reason string }{wagerID, actorUserID, reason})
	if f.voidErr != nil {
		return betting.Wager{}, f.voidErr
	}
	return betting.Wager{ID: betting.ID(wagerID), State: betting.WagerVoided,
		Stake: ledger.Money{Cents: 30_000, Currency: ledger.CAD}}, nil
}

func (f *fakeWagers) ListWagerRecord(_ context.Context, limit int) ([]bettingpg.WagerRecordRow, error) {
	f.recordLimit = limit
	return f.record, nil
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

type fakeMembers struct {
	book bettingpg.MemberBookRow
	err  error
	ids  []string
}

func (f *fakeMembers) MemberBook(_ context.Context, userID string, defaultAutoApproveCents int64) (bettingpg.MemberBookRow, error) {
	f.ids = append(f.ids, userID)
	if f.err != nil {
		return bettingpg.MemberBookRow{}, f.err
	}
	book := f.book
	if book.UserID == "" {
		book.UserID = userID
	}
	if book.AutoApproveLimit.Cents == 0 {
		book.AutoApproveLimit = ledger.Money{Cents: defaultAutoApproveCents, Currency: ledger.CAD}
	}
	return book, nil
}

type fakeLedger struct {
	rows []privateweb.LedgerRow
	ids  []string
}

func (f *fakeLedger) LedgerRows(_ context.Context, userID string) ([]privateweb.LedgerRow, error) {
	f.ids = append(f.ids, userID)
	return f.rows, nil
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
		Members:     &fakeMembers{},
		Ledger:      &fakeLedger{},
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
		Wagers:   wagers, Settlements: &fakeSettlements{}, Members: &fakeMembers{}, Ledger: &fakeLedger{}, AutoApproveMaxCents: 10_000,
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
		Wagers:   wagers, Settlements: &fakeSettlements{}, Members: &fakeMembers{}, Ledger: &fakeLedger{}, AutoApproveMaxCents: 1_000, // $10 threshold
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
		Wagers:   wagers, Settlements: &fakeSettlements{}, Members: &fakeMembers{}, Ledger: &fakeLedger{}, AutoApproveMaxCents: 1_000,
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

func recordRowFixture(opening, taken, closing int32, marketState betting.MarketState,
	result betting.SettlementResult) bettingpg.WagerRecordRow {
	openingOdds, _ := ledger.NewAmericanOdds(opening)
	takenOdds, _ := ledger.NewAmericanOdds(taken)
	closingOdds, _ := ledger.NewAmericanOdds(closing)
	stake := ledger.Money{Cents: 30_000, Currency: ledger.CAD}
	profit, _ := takenOdds.Profit(stake)
	state := betting.WagerAccepted
	if result != "" {
		state = betting.WagerSettled
	}
	return bettingpg.WagerRecordRow{
		ID: testWagerID, PlacedAt: time.Now().UTC(), MemberName: "Dan Guns",
		MarketTitle: "Cabot Cup 2026 Match 4", MarketState: marketState,
		SelectionTerms: "Bill, DC to win", OpeningOdds: openingOdds, TakenOdds: takenOdds,
		ClosingOdds: closingOdds, Stake: stake, PotentialProfit: profit,
		State: state, Result: result,
	}
}

func TestWagerRecordJudgesAgainstTheClosingLine(t *testing.T) {
	wagers := &fakeWagers{record: []bettingpg.WagerRecordRow{
		// Took -208 on a market that closed at -271: a better price than the
		// close, so the member beat the book on price.
		recordRowFixture(-180, -208, -271, betting.MarketSettled, betting.ResultLoss),
	}}
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, wagers, &fakeSettlements{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/wagers/record", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, expected := range []string{"-180", "-208", "-271", "Beat the close", "loss", "Dan Guns"} {
		if !strings.Contains(body, expected) {
			t.Errorf("record page does not contain %q", expected)
		}
	}
	// $300 at -208 wins $144.23; at the -271 close it would win $110.70, so
	// the member got $33.53 more than the close was paying.
	if !strings.Contains(body, "CA$33.53") {
		t.Error("record page does not quantify the edge against the close")
	}
}

func TestWagerRecordReportsBehindAndLevel(t *testing.T) {
	wagers := &fakeWagers{record: []bettingpg.WagerRecordRow{
		recordRowFixture(-180, -271, -208, betting.MarketSettled, betting.ResultWin),
		recordRowFixture(-110, -110, -110, betting.MarketClosed, ""),
	}}
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, wagers, &fakeSettlements{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/wagers/record", nil))

	body := w.Body.String()
	if !strings.Contains(body, "Behind the close") {
		t.Error("a worse price than the close should read as behind it")
	}
	if !strings.Contains(body, "Level") {
		t.Error("an unmoved line should read as level")
	}
}

// A line that is still moving is not a closing line, so the record must not
// pass judgement on a wager whose market is still taking action.
func TestWagerRecordWithholdsTheVerdictWhileTheMarketIsOpen(t *testing.T) {
	wagers := &fakeWagers{record: []bettingpg.WagerRecordRow{
		recordRowFixture(-180, -208, -271, betting.MarketOpen, ""),
	}}
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, wagers, &fakeSettlements{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/wagers/record", nil))

	body := w.Body.String()
	if strings.Contains(body, "Beat the close") || strings.Contains(body, "Behind the close") {
		t.Fatal("the record judged a wager against a line that is still moving")
	}
	for _, expected := range []string{"still moving", "Market still open"} {
		if !strings.Contains(body, expected) {
			t.Errorf("record page does not contain %q", expected)
		}
	}
}

func TestWagerRecordIsAdminOnly(t *testing.T) {
	wagers := &fakeWagers{record: []bettingpg.WagerRecordRow{
		recordRowFixture(-180, -208, -271, betting.MarketSettled, betting.ResultLoss),
	}}
	handler := newTestHandlerWithSettlements(t, memberSession(), &fakeMarkets{}, wagers, &fakeSettlements{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/wagers/record", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "Dan Guns") {
		t.Fatal("a member saw another member's wager record")
	}
}

func setLineForm(odds, reason string) string {
	return url.Values{"csrf_token": {testCSRF}, "odds": {odds}, "reason": {reason}}.Encode()
}

func setLinePath() string {
	return "/admin/markets/" + testMarketID + "/selections/" + testSelID + "/line"
}

func TestSetLineSendsTheOpeningPriceAndSessionActor(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	r := httptest.NewRequest(http.MethodPost, setLinePath(), strings.NewReader(setLineForm("-150", "steam on the other side")))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.lineCalls) != 1 {
		t.Fatalf("SetOpeningLine calls = %d, want 1", len(markets.lineCalls))
	}
	call := markets.lineCalls[0]
	if call.marketID != testMarketID || call.selectionID != testSelID {
		t.Fatalf("call targeted %s/%s", call.marketID, call.selectionID)
	}
	if call.odds != -150 || call.reason != "steam on the other side" || call.actor != testUserID {
		t.Fatalf("call = %+v", call)
	}
}

func TestSetLineAcceptsTheWayAdminsWriteOdds(t *testing.T) {
	for _, test := range []struct {
		input string
		want  ledger.AmericanOdds
	}{
		{"-150", -150}, {"+120", 120}, {"120", 120}, {" -110 ", -110},
	} {
		t.Run(test.input, func(t *testing.T) {
			markets := &fakeMarkets{}
			handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})
			r := httptest.NewRequest(http.MethodPost, setLinePath(), strings.NewReader(setLineForm(test.input, "adjusting")))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", w.Code)
			}
			if markets.lineCalls[0].odds != test.want {
				t.Fatalf("odds = %d, want %d", markets.lineCalls[0].odds, test.want)
			}
		})
	}
}

func TestSetLineRejectsBadInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		status int
	}{
		// American odds exclude the ambiguous range between -100 and +100.
		{"inside the dead band", setLineForm("50", "nope"), http.StatusBadRequest},
		{"not a number", setLineForm("evens", "nope"), http.StatusBadRequest},
		{"no reason", setLineForm("-150", "   "), http.StatusBadRequest},
		{"no csrf token", url.Values{"odds": {"-150"}, "reason": {"x"}}.Encode(), http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			markets := &fakeMarkets{}
			handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})
			r := httptest.NewRequest(http.MethodPost, setLinePath(), strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if len(markets.lineCalls) != 0 {
				t.Fatal("a rejected line change reached the store")
			}
		})
	}
}

func TestSetLineIsAdminOnly(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, memberSession(), markets, &fakeWagers{})

	r := httptest.NewRequest(http.MethodPost, setLinePath(), strings.NewReader(setLineForm("-150", "sneaky")))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(markets.lineCalls) != 0 {
		t.Fatal("a member moved a line")
	}
}

func TestSetLineReportsAClosedMarket(t *testing.T) {
	markets := &fakeMarkets{setLineErr: bettingpg.ErrMarketNotPriceable}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	r := httptest.NewRequest(http.MethodPost, setLinePath(), strings.NewReader(setLineForm("-150", "too late")))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "draft or open") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

// An outright can name the whole field, so the form parser must read every
// outcome row submitted rather than a fixed number.
func TestCreateMarketAcceptsAFullFieldOfOutcomes(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	form := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "market_type": {"future"},
		"title": {"Leading points getter"}, "currency": {"CAD"},
		"closes_at": {time.Now().Add(48 * time.Hour).Format("2006-01-02T15:04")},
	}
	// Sixteen runners, the shape that prompted this.
	prices := []string{"500", "600", "600", "900", "900", "900", "900", "1400",
		"1400", "1800", "1800", "2500", "2500", "2500", "2500", "3000"}
	for i, price := range prices {
		slot := i + 1
		form.Set(fmt.Sprintf("selection_terms_%d", slot), fmt.Sprintf("Runner %d", slot))
		form.Set(fmt.Sprintf("selection_sign_%d", slot), "+")
		form.Set(fmt.Sprintf("selection_odds_%d", slot), price)
	}

	r := httptest.NewRequest(http.MethodPost, "/admin/markets", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.createCalls) != 1 {
		t.Fatalf("CreateMarket calls = %d, want 1", len(markets.createCalls))
	}
	created := markets.createCalls[0]
	if len(created.Selections) != 16 {
		t.Fatalf("selections = %d, want all 16 outcomes", len(created.Selections))
	}
	if created.Selections[0].OfferedAmericanOdds != 500 || created.Selections[15].OfferedAmericanOdds != 3000 {
		t.Fatalf("first/last odds = %d/%d, want +500/+3000",
			created.Selections[0].OfferedAmericanOdds, created.Selections[15].OfferedAmericanOdds)
	}
	// Keys must stay unique, or the store rejects the market.
	seen := map[string]bool{}
	for _, selection := range created.Selections {
		if seen[selection.Key] {
			t.Fatalf("duplicate selection key %q", selection.Key)
		}
		seen[selection.Key] = true
	}
}

// Rows left blank in the middle are skipped, not treated as errors: an admin
// adding rows and changing their mind must not be blocked.
func TestCreateMarketSkipsBlankOutcomeRows(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	form := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "market_type": {"prop"},
		"title": {"Over/under 0.5 eagles"}, "currency": {"CAD"},
		"closes_at":         {time.Now().Add(24 * time.Hour).Format("2006-01-02T15:04")},
		"selection_terms_1": {"Over"}, "selection_sign_1": {"+"}, "selection_odds_1": {"120"},
		// slots 2 and 3 left entirely blank
		"selection_terms_4": {"Under"}, "selection_sign_4": {"-"}, "selection_odds_4": {"150"},
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/markets", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	created := markets.createCalls[0]
	if len(created.Selections) != 2 {
		t.Fatalf("selections = %d, want the 2 filled rows", len(created.Selections))
	}
	if created.Selections[0].DisplayTerms != "Over" || created.Selections[1].DisplayTerms != "Under" {
		t.Fatalf("selections = %+v", created.Selections)
	}
	if created.Selections[0].OfferedAmericanOdds != 120 || created.Selections[1].OfferedAmericanOdds != -150 {
		t.Fatalf("odds = %d/%d, want +120/-150",
			created.Selections[0].OfferedAmericanOdds, created.Selections[1].OfferedAmericanOdds)
	}
}

// A form that comes back from a validation error must keep every row the admin
// filled in, however many they added.
func TestCreateMarketFormKeepsAddedRowsAfterAnError(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	form := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "type": {"future"},
		"title":     {""}, // invalid: forces the form back
		"currency":  {"CAD"},
		"closes_at": {time.Now().Add(24 * time.Hour).Format("2006-01-02T15:04")},
	}
	for slot := 1; slot <= 12; slot++ {
		form.Set(fmt.Sprintf("selection_terms_%d", slot), fmt.Sprintf("Runner %d", slot))
		form.Set(fmt.Sprintf("selection_sign_%d", slot), "+")
		form.Set(fmt.Sprintf("selection_odds_%d", slot), "900")
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/markets", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "selection_terms_12") {
		t.Fatal("the re-rendered form dropped outcome rows the admin had added")
	}
	if !strings.Contains(body, "Runner 12") {
		t.Fatal("the re-rendered form lost the values in the added rows")
	}
	if len(markets.createCalls) != 0 {
		t.Fatal("an invalid market reached the store")
	}
}

func TestSelectionSlotsGrowWithTheForm(t *testing.T) {
	if got := len(selectionSlots(nil)); got != startingSelectionSlots {
		t.Fatalf("blank form slots = %d, want %d", got, startingSelectionSlots)
	}
	form := url.Values{"selection_terms_9": {"Runner 9"}}
	if got := len(selectionSlots(form)); got != 10 {
		t.Fatalf("slots for a form using row 9 = %d, want 10", got)
	}
	// The last row still renders one spare below it, but never past the cap.
	atCap := url.Values{fmt.Sprintf("selection_terms_%d", bettingpg.MaxSelectionsPerMarket): {"last runner"}}
	if got := len(selectionSlots(atCap)); got != bettingpg.MaxSelectionsPerMarket {
		t.Fatalf("slots for a full form = %d, want the cap %d", got, bettingpg.MaxSelectionsPerMarket)
	}
	// A row number past the cap is ignored rather than growing the form: the
	// store would reject it anyway.
	beyond := url.Values{fmt.Sprintf("selection_terms_%d", bettingpg.MaxSelectionsPerMarket+5): {"too far"}}
	if got := len(selectionSlots(beyond)); got != startingSelectionSlots {
		t.Fatalf("slots for an out-of-range row = %d, want the starting %d", got, startingSelectionSlots)
	}
}

func TestMemberBoardIsScopedToTheMember(t *testing.T) {
	markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newTestHandler(t, memberSession(), markets, &fakeWagers{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/book/markets", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// The board must be read through the user-scoped reader, or restrictions
	// would never be applied to what a member sees.
	if len(markets.scopedUsers) != 1 || markets.scopedUsers[0] != testUserID {
		t.Fatalf("scoped reads = %v, want one for the session user", markets.scopedUsers)
	}
}

// Hiding a bet is a courtesy; the refusal is the control. A member who
// reconstructs the form for a market they cannot see must still be refused.
func TestPlaceWagerStillRefusedWhenTheMarketIsHiddenFromTheMember(t *testing.T) {
	wagers := &fakeWagers{placeErr: betting.ErrUserRestricted}
	markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newTestHandler(t, memberSession(), markets, wagers)

	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"idempotency_key": {testIdem}, "stake": {"25.00"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/book/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not able to bet on this market") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestRestrictMemberSendsTheWholeMarketOrOneSide(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection string
	}{
		{"whole market", ""},
		{"one side", testSelID},
	} {
		t.Run(test.name, func(t *testing.T) {
			markets := &fakeMarkets{}
			handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})
			body := url.Values{
				"csrf_token": {testCSRF}, "user_id": {testUserID},
				"selection_id": {test.selection}, "reason": {"the bet is about them"},
			}.Encode()
			r := httptest.NewRequest(http.MethodPost, "/admin/markets/"+testMarketID+"/restrict", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
			}
			if len(markets.restricted) != 1 {
				t.Fatalf("RestrictMember calls = %d, want 1", len(markets.restricted))
			}
			got := markets.restricted[0]
			if got.MarketID != testMarketID || got.UserID != testUserID || got.SelectionID != test.selection {
				t.Fatalf("restriction = %+v", got)
			}
			if got.ActorUserID != testUserID || got.Reason != "the bet is about them" {
				t.Fatalf("restriction actor/reason = %q/%q", got.ActorUserID, got.Reason)
			}
		})
	}
}

func TestRestrictMemberRejectsBadInputAndNonAdmins(t *testing.T) {
	valid := url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID}, "reason": {"because"}}
	for _, test := range []struct {
		name    string
		session privateweb.Session
		body    url.Values
		status  int
	}{
		{"member cannot restrict", memberSession(), valid, http.StatusForbidden},
		{"no reason", adminSession(), url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID}, "reason": {"  "}}, http.StatusBadRequest},
		{"no member", adminSession(), url.Values{"csrf_token": {testCSRF}, "reason": {"because"}}, http.StatusBadRequest},
		{"malformed selection", adminSession(), url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID}, "selection_id": {"nope"}, "reason": {"because"}}, http.StatusBadRequest},
		{"no csrf token", adminSession(), url.Values{"user_id": {testUserID}, "reason": {"because"}}, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			markets := &fakeMarkets{}
			handler := newTestHandler(t, test.session, markets, &fakeWagers{})
			r := httptest.NewRequest(http.MethodPost, "/admin/markets/"+testMarketID+"/restrict", strings.NewReader(test.body.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if len(markets.restricted) != 0 {
				t.Fatal("a rejected restriction reached the store")
			}
		})
	}
}

func TestLiftRestrictionTargetsTheRightRow(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	body := url.Values{"csrf_token": {testCSRF}, "user_id": {testUserID}, "selection_id": {testSelID}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/markets/"+testMarketID+"/restrict/lift", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.lifted) != 1 {
		t.Fatalf("LiftRestriction calls = %d, want 1", len(markets.lifted))
	}
	got := markets.lifted[0]
	if got.market != testMarketID || got.user != testUserID || got.selection != testSelID {
		t.Fatalf("lifted = %+v", got)
	}
}

func TestAdminMarketsShowsRestrictionsAndThePicker(t *testing.T) {
	markets := &fakeMarkets{
		all:     []bettingpg.MarketRow{openMarketFixture()},
		members: []bettingpg.MemberOption{{ID: testUserID, Name: "Ryan Theriault"}},
		restrictions: []bettingpg.RestrictionRow{{
			MarketID: testMarketID, MarketTitle: "Over/under 0.5 eagles", UserID: testUserID,
			MemberName: "Ryan Theriault", SelectionID: testSelID, SelectionTerms: "Under",
			Reason: "the bet is about them", RestrictedBy: "Book Admin",
		}},
	}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/markets", nil))

	body := w.Body.String()
	for _, expected := range []string{
		"Ryan Theriault", "Under", "the bet is about them", "Restrict a member",
		"The whole market", // the picker's default scope
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("markets page does not contain %q", expected)
		}
	}
}

func voidPath() string { return "/admin/wagers/" + testWagerID + "/void" }

func TestVoidWagerSendsTheReasonAndSessionActor(t *testing.T) {
	wagers := &fakeWagers{}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}, "reason": {"struck in error"}}.Encode()
	r := httptest.NewRequest(http.MethodPost, voidPath(), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(wagers.voidCalls) != 1 {
		t.Fatalf("VoidWager calls = %d, want 1", len(wagers.voidCalls))
	}
	call := wagers.voidCalls[0]
	if call.id != testWagerID || call.actor != testUserID || call.reason != "struck in error" {
		t.Fatalf("call = %+v", call)
	}
}

func TestVoidWagerRejectsBadInputAndNonAdmins(t *testing.T) {
	for _, test := range []struct {
		name    string
		session privateweb.Session
		body    string
		path    string
		status  int
	}{
		{"member cannot void", memberSession(), url.Values{"csrf_token": {testCSRF}, "reason": {"nope"}}.Encode(), voidPath(), http.StatusForbidden},
		{"no reason", adminSession(), url.Values{"csrf_token": {testCSRF}, "reason": {"  "}}.Encode(), voidPath(), http.StatusBadRequest},
		{"no csrf token", adminSession(), url.Values{"reason": {"nope"}}.Encode(), voidPath(), http.StatusForbidden},
		{"malformed wager", adminSession(), url.Values{"csrf_token": {testCSRF}, "reason": {"nope"}}.Encode(), "/admin/wagers/not-a-uuid/void", http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			wagers := &fakeWagers{}
			handler := newTestHandler(t, test.session, &fakeMarkets{}, wagers)
			r := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if len(wagers.voidCalls) != 0 {
				t.Fatal("a rejected void reached the store")
			}
		})
	}
}

func TestVoidWagerExplainsAWagerThatCannotBeVoided(t *testing.T) {
	wagers := &fakeWagers{voidErr: betting.ErrInvalidTransition}
	handler := newTestHandler(t, adminSession(), &fakeMarkets{}, wagers)

	body := url.Values{"csrf_token": {testCSRF}, "reason": {"too late"}}.Encode()
	r := httptest.NewRequest(http.MethodPost, voidPath(), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Only an accepted wager can be voided") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

// The record page offers Void on live action only.
func TestWagerRecordOffersVoidOnAcceptedWagersOnly(t *testing.T) {
	wagers := &fakeWagers{record: []bettingpg.WagerRecordRow{
		recordRowFixture(-180, -208, -271, betting.MarketOpen, ""),
	}}
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, wagers, &fakeSettlements{})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/wagers/record", nil))
	if !strings.Contains(w.Body.String(), "/void") {
		t.Fatal("an accepted wager should offer Void")
	}

	// A settled wager has already paid: voiding it is not on offer.
	wagers.record = []bettingpg.WagerRecordRow{
		recordRowFixture(-180, -208, -271, betting.MarketSettled, betting.ResultLoss),
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/wagers/record", nil))
	if strings.Contains(w.Body.String(), "/void") {
		t.Fatal("a settled wager must not offer Void")
	}
}

// Both wager views must be reachable in one tap from either page and from the
// sidebar: the record was previously only findable through a link inside a
// sentence.
func TestWagerViewsAreEasyToMoveBetween(t *testing.T) {
	wagers := &fakeWagers{record: []bettingpg.WagerRecordRow{
		recordRowFixture(-180, -208, -271, betting.MarketOpen, ""),
	}}
	handler := newTestHandlerWithSettlements(t, adminSession(), &fakeMarkets{}, wagers, &fakeSettlements{})

	for path, current := range map[string]string{
		"/admin/wagers":        `href="/admin/wagers" aria-current="page"`,
		"/admin/wagers/record": `href="/admin/wagers/record" aria-current="page"`,
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if !strings.Contains(body, `class="private-tabs"`) {
			t.Errorf("%s has no tab strip", path)
		}
		// Both destinations are present on both pages.
		if !strings.Contains(body, `<a href="/admin/wagers/record"`) || !strings.Contains(body, `<a href="/admin/wagers"`) {
			t.Errorf("%s does not link to both wager views", path)
		}
		// And the tab for the page you are on is marked as current.
		if !strings.Contains(body, current) {
			t.Errorf("%s does not mark its own tab as current", path)
		}
	}
}

func memberBookFixture() *fakeMembers {
	return &fakeMembers{book: bettingpg.MemberBookRow{
		UserID: testUserID, Name: "Ryan Theriault", Email: "ryan@example.test",
		Balance:          ledger.Money{Cents: -25_000, Currency: ledger.CAD},
		CreditLimit:      ledger.Money{Cents: 150_000, Currency: ledger.CAD},
		CreditAvailable:  ledger.Money{Cents: 125_000, Currency: ledger.CAD},
		AutoApproveLimit: ledger.Money{Cents: 10_000, Currency: ledger.CAD},
	}}
}

func newMemberBookHandler(t *testing.T, session privateweb.Session, markets *fakeMarkets,
	wagers *fakeWagers, members *fakeMembers, entries *fakeLedger) *Handler {
	t.Helper()
	handler, err := New(Dependencies{
		Sessions: fakeSessions{session: session}, Markets: markets, Wagers: wagers,
		Settlements: &fakeSettlements{}, Members: members, Ledger: entries,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func TestMemberBookShowsStandingWagersAndLedger(t *testing.T) {
	members := memberBookFixture()
	entries := &fakeLedger{rows: []privateweb.LedgerRow{{
		OccurredAt: time.Now().UTC(), Description: "Wager accepted", TransactionType: "wager_acceptance",
		Reference: "wager:abc", Amount: ledger.Money{Cents: -5_000, Currency: ledger.CAD},
	}}}
	wagers := &fakeWagers{mine: []bettingpg.UserWagerRow{
		myWagerFixture(betting.WagerAccepted, -208, -208, ""),
	}}
	markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newMemberBookHandler(t, adminSession(), markets, wagers, members, entries)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/members/"+testUserID+"/book", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, expected := range []string{
		"Ryan Theriault", "-CA$250.00", "CA$1500.00", "CA$1250.00", // balance, credit line, left to stake
		"Wager accepted", "Match 4", // ledger and their wagers
		"Place a wager for Ryan Theriault",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("member book does not contain %q", expected)
		}
	}
	// Every read is scoped to the member in the path, not the admin's session.
	if len(members.ids) != 1 || members.ids[0] != testUserID {
		t.Fatalf("member book reads = %v, want the member from the path", members.ids)
	}
	if len(entries.ids) != 1 || entries.ids[0] != testUserID {
		t.Fatalf("ledger reads = %v, want the member from the path", entries.ids)
	}
	// The board offered is the member's own, so restrictions apply to it.
	if len(markets.scopedUsers) != 1 || markets.scopedUsers[0] != testUserID {
		t.Fatalf("market reads = %v, want them scoped to the member", markets.scopedUsers)
	}
}

func TestMemberBookIsAdminOnly(t *testing.T) {
	members := memberBookFixture()
	handler := newMemberBookHandler(t, memberSession(), &fakeMarkets{}, &fakeWagers{}, members, &fakeLedger{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/members/"+testUserID+"/book", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "Ryan Theriault") {
		t.Fatal("a member saw another member's account")
	}
	if len(members.ids) != 0 {
		t.Fatal("a member request reached the member book reader")
	}
}

func TestPlaceForMemberRecordsWhoPlacedItAndFillsImmediately(t *testing.T) {
	wagers := &fakeWagers{}
	markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newMemberBookHandler(t, adminSession(), markets, wagers, memberBookFixture(), &fakeLedger{})

	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"stake": {"25.00"}, "member_name": {"Ryan Theriault"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/members/"+testSelID+"/wagers", strings.NewReader(body))
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
	// The wager belongs to the member in the path; the admin is recorded as
	// having placed it.
	if placed.UserID != testSelID {
		t.Fatalf("wager user = %q, want the member from the path", placed.UserID)
	}
	if placed.PlacedByUserID != testUserID {
		t.Fatalf("placed by = %q, want the admin's session user", placed.PlacedByUserID)
	}
	if placed.StakeCents != 2_500 || placed.Currency != ledger.CAD {
		t.Fatalf("stake = %d %s", placed.StakeCents, placed.Currency)
	}
	// It fills straight away rather than queueing, and the admin is the actor.
	if len(wagers.acceptCalls) != 1 {
		t.Fatalf("AcceptWager calls = %d, want the wager filled immediately", len(wagers.acceptCalls))
	}
}

// The ID and idempotency key must be generated server-side: a replayed form
// must not be able to aim at an existing wager or place the same bet twice.
func TestPlaceForMemberIgnoresFormSuppliedIdentifiers(t *testing.T) {
	wagers := &fakeWagers{}
	markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newMemberBookHandler(t, adminSession(), markets, wagers, memberBookFixture(), &fakeLedger{})

	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID},
		"stake": {"25.00"}, "wager_id": {testWagerID}, "idempotency_key": {testIdem},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/members/"+testSelID+"/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	placed := wagers.placed[0]
	if placed.WagerID == testWagerID || placed.IdempotencyKey == testIdem {
		t.Fatalf("form-supplied identifiers were used: %+v", placed)
	}
	if !isUUID(placed.WagerID) || !isUUID(placed.IdempotencyKey) {
		t.Fatalf("generated identifiers are not UUIDs: %+v", placed)
	}
}

// A market the member is restricted from is not on their board, so it cannot
// be bet for them here either.
func TestPlaceForMemberRefusesAMarketNotOpenToThem(t *testing.T) {
	wagers := &fakeWagers{}
	// No markets on this member's board: everything is restricted or closed.
	markets := &fakeMarkets{open: nil}
	handler := newMemberBookHandler(t, adminSession(), markets, wagers, memberBookFixture(), &fakeLedger{})

	body := url.Values{
		"csrf_token": {testCSRF}, "market_id": {testMarketID}, "selection_id": {testSelID}, "stake": {"25.00"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/members/"+testSelID+"/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if len(wagers.placed) != 0 {
		t.Fatal("a wager was placed into a market not open to that member")
	}
}

func TestPlaceForMemberRejectsBadInputAndNonAdmins(t *testing.T) {
	valid := url.Values{"csrf_token": {testCSRF}, "market_id": {testMarketID},
		"selection_id": {testSelID}, "stake": {"25.00"}}
	for _, test := range []struct {
		name    string
		session privateweb.Session
		body    url.Values
		path    string
		status  int
	}{
		{"member cannot place for others", memberSession(), valid, "/admin/members/" + testSelID + "/wagers", http.StatusForbidden},
		{"no csrf token", adminSession(), url.Values{"market_id": {testMarketID}, "selection_id": {testSelID}, "stake": {"25.00"}}, "/admin/members/" + testSelID + "/wagers", http.StatusForbidden},
		{"malformed member", adminSession(), valid, "/admin/members/nope/wagers", http.StatusNotFound},
		{"bad stake", adminSession(), url.Values{"csrf_token": {testCSRF}, "market_id": {testMarketID},
			"selection_id": {testSelID}, "stake": {"lots"}}, "/admin/members/" + testSelID + "/wagers", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			wagers := &fakeWagers{}
			markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
			handler := newMemberBookHandler(t, test.session, markets, wagers, memberBookFixture(), &fakeLedger{})
			r := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if len(wagers.placed) != 0 {
				t.Fatal("a rejected placement reached the store")
			}
		})
	}
}

// If the member's credit will not cover it, the wager is left pending rather
// than failing silently, and the admin is told why.
func TestPlaceForMemberLeavesItPendingWhenCreditWillNotCoverIt(t *testing.T) {
	wagers := &fakeWagers{acceptErr: bettingpg.ErrInsufficientFunds}
	markets := &fakeMarkets{open: []bettingpg.MarketRow{openMarketFixture()}}
	handler := newMemberBookHandler(t, adminSession(), markets, wagers, memberBookFixture(), &fakeLedger{})

	body := url.Values{"csrf_token": {testCSRF}, "market_id": {testMarketID},
		"selection_id": {testSelID}, "stake": {"25.00"}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/admin/members/"+testSelID+"/wagers", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if len(wagers.placed) != 1 {
		t.Fatal("the wager was not placed at all")
	}
	body2 := w.Body.String()
	if !strings.Contains(body2, "approval queue") || !strings.Contains(body2, "does not cover this stake") {
		t.Fatalf("body = %q, want it to explain the wager is pending and why", body2)
	}
}

func closeTimePath() string { return "/admin/markets/" + testMarketID + "/close-time" }

func TestSetCloseTimeSendsTheNewTimeAndActor(t *testing.T) {
	markets := &fakeMarkets{}
	handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})

	when := time.Now().Add(26 * time.Hour).Truncate(time.Minute)
	body := url.Values{
		"csrf_token": {testCSRF}, "closes_at": {when.Format("2006-01-02T15:04")},
		"reason": {"tee times moved to the afternoon"},
	}.Encode()
	r := httptest.NewRequest(http.MethodPost, closeTimePath(), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if len(markets.closeTimes) != 1 {
		t.Fatalf("SetMarketCloseTime calls = %d, want 1", len(markets.closeTimes))
	}
	call := markets.closeTimes[0]
	if call.marketID != testMarketID || call.actor != testUserID {
		t.Fatalf("call = %+v", call)
	}
	if call.reason != "tee times moved to the afternoon" {
		t.Fatalf("reason = %q", call.reason)
	}
	// The time is read as Atlantic, the same as everywhere else in the app.
	if call.closesAt.IsZero() {
		t.Fatal("the new closing time did not reach the store")
	}
}

func TestSetCloseTimeRejectsBadInputAndNonAdmins(t *testing.T) {
	future := time.Now().Add(26 * time.Hour).Format("2006-01-02T15:04")
	for _, test := range []struct {
		name    string
		session privateweb.Session
		body    url.Values
		status  int
	}{
		{"member cannot move it", memberSession(),
			url.Values{"csrf_token": {testCSRF}, "closes_at": {future}, "reason": {"nope"}}, http.StatusForbidden},
		{"no reason", adminSession(),
			url.Values{"csrf_token": {testCSRF}, "closes_at": {future}, "reason": {"  "}}, http.StatusBadRequest},
		{"unparseable time", adminSession(),
			url.Values{"csrf_token": {testCSRF}, "closes_at": {"whenever"}, "reason": {"x"}}, http.StatusBadRequest},
		{"no csrf token", adminSession(),
			url.Values{"closes_at": {future}, "reason": {"x"}}, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			markets := &fakeMarkets{}
			handler := newTestHandler(t, test.session, markets, &fakeWagers{})
			r := httptest.NewRequest(http.MethodPost, closeTimePath(), strings.NewReader(test.body.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if len(markets.closeTimes) != 0 {
				t.Fatal("a rejected close-time change reached the store")
			}
		})
	}
}

func TestSetCloseTimeExplainsAPastTimeAndAClosedMarket(t *testing.T) {
	future := time.Now().Add(26 * time.Hour).Format("2006-01-02T15:04")
	for _, test := range []struct {
		name   string
		err    error
		status int
		expect string
	}{
		{"already passed", bettingpg.ErrCloseTimeInPast, http.StatusBadRequest, "use Close"},
		{"market closed", bettingpg.ErrMarketNotPriceable, http.StatusConflict, "draft or open"},
	} {
		t.Run(test.name, func(t *testing.T) {
			markets := &fakeMarkets{closeTimeErr: test.err}
			handler := newTestHandler(t, adminSession(), markets, &fakeWagers{})
			body := url.Values{"csrf_token": {testCSRF}, "closes_at": {future}, "reason": {"moving it"}}.Encode()
			r := httptest.NewRequest(http.MethodPost, closeTimePath(), strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("HX-Request", "true")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if !strings.Contains(w.Body.String(), test.expect) {
				t.Fatalf("body = %q, want it to mention %q", w.Body.String(), test.expect)
			}
		})
	}
}
