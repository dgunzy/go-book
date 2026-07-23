package competitionweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	matchReqs   []competitionpg.CreateMatchRequest
	events      []competitionpg.EventRow
	players     []competitionpg.PlayerRow
	created     competitionpg.MatchCreated
	createErr   error
	deleteErr   error
	deleted     []string
	rosterReqs  []competitionpg.SetTeamMemberRequest
	rosterError error
}

func (f *fakeComp) ListEvents(context.Context) ([]competitionpg.EventRow, error) {
	return f.events, nil
}
func (f *fakeComp) ListPlayers(context.Context) ([]competitionpg.PlayerRow, error) {
	return f.players, nil
}
func (f *fakeComp) ListStandings(context.Context) ([]competitionpg.StandingRow, error) {
	return nil, nil
}
func (f *fakeComp) CreateEvent(context.Context, competitionpg.CreateEventRequest) (string, error) {
	return "e1", nil
}
func (f *fakeComp) CreatePlayer(context.Context, string, string) (string, error) { return "p1", nil }
func (f *fakeComp) CreateTeam(context.Context, string, string, string) (string, error) {
	return "t1", nil
}
func (f *fakeComp) SetTeamMember(_ context.Context, req competitionpg.SetTeamMemberRequest) error {
	f.rosterReqs = append(f.rosterReqs, req)
	return f.rosterError
}
func (f *fakeComp) RemoveTeamMember(_ context.Context, eventID, teamID, playerID, actor, reason string) error {
	f.deleted = append(f.deleted, "roster:"+eventID+":"+teamID+":"+playerID+":"+actor+":"+reason)
	return f.rosterError
}
func (f *fakeComp) CreateMatch(_ context.Context, req competitionpg.CreateMatchRequest) (competitionpg.MatchCreated, error) {
	f.matchReqs = append(f.matchReqs, req)
	return f.created, f.createErr
}
func (f *fakeComp) RecordAdminResult(_ context.Context, req competitionpg.RecordResultRequest) (string, error) {
	f.resultCalls = append(f.resultCalls, req)
	return "v1", nil
}
func (f *fakeComp) DeleteMatch(_ context.Context, id, actor, reason string) error {
	f.deleted = append(f.deleted, "match:"+id+":"+actor+":"+reason)
	return f.deleteErr
}
func (f *fakeComp) DeleteTeam(_ context.Context, eventID, teamID, actor, reason string) error {
	f.deleted = append(f.deleted, "team:"+eventID+":"+teamID+":"+actor+":"+reason)
	return f.deleteErr
}
func (f *fakeComp) DeleteEvent(_ context.Context, id, actor, reason string) error {
	f.deleted = append(f.deleted, "event:"+id+":"+actor+":"+reason)
	return f.deleteErr
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

func TestCreateMatchPointsToReadableMarketPicker(t *testing.T) {
	comp := &fakeComp{created: competitionpg.MatchCreated{MatchID: matchID, Side1ID: "s1", Side2ID: "s2"}}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches", url.Values{
		"csrf_token": {csrf}, "event_id": {"33333333-3333-3333-3333-333333333333"},
		"side1_team_id": {"44444444-4444-4444-4444-444444444444"},
		"side2_team_id": {"55555555-5555-5555-5555-555555555555"}, "format": {"singles"},
		"side1_players": {"66666666-6666-6666-6666-666666666666"},
		"side2_players": {"77777777-7777-7777-7777-777777777777"},
	}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "available by name when you create a Match market") {
		t.Fatalf("status=%d, expected market picker notice; body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "side:s1") {
		t.Fatal("match page exposed internal side IDs")
	}
}

