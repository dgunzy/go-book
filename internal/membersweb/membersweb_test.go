package membersweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/identity"
	"github.com/dgunzy/go-book/internal/identitypg"
	"github.com/dgunzy/go-book/internal/privateweb"
)

const (
	testCSRF   = "csrf-secret"
	testUserID = "11111111-1111-1111-1111-111111111111"
	targetID   = "22222222-2222-2222-2222-222222222222"
)

type fakeSessions struct {
	session privateweb.Session
	err     error
}

func (f fakeSessions) CurrentSession(*http.Request) (privateweb.Session, error) {
	return f.session, f.err
}

type fakeMembers struct {
	inviteToken string
	inviteErr   error
	roleErr     error
	revokeErr   error
	limitErr    error
	inviteCalls []struct{ actor, role, email string }
	roleCalls   []struct{ actor, target, role, reason string }
	revokeCalls []struct{ actor, target, reason string }
	limitCalls  []struct {
		target string
		cents  *int64
	}
	creditCalls []int64
}

func (f *fakeMembers) ListMembers(context.Context) ([]identitypg.MemberRow, error) {
	return []identitypg.MemberRow{{UserID: targetID, DisplayName: "Jane", Email: "jane@example.test", Role: "member", Status: "active", GrantedAt: time.Now(), IdentityLinked: true}}, nil
}
func (f *fakeMembers) ListPendingInvitations(context.Context) ([]identitypg.InvitationRow, error) {
	return nil, nil
}
func (f *fakeMembers) IssueInvitation(_ context.Context, actor, role, email string, _ time.Duration) (string, error) {
	f.inviteCalls = append(f.inviteCalls, struct{ actor, role, email string }{actor, role, email})
	if f.inviteErr != nil {
		return "", f.inviteErr
	}
	return f.inviteToken, nil
}
func (f *fakeMembers) RevokeInvitation(_ context.Context, actor, id string) error { return f.revokeErr }
func (f *fakeMembers) ChangeMemberRole(_ context.Context, actor, target, role, reason string) error {
	f.roleCalls = append(f.roleCalls, struct{ actor, target, role, reason string }{actor, target, role, reason})
	return f.roleErr
}
func (f *fakeMembers) RevokeMember(_ context.Context, actor, target, reason string) error {
	f.revokeCalls = append(f.revokeCalls, struct{ actor, target, reason string }{actor, target, reason})
	return f.revokeErr
}
func (f *fakeMembers) SetAutoApproveLimit(_ context.Context, actor, target string, cents *int64) error {
	f.limitCalls = append(f.limitCalls, struct {
		target string
		cents  *int64
	}{target, cents})
	return f.limitErr
}
func (f *fakeMembers) SetCreditLimit(_ context.Context, actor, target string, cents int64) error {
	f.creditCalls = append(f.creditCalls, cents)
	return f.limitErr
}

type fakePlayers struct {
	links       []competitionpg.PlayerLink
	listErr     error
	listActors  []string
	linkCalls   []struct{ actor, playerID, userID string }
	unlinkCalls []struct{ actor, playerID, userID string }
	linkErr     error
}

func (f *fakePlayers) ListPlayerLinks(_ context.Context, actor string) ([]competitionpg.PlayerLink, error) {
	f.listActors = append(f.listActors, actor)
	return f.links, f.listErr
}
func (f *fakePlayers) LinkPlayerToUser(_ context.Context, actor, playerID, userID string) error {
	f.linkCalls = append(f.linkCalls, struct{ actor, playerID, userID string }{actor, playerID, userID})
	return f.linkErr
}
func (f *fakePlayers) UnlinkPlayer(_ context.Context, actor, playerID, userID string) error {
	f.unlinkCalls = append(f.unlinkCalls, struct{ actor, playerID, userID string }{actor, playerID, userID})
	return f.linkErr
}

func newHandler(t *testing.T, session privateweb.Session, members *fakeMembers) *Handler {
	t.Helper()
	return newHandlerWithPlayers(t, session, members, &fakePlayers{})
}

