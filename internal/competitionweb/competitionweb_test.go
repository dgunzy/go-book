package competitionweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/privateweb"
)

const (
	csrf    = "csrf-secret"
	userID  = "11111111-1111-1111-1111-111111111111"
	matchID = "22222222-2222-2222-2222-222222222222"
)

type fakeSessions struct {
	session privateweb.Session
	err     error
}

func (f fakeSessions) CurrentSession(*http.Request) (privateweb.Session, error) {
	return f.session, f.err
}

type fakeComp struct {
	resultCalls []competitionpg.RecordResultRequest
	created     competitionpg.MatchCreated
	createErr   error
}

func (f *fakeComp) ListEvents(context.Context) ([]competitionpg.EventRow, error) { return nil, nil }
func (f *fakeComp) CreateEvent(context.Context, competitionpg.CreateEventRequest) (string, error) {
	return "e1", nil
}
func (f *fakeComp) CreateTeam(context.Context, string, string, string) (string, error) {
	return "t1", nil
}
func (f *fakeComp) CreateMatch(context.Context, string, string, string, string, string) (competitionpg.MatchCreated, error) {
	return f.created, f.createErr
}
func (f *fakeComp) RecordAdminResult(_ context.Context, req competitionpg.RecordResultRequest) (string, error) {
	f.resultCalls = append(f.resultCalls, req)
	return "v1", nil
}

func handler(t *testing.T, role privateweb.Role, comp *fakeComp) *Handler {
	t.Helper()
	h, err := New(Dependencies{
		Sessions:    fakeSessions{session: privateweb.Session{UserID: userID, Role: role, Active: true, CSRFToken: csrf}},
		Competition: comp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func post(path string, v url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestMemberForbidden(t *testing.T) {
	h := handler(t, privateweb.RoleMember, &fakeComp{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/matches", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
}

func TestRecordResultRequiresCSRF(t *testing.T) {
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches/"+matchID+"/result", url.Values{"winner": {"side_1"}, "reason": {"x"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(comp.resultCalls) != 0 {
		t.Fatal("RecordAdminResult called without CSRF")
	}
}

func TestRecordResultHappyPath(t *testing.T) {
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches/"+matchID+"/result", url.Values{
		"csrf_token": {csrf}, "winner": {"side_1"}, "score": {"3 & 2"}, "reason": {"final card"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(comp.resultCalls) != 1 || comp.resultCalls[0].MatchID != matchID || comp.resultCalls[0].ActorUserID != userID {
		t.Fatalf("result calls = %+v", comp.resultCalls)
	}
}

func TestCreateMatchShowsIDs(t *testing.T) {
	comp := &fakeComp{created: competitionpg.MatchCreated{MatchID: matchID, Side1ID: "s1", Side2ID: "s2"}}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches", url.Values{
		"csrf_token": {csrf}, "event_id": {"33333333-3333-3333-3333-333333333333"},
		"side1_team_id": {"44444444-4444-4444-4444-444444444444"},
		"side2_team_id": {"55555555-5555-5555-5555-555555555555"}, "format": {"singles"},
	}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "side:s1") {
		t.Fatalf("status=%d, expected side IDs shown; body=%q", rec.Code, rec.Body.String())
	}
}