func TestCreateMatchRequiresPlayersForFormat(t *testing.T) {
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches", url.Values{
		"csrf_token": {csrf}, "event_id": {"33333333-3333-3333-3333-333333333333"},
		"side1_team_id": {"44444444-4444-4444-4444-444444444444"},
		"side2_team_id": {"55555555-5555-5555-5555-555555555555"}, "format": {"fourball"},
		"side1_players": {"66666666-6666-6666-6666-666666666666"},
		"side2_players": {"77777777-7777-7777-7777-777777777777"},
	}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "exactly 2 players per side") || strings.Contains(rec.Body.String(), "invalid competition data") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(comp.matchReqs) != 0 {
		t.Fatalf("CreateMatch calls = %d, want none", len(comp.matchReqs))
	}
}

func TestDeleteMatchRequiresCSRF(t *testing.T) {
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches/"+matchID+"/delete", url.Values{"reason": {"duplicate"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(comp.deleted) != 0 {
		t.Fatalf("delete calls = %v, want none", comp.deleted)
	}
}

func TestDeleteMatchPassesActorAndAuditReason(t *testing.T) {
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/matches/"+matchID+"/delete", url.Values{
		"csrf_token": {csrf}, "reason": {"duplicate test match"},
	}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Unused match deleted") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	want := "match:" + matchID + ":" + userID + ":duplicate test match"
	if len(comp.deleted) != 1 || comp.deleted[0] != want {
		t.Fatalf("delete calls = %v, want %q", comp.deleted, want)
	}
}

func TestDeleteProtectedHistoryExplainsCorrectionFlow(t *testing.T) {
	comp := &fakeComp{deleteErr: competitionpg.ErrDeleteProtected}
	h := handler(t, privateweb.RoleOwner, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/events/33333333-3333-3333-3333-333333333333/delete", url.Values{
		"csrf_token": {csrf}, "reason": {"made in error"},
	}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot be deleted") || !strings.Contains(rec.Body.String(), "void workflow") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestDeleteTeamBindsParentEvent(t *testing.T) {
	const eventID = "33333333-3333-3333-3333-333333333333"
	const teamID = "44444444-4444-4444-4444-444444444444"
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/events/"+eventID+"/teams/"+teamID+"/delete", url.Values{
		"csrf_token": {csrf}, "reason": {"duplicate team"},
	}))
	want := "team:" + eventID + ":" + teamID + ":" + userID + ":duplicate team"
	if rec.Code != http.StatusOK || len(comp.deleted) != 1 || comp.deleted[0] != want {
		t.Fatalf("status=%d delete calls=%v, want %q; body=%q", rec.Code, comp.deleted, want, rec.Body.String())
	}
}

func TestMatchPageShowsPlayersDistinctDefaultsAndConfirmations(t *testing.T) {
	const eventID = "33333333-3333-3333-3333-333333333333"
	const team1ID = "44444444-4444-4444-4444-444444444444"
	const team2ID = "55555555-5555-5555-5555-555555555555"
	const player1ID = "66666666-6666-6666-6666-666666666666"
	const player2ID = "77777777-7777-7777-7777-777777777777"
	verifiedAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	comp := &fakeComp{
		players: []competitionpg.PlayerRow{{ID: player1ID, DisplayName: "Alex"}, {ID: player2ID, DisplayName: "Bill"}},
		events: []competitionpg.EventRow{{
			ID: eventID, Name: "Cabot Cup", SeasonYear: 2026,
			Teams: []competitionpg.TeamRow{
				{ID: team1ID, Name: "Bears", Members: []competitionpg.TeamMemberRow{{PlayerID: player1ID, PlayerName: "Alex", IsCaptain: true}}},
				{ID: team2ID, Name: "Flamingos", Members: []competitionpg.TeamMemberRow{{PlayerID: player2ID, PlayerName: "Bill", IsCaptain: true}}},
			},
			Matches: []competitionpg.MatchRow{{
				ID: matchID, Number: 1, Format: "singles", State: "open",
				Side1TeamName: "Bears", Side1Players: "Alex",
				Side2TeamName: "Flamingos", Side2Players: "Bill",
			}, {
				ID: "88888888-8888-8888-8888-888888888888", Number: 2, Format: "singles", State: "verified",
				Side1TeamName: "Bears", Side1Players: "Alex", Side2TeamName: "Flamingos", Side2Players: "Bill",
				ResultOutcome: "side_1", ResultScore: "3 & 2", VerificationMethod: "admin_override", VerifiedAt: &verifiedAt,
			}},
		}},
	}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/matches", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`value="` + team1ID + `" selected`, `value="` + team2ID + `" selected`,
		`value="` + player1ID + `" data-team-id="` + team1ID + `" selected`,
		`value="` + player2ID + `" data-team-id="` + team2ID + `" selected`,
		"Alex", "Bill", "Captain assigned", `data-match-create-form`, `data-match-format`, `data-match-side="one"`,
		`data-team-id="` + team1ID + `"`, `data-team-id="` + team2ID + `"`,
		"Alex won", "3 &amp; 2", "Admin verified", "Jul 22, 2026 09:00 ADT",
		`data-confirm="Delete this unused match?`, `Delete empty event`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing %q; body=%q", want, body)
		}
	}
}