func newHandlerWithPlayers(t *testing.T, session privateweb.Session, members *fakeMembers, players *fakePlayers) *Handler {
	t.Helper()
	h, err := New(Dependencies{Sessions: fakeSessions{session: session}, Members: members, Players: players, PublicBaseURL: "https://cabotcup.ca"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h
}

func session(role privateweb.Role) privateweb.Session {
	return privateweb.Session{UserID: testUserID, Role: role, Active: true, CSRFToken: testCSRF}
}

func postForm(path string, values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestUnauthenticatedRedirects(t *testing.T) {
	h := newHandler(t, privateweb.Session{}, &fakeMembers{})
	h.deps.Sessions = fakeSessions{err: privateweb.ErrNoSession}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/members", nil))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("status=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMemberForbidden(t *testing.T) {
	h := newHandler(t, session(privateweb.RoleMember), &fakeMembers{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/members", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
}

func TestAdminSeesMembers(t *testing.T) {
	players := &fakePlayers{links: []competitionpg.PlayerLink{{
		PlayerID: "66666666-6666-6666-6666-666666666666", PlayerName: "Jane Golfer",
	}}}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/members", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "jane@example.test") || !strings.Contains(rec.Body.String(), "Jane Golfer") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(players.listActors) != 1 || players.listActors[0] != testUserID {
		t.Fatalf("list actors = %v", players.listActors)
	}
}

func TestAdminSeesExistingPlayerLink(t *testing.T) {
	playerID := "66666666-6666-6666-6666-666666666666"
	players := &fakePlayers{links: []competitionpg.PlayerLink{{
		PlayerID: playerID, PlayerName: "Jane Golfer", LinkedUserID: targetID,
	}}}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/members", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Jane Golfer") || !strings.Contains(rec.Body.String(), "value=\""+playerID+"\"") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestInviteWithoutCSRFForbidden(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleAdmin), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/invite", url.Values{"role": {"member"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(members.inviteCalls) != 0 {
		t.Fatal("IssueInvitation called without CSRF")
	}
}

func TestAdminInviteMemberShowsLink(t *testing.T) {
	members := &fakeMembers{inviteToken: "raw-token-abc"}
	h := newHandler(t, session(privateweb.RoleAdmin), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/invite", url.Values{"csrf_token": {testCSRF}, "role": {"member"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "https://cabotcup.ca/invite/raw-token-abc") {
		t.Fatal("invite link not shown")
	}
	if !strings.Contains(rec.Body.String(), "data-select-on-click") || strings.Contains(rec.Body.String(), "onclick=") {
		t.Fatal("invite-link selection is not wired through CSP-safe external JavaScript")
	}
	if len(members.inviteCalls) != 1 || members.inviteCalls[0].role != "member" || members.inviteCalls[0].actor != testUserID {
		t.Fatalf("invite calls = %+v", members.inviteCalls)
	}
}

func TestAdminCannotInviteAdmin(t *testing.T) {
	members := &fakeMembers{inviteToken: "tok"}
	h := newHandler(t, session(privateweb.RoleAdmin), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/invite", url.Values{"csrf_token": {testCSRF}, "role": {"admin"}}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	if len(members.inviteCalls) != 0 {
		t.Fatal("admin was allowed to issue an admin invite in the handler")
	}
}

func TestOwnerCanInviteAdmin(t *testing.T) {
	members := &fakeMembers{inviteToken: "tok"}
	h := newHandler(t, session(privateweb.RoleOwner), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/invite", url.Values{"csrf_token": {testCSRF}, "role": {"admin"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(members.inviteCalls) != 1 || members.inviteCalls[0].role != "admin" {
		t.Fatalf("invite calls = %+v", members.inviteCalls)
	}
}

func TestRoleChangeRequiresOwner(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleAdmin), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/role", url.Values{"csrf_token": {testCSRF}, "role": {"admin"}, "reason": {"promote"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(members.roleCalls) != 0 {
		t.Fatal("admin reached ChangeMemberRole")
	}
}

func TestOwnerChangesRole(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleOwner), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/role", url.Values{"csrf_token": {testCSRF}, "role": {"admin"}, "reason": {"promote to admin"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", rec.Code)
	}
	if len(members.roleCalls) != 1 || members.roleCalls[0].target != targetID || members.roleCalls[0].role != "admin" || members.roleCalls[0].actor != testUserID {
		t.Fatalf("role calls = %+v", members.roleCalls)
	}
}

