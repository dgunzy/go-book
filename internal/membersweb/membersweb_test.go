package membersweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	inviteCalls []struct{ actor, role, email string }
	roleCalls   []struct{ actor, target, role, reason string }
	revokeCalls []struct{ actor, target, reason string }
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

func newHandler(t *testing.T, session privateweb.Session, members *fakeMembers) *Handler {
	t.Helper()
	h, err := New(Dependencies{Sessions: fakeSessions{session: session}, Members: members, PublicBaseURL: "https://cabotcup.ca"})
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
	h := newHandler(t, session(privateweb.RoleAdmin), &fakeMembers{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/members", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "jane@example.test") {
		t.Fatalf("status=%d body has jane=%v", rec.Code, strings.Contains(rec.Body.String(), "jane@example.test"))
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