func TestRosterUpdateAndRemovalCarryActorAndReason(t *testing.T) {
	const eventID = "33333333-3333-3333-3333-333333333333"
	const teamID = "44444444-4444-4444-4444-444444444444"
	const playerID = "66666666-6666-6666-6666-666666666666"
	comp := &fakeComp{}
	h := handler(t, privateweb.RoleAdmin, comp)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/events/"+eventID+"/teams/"+teamID+"/roster", url.Values{
		"csrf_token": {csrf}, "player_id": {playerID}, "is_captain": {"true"},
	}))
	if rec.Code != http.StatusOK || len(comp.rosterReqs) != 1 || !comp.rosterReqs[0].IsCaptain || comp.rosterReqs[0].ActorUserID != userID {
		t.Fatalf("status=%d roster calls=%+v body=%q", rec.Code, comp.rosterReqs, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/events/"+eventID+"/teams/"+teamID+"/roster/"+playerID+"/remove", url.Values{
		"csrf_token": {csrf}, "reason": {"left this year's team"},
	}))
	want := "roster:" + eventID + ":" + teamID + ":" + playerID + ":" + userID + ":left this year's team"
	if rec.Code != http.StatusOK || len(comp.deleted) != 1 || comp.deleted[0] != want {
		t.Fatalf("status=%d calls=%v want=%q body=%q", rec.Code, comp.deleted, want, rec.Body.String())
	}
}

func TestRosterProtectedErrorIsClear(t *testing.T) {
	comp := &fakeComp{rosterError: competitionpg.ErrRosterProtected}
	h := handler(t, privateweb.RoleOwner, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/admin/events/33333333-3333-3333-3333-333333333333/teams/44444444-4444-4444-4444-444444444444/roster/66666666-6666-6666-6666-666666666666/remove", url.Values{
		"csrf_token": {csrf}, "reason": {"mistake"},
	}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "has match history") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMatchPageRequiresPlayersBeforeShowingCreateForm(t *testing.T) {
	comp := &fakeComp{events: []competitionpg.EventRow{{
		ID: "33333333-3333-3333-3333-333333333333", Name: "Cabot Cup", SeasonYear: 2026,
		Teams: []competitionpg.TeamRow{
			{ID: "44444444-4444-4444-4444-444444444444", Name: "Bears"},
			{ID: "55555555-5555-5555-5555-555555555555", Name: "Flamingos"},
		},
	}}}
	h := handler(t, privateweb.RoleAdmin, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/matches", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Staff at least two team rosters and assign each a captain before setting a match") {
		t.Fatalf("status=%d body=%q", rec.Code, body)
	}
	if strings.Contains(body, "data-match-create-form") {
		t.Fatal("match create form rendered before enough players exist")
	}
}