func TestOwnerRevokeRequiresReason(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleOwner), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/revoke", url.Values{"csrf_token": {testCSRF}}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	if len(members.revokeCalls) != 0 {
		t.Fatal("RevokeMember called without a reason")
	}
}

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := New(Dependencies{Sessions: fakeSessions{}, Members: &fakeMembers{}})
	if err == nil {
		t.Fatal("New should require a public base URL")
	}
	_ = identity.ErrUnauthorized
}

func TestSetLimitParsesDollarsAndClears(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleAdmin), members)
	// Set a $250 limit.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/limit", url.Values{"csrf_token": {testCSRF}, "limit": {"250.00"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(members.limitCalls) != 1 || members.limitCalls[0].cents == nil || *members.limitCalls[0].cents != 25000 {
		t.Fatalf("limit calls = %+v, want one at 25000 cents", members.limitCalls)
	}
	// Blank clears the override (nil).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/limit", url.Values{"csrf_token": {testCSRF}, "limit": {""}}))
	if len(members.limitCalls) != 2 || members.limitCalls[1].cents != nil {
		t.Fatalf("second limit call = %+v, want nil (cleared)", members.limitCalls)
	}
}

func TestSetLimitRequiresAdmin(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleMember), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/limit", url.Values{"csrf_token": {testCSRF}, "limit": {"100"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(members.limitCalls) != 0 {
		t.Fatal("member reached SetAutoApproveLimit")
	}
}

func TestSetCreditLimit(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, session(privateweb.RoleAdmin), members)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/credit", url.Values{"csrf_token": {testCSRF}, "credit": {"1000.00"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(members.creditCalls) != 1 || members.creditCalls[0] != 100000 {
		t.Fatalf("credit calls = %v, want [100000]", members.creditCalls)
	}
}

func TestLinkPlayerHappyPath(t *testing.T) {
	players := &fakePlayers{}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	playerID := "66666666-6666-6666-6666-666666666666"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/link-player", url.Values{"csrf_token": {testCSRF}, "player_id": {playerID}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(players.linkCalls) != 1 || players.linkCalls[0].actor != testUserID || players.linkCalls[0].playerID != playerID || players.linkCalls[0].userID != targetID {
		t.Fatalf("link calls = %+v", players.linkCalls)
	}
}

func TestLinkPlayerRequiresCSRF(t *testing.T) {
	players := &fakePlayers{}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/link-player", url.Values{
		"player_id": {"66666666-6666-6666-6666-666666666666"},
	}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(players.linkCalls) != 0 {
		t.Fatal("LinkPlayerToUser called without CSRF")
	}
}

func TestLinkPlayerReturnsSafeConflictMessage(t *testing.T) {
	players := &fakePlayers{linkErr: fmt.Errorf("wrapped: %w", competitionpg.ErrMemberAlreadyLinked)}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/link-player", url.Values{
		"csrf_token": {testCSRF}, "player_id": {"66666666-6666-6666-6666-666666666666"},
	}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already linked to another historical player") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestLinkPlayerDoesNotExposeStoreError(t *testing.T) {
	players := &fakePlayers{linkErr: errors.New("postgresql secret diagnostic")}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/link-player", url.Values{
		"csrf_token": {testCSRF}, "player_id": {"66666666-6666-6666-6666-666666666666"},
	}))
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "postgresql secret diagnostic") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUnlinkPlayerHappyPath(t *testing.T) {
	players := &fakePlayers{}
	h := newHandlerWithPlayers(t, session(privateweb.RoleAdmin), &fakeMembers{}, players)
	playerID := "66666666-6666-6666-6666-666666666666"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/unlink-player", url.Values{
		"csrf_token": {testCSRF}, "player_id": {playerID},
	}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(players.unlinkCalls) != 1 || players.unlinkCalls[0].actor != testUserID || players.unlinkCalls[0].playerID != playerID || players.unlinkCalls[0].userID != targetID {
		t.Fatalf("unlink calls = %+v", players.unlinkCalls)
	}
}

func TestLinkPlayerRequiresAdmin(t *testing.T) {
	players := &fakePlayers{}
	h := newHandlerWithPlayers(t, session(privateweb.RoleMember), &fakeMembers{}, players)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/admin/members/"+targetID+"/link-player", url.Values{"csrf_token": {testCSRF}, "player_id": {"66666666-6666-6666-6666-666666666666"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if len(players.linkCalls) != 0 {
		t.Fatal("member reached LinkPlayerToUser")
	}
}
