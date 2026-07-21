package identitypg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDraft(t *testing.T) identity.SessionDraft {
	t.Helper()
	token, err := identity.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := identity.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	return identity.SessionDraft{
		TokenHash:      identity.HashSessionToken(token),
		CSRFSecretHash: identity.HashCSRFSecret(csrf),
		CreatedAt:      now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func membershipTestStore(t *testing.T) (Store, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("IDENTITYPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("IDENTITYPG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return Store{Pool: pool}, pool, ctx
}

// makeMember inserts an active user with the given role and returns its ID.
func makeMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label, role string) string {
	t.Helper()
	suffix := time.Now().UTC().UnixNano()
	email := fmt.Sprintf("%s-%d@example.test", strings.ToLower(strings.ReplaceAll(label, " ", "-")), suffix)
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (display_name, email, status) VALUES ($1, $2, 'active') RETURNING id::text`,
		label, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, $2)`, userID, role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM audit_entries WHERE target_id = $1::uuid OR actor_user_id = $1::uuid`, userID)
		_, _ = pool.Exec(c, `DELETE FROM invitations WHERE issued_by = $1::uuid OR consumed_by = $1::uuid`, userID)
		_, _ = pool.Exec(c, `DELETE FROM auth_identities WHERE user_id = $1::uuid`, userID)
		_, _ = pool.Exec(c, `DELETE FROM memberships WHERE user_id = $1::uuid`, userID)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, userID)
	})
	return userID
}

func TestInviteIssueAndConsumeCreatesMember(t *testing.T) {
	store, pool, ctx := membershipTestStore(t)
	owner := makeMember(t, ctx, pool, "Invite Owner", "owner")

	token, err := store.IssueInvitation(ctx, owner, "member", "", time.Hour)
	if err != nil {
		t.Fatalf("IssueInvitation() error = %v", err)
	}

	suffix := time.Now().UTC().UnixNano()
	inviteeEmail := fmt.Sprintf("invitee-%d@example.test", suffix)
	verified := identity.VerifiedIdentity{
		Provider: "google", Subject: fmt.Sprintf("sub-%d", suffix), Email: inviteeEmail,
		EmailVerified: true, DisplayName: "New Invitee",
	}
	draft := testDraft(t)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, inviteeEmail)
		_, _ = pool.Exec(c, `DELETE FROM audit_entries WHERE target_id IN (SELECT id FROM users WHERE email = $1)`, inviteeEmail)
		_, _ = pool.Exec(c, `DELETE FROM auth_identities WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, inviteeEmail)
		_, _ = pool.Exec(c, `DELETE FROM memberships WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, inviteeEmail)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE email = $1`, inviteeEmail)
	})

	session, principal, err := store.CreateSessionForInvitedIdentity(ctx, verified, draft, token)
	if err != nil {
		t.Fatalf("CreateSessionForInvitedIdentity() error = %v", err)
	}
	if principal.Membership.Role != identity.Role("member") {
		t.Fatalf("invited role = %q, want member", principal.Membership.Role)
	}
	if session.UserID == "" {
		t.Fatal("no session created for invited member")
	}

	// The invitation is now consumed and cannot be reused.
	verified2 := verified
	verified2.Subject = fmt.Sprintf("sub2-%d", suffix)
	verified2.Email = fmt.Sprintf("other-%d@example.test", suffix)
	if _, _, err := store.CreateSessionForInvitedIdentity(ctx, verified2, testDraft(t), token); err == nil {
		t.Fatal("a consumed invitation was accepted a second time")
	}

	// The new member now appears in the member list.
	members, err := store.ListMembers(ctx)
	if err != nil {
		t.Fatalf("ListMembers() error = %v", err)
	}
	found := false
	for _, m := range members {
		if m.Email == inviteeEmail && m.Role == "member" {
			found = true
		}
	}
	if !found {
		t.Fatal("invited member missing from ListMembers")
	}
}

func TestInviteAuthorizationRules(t *testing.T) {
	store, pool, ctx := membershipTestStore(t)
	admin := makeMember(t, ctx, pool, "Invite Admin", "admin")
	member := makeMember(t, ctx, pool, "Plain Member", "member")

	// An admin may invite a member but not an admin.
	if _, err := store.IssueInvitation(ctx, admin, "member", "", time.Hour); err != nil {
		t.Fatalf("admin inviting member: %v", err)
	}
	if _, err := store.IssueInvitation(ctx, admin, "admin", "", time.Hour); err == nil {
		t.Fatal("admin was allowed to invite an admin")
	}
	// A plain member may not invite at all.
	if _, err := store.IssueInvitation(ctx, member, "member", "", time.Hour); err == nil {
		t.Fatal("a member was allowed to invite")
	}
}

func TestChangeRoleAndRevokeRequireOwner(t *testing.T) {
	store, pool, ctx := membershipTestStore(t)
	owner := makeMember(t, ctx, pool, "Role Owner", "owner")
	admin := makeMember(t, ctx, pool, "Role Admin", "admin")
	target := makeMember(t, ctx, pool, "Role Target", "member")

	// An admin cannot promote anyone.
	if err := store.ChangeMemberRole(ctx, admin, target, "admin", "promote"); err == nil {
		t.Fatal("admin was allowed to change a role")
	}
	// The owner can promote the member to admin.
	if err := store.ChangeMemberRole(ctx, owner, target, "admin", "promote to admin"); err != nil {
		t.Fatalf("owner promote: %v", err)
	}
	role := activeRoleOf(t, ctx, pool, target)
	if role != "admin" {
		t.Fatalf("target role after promote = %q, want admin", role)
	}
	// The owner can revoke; an admin cannot.
	if err := store.RevokeMember(ctx, admin, target, "no"); err == nil {
		t.Fatal("admin was allowed to revoke a member")
	}
	if err := store.RevokeMember(ctx, owner, target, "left the group"); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if activeRoleOf(t, ctx, pool, target) != "" {
		t.Fatal("target still has an active membership after revoke")
	}
}

func TestCannotDemoteLastOwner(t *testing.T) {
	store, pool, ctx := membershipTestStore(t)
	// Ensure exactly-one-owner reasoning is scoped: this test's owner may not
	// be the only owner in a shared DB, so we assert behavior only when it is.
	owner := makeMember(t, ctx, pool, "Solo Owner", "owner")
	var ownerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE role='owner' AND revoked_at IS NULL`).Scan(&ownerCount); err != nil {
		t.Fatal(err)
	}
	if ownerCount != 1 {
		t.Skipf("shared DB has %d owners; last-owner guard not exercised", ownerCount)
	}
	if err := store.ChangeMemberRole(ctx, owner, owner, "admin", "self-demote"); err == nil {
		t.Fatal("the last owner was demoted")
	}
}

func activeRoleOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var role string
	err := pool.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID).Scan(&role)
	if err != nil {
		return ""
	}
	return role
}
