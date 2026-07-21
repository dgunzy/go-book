package identitypg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5"
)

// invitationTokenContext domain-separates the invitation-token hash from any
// other SHA-256 use in the system.
const invitationTokenContext = "cabot-invitation-token-v1"

// hashInvitationToken returns the stored form of a raw invitation token. Only
// the hash is ever persisted; the raw token lives only in the invite link.
func hashInvitationToken(raw string) []byte {
	sum := sha256.Sum256([]byte(invitationTokenContext + ":" + raw))
	return sum[:]
}

// MemberRow is one member for the admin member list.
type MemberRow struct {
	UserID         string
	DisplayName    string
	Email          string
	Role           string
	Status         string
	GrantedAt      time.Time
	IdentityLinked bool
	// AutoApproveMaxCents is the member's per-player auto-approve override in
	// cents, or nil when they use the book-wide default.
	AutoApproveMaxCents *int64
	// CreditLimitCents is how far the member's cash balance may go negative
	// (how much they can bet on credit / owe the book).
	CreditLimitCents int64
}

// InvitationRow is one outstanding (unconsumed, unrevoked, unexpired) invite.
type InvitationRow struct {
	ID            string
	Role          string
	IntendedEmail string
	IssuedByName  string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

// CreateSessionForInvitedIdentity consumes inviteToken to admit a first-time
// member, then creates their session, all in one serializable transaction. If
// the identity already resolves to an active member the invite is left
// untouched and the existing membership is used, so a member cannot burn an
// invite by clicking their own link.
func (store Store) CreateSessionForInvitedIdentity(ctx context.Context, verified identity.VerifiedIdentity, draft identity.SessionDraft, inviteToken string) (identity.Session, identity.Principal, error) {
	return store.withPrincipalTransaction(ctx, func(tx pgx.Tx) (identity.Session, identity.Principal, error) {
		principal, err := resolveApprovedIdentity(ctx, tx, verified)
		if errors.Is(err, identity.ErrSignInNotAllowed) {
			principal, err = consumeInvitation(ctx, tx, verified, inviteToken)
		}
		if err != nil {
			return identity.Session{}, identity.Principal{}, err
		}
		session, err := insertSession(ctx, tx, principal.User.ID, draft, "")
		return session, principal, err
	})
}

func consumeInvitation(ctx context.Context, tx pgx.Tx, verified identity.VerifiedIdentity, rawToken string) (identity.Principal, error) {
	if strings.TrimSpace(rawToken) == "" {
		return identity.Principal{}, identity.ErrSignInNotAllowed
	}
	var invitationID, role string
	var intendedEmail *string
	var issuedBy string
	err := tx.QueryRow(ctx, `
		SELECT id::text, role, intended_email, issued_by::text
		FROM invitations
		WHERE token_hash = $1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > now()
		FOR UPDATE`, hashInvitationToken(rawToken)).Scan(&invitationID, &role, &intendedEmail, &issuedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Principal{}, identity.ErrSignInNotAllowed
	}
	if err != nil {
		return identity.Principal{}, fmt.Errorf("load invitation: %w", err)
	}
	if intendedEmail != nil && !strings.EqualFold(strings.TrimSpace(*intendedEmail), verified.Email) {
		// The invite was addressed to a specific email and this is not it.
		return identity.Principal{}, identity.ErrSignInNotAllowed
	}

	// Find or create the user for this verified email.
	var userID, status string
	err = tx.QueryRow(ctx, `SELECT id::text, status FROM users WHERE lower(email) = lower($1) FOR UPDATE`, verified.Email).Scan(&userID, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (display_name, email, status) VALUES ($1, $2, 'active') RETURNING id::text`,
			verified.DisplayName, strings.ToLower(verified.Email)).Scan(&userID); err != nil {
			return identity.Principal{}, fmt.Errorf("create invited user: %w", err)
		}
	case err != nil:
		return identity.Principal{}, fmt.Errorf("look up invited user: %w", err)
	case status != "active":
		return identity.Principal{}, identity.ErrSignInNotAllowed
	}

	// The user must not already hold an active membership (resolveApprovedIdentity
	// already ruled that out for the normal case, but a concurrent grant is
	// guarded here too by the unique active-role index).
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, role, granted_by) VALUES ($1::uuid, $2, $3::uuid)`,
		userID, role, issuedBy); err != nil {
		return identity.Principal{}, fmt.Errorf("grant invited membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE invitations SET consumed_at = now(), consumed_by = $2::uuid WHERE id = $1::uuid`,
		invitationID, userID); err != nil {
		return identity.Principal{}, fmt.Errorf("consume invitation: %w", err)
	}
	profile, err := json.Marshal(map[string]string{"display_name": verified.DisplayName})
	if err != nil {
		return identity.Principal{}, fmt.Errorf("encode identity profile: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_identities (user_id, provider, subject, email, email_verified, profile, last_authenticated_at)
		VALUES ($1::uuid, $2, $3, $4, true, $5::jsonb, now())
		ON CONFLICT (provider, subject) DO UPDATE
		SET last_authenticated_at = now(), email = EXCLUDED.email, email_verified = true
		WHERE auth_identities.user_id = EXCLUDED.user_id`,
		userID, verified.Provider, verified.Subject, verified.Email, string(profile)); err != nil {
		return identity.Principal{}, fmt.Errorf("link invited OIDC identity: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, after_data)
		VALUES ($1::uuid, 'membership.invitation_consumed', 'user', $2::uuid, 'accepted invite', jsonb_build_object('role', $3::text))`,
		issuedBy, userID, role); err != nil {
		return identity.Principal{}, fmt.Errorf("record invitation audit: %w", err)
	}

	principal, err := scanPrincipal(tx.QueryRow(ctx, principalByUserSQL, userID))
	if err != nil {
		return identity.Principal{}, fmt.Errorf("load invited principal: %w", err)
	}
	return principal, nil
}

const principalByUserSQL = principalColumns + `
FROM users u
JOIN memberships m ON m.user_id = u.id AND m.revoked_at IS NULL
WHERE u.id = $1::uuid AND u.status = 'active'`

// IssueInvitation creates a single-use invite for role and returns the raw
// token to embed in the link (only its hash is stored). actorUserID must be an
// admin or owner for a member invite, and an owner for an admin invite.
func (store Store) IssueInvitation(ctx context.Context, actorUserID, role, intendedEmail string, ttl time.Duration) (string, error) {
	if store.Pool == nil {
		return "", errors.New("identity PostgreSQL pool is required")
	}
	if role != "member" && role != "admin" {
		return "", fmt.Errorf("%w: invitations may grant member or admin only", identity.ErrUnauthorized)
	}
	if ttl <= 0 || ttl > 30*24*time.Hour {
		return "", errors.New("invitation ttl must be between 0 and 30 days")
	}
	actorRole, err := store.activeRole(ctx, actorUserID)
	if err != nil {
		return "", err
	}
	if role == "admin" && actorRole != "owner" {
		return "", fmt.Errorf("%w: only an owner may invite an admin", identity.ErrUnauthorized)
	}
	if actorRole != "admin" && actorRole != "owner" {
		return "", fmt.Errorf("%w: only an admin or owner may invite members", identity.ErrUnauthorized)
	}

	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return "", fmt.Errorf("generate invitation token: %w", err)
	}
	rawToken := base64.RawURLEncoding.EncodeToString(rawBytes)
	var normalizedEmail any
	if trimmed := strings.ToLower(strings.TrimSpace(intendedEmail)); trimmed != "" {
		normalizedEmail = trimmed
	}
	if _, err := store.Pool.Exec(ctx, `
		INSERT INTO invitations (token_hash, intended_email, role, issued_by, expires_at)
		VALUES ($1, $2, $3, $4::uuid, now() + $5::interval)`,
		hashInvitationToken(rawToken), normalizedEmail, role, actorUserID, fmt.Sprintf("%d seconds", int64(ttl.Seconds()))); err != nil {
		return "", fmt.Errorf("create invitation: %w", err)
	}
	return rawToken, nil
}

// RevokeInvitation cancels an outstanding invitation.
func (store Store) RevokeInvitation(ctx context.Context, actorUserID, invitationID string) error {
	if store.Pool == nil {
		return errors.New("identity PostgreSQL pool is required")
	}
	actorRole, err := store.activeRole(ctx, actorUserID)
	if err != nil {
		return err
	}
	if actorRole != "admin" && actorRole != "owner" {
		return fmt.Errorf("%w: only an admin or owner may revoke invitations", identity.ErrUnauthorized)
	}
	tag, err := store.Pool.Exec(ctx, `
		UPDATE invitations SET revoked_at = now(), revoked_by = $2::uuid
		WHERE id = $1::uuid AND consumed_at IS NULL AND revoked_at IS NULL`, invitationID, actorUserID)
	if err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("invitation not found or already consumed")
	}
	return nil
}

// ListMembers returns every user with an active membership, newest first.
func (store Store) ListMembers(ctx context.Context) ([]MemberRow, error) {
	if store.Pool == nil {
		return nil, errors.New("identity PostgreSQL pool is required")
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT u.id::text, u.display_name, coalesce(u.email, ''), m.role, u.status, m.granted_at,
		       EXISTS (SELECT 1 FROM auth_identities ai WHERE ai.user_id = u.id),
		       u.wager_auto_approve_max_cents, u.credit_limit_cents
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.revoked_at IS NULL
		ORDER BY m.granted_at DESC, u.display_name`)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	var result []MemberRow
	for rows.Next() {
		var row MemberRow
		if err := rows.Scan(&row.UserID, &row.DisplayName, &row.Email, &row.Role, &row.Status, &row.GrantedAt, &row.IdentityLinked, &row.AutoApproveMaxCents, &row.CreditLimitCents); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		row.GrantedAt = row.GrantedAt.UTC()
		result = append(result, row)
	}
	return result, rows.Err()
}

// ListPendingInvitations returns outstanding invites for the admin view.
func (store Store) ListPendingInvitations(ctx context.Context) ([]InvitationRow, error) {
	if store.Pool == nil {
		return nil, errors.New("identity PostgreSQL pool is required")
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT i.id::text, i.role, coalesce(i.intended_email, ''), coalesce(u.display_name, ''), i.expires_at, i.created_at
		FROM invitations i
		LEFT JOIN users u ON u.id = i.issued_by
		WHERE i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
		ORDER BY i.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()
	var result []InvitationRow
	for rows.Next() {
		var row InvitationRow
		if err := rows.Scan(&row.ID, &row.Role, &row.IntendedEmail, &row.IssuedByName, &row.ExpiresAt, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		row.ExpiresAt = row.ExpiresAt.UTC()
		row.CreatedAt = row.CreatedAt.UTC()
		result = append(result, row)
	}
	return result, rows.Err()
}

// ChangeMemberRole revokes a member's current membership and grants a new one
// with newRole. Only an owner may do this, and the last owner cannot be
// demoted. The change is audited.
func (store Store) ChangeMemberRole(ctx context.Context, actorUserID, targetUserID, newRole, reason string) error {
	if store.Pool == nil {
		return errors.New("identity PostgreSQL pool is required")
	}
	if newRole != "member" && newRole != "admin" && newRole != "owner" {
		return errors.New("role must be member, admin, or owner")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a reason is required for a role change", identity.ErrUnauthorized)
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin role change: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := requireOwnerTx(ctx, tx, actorUserID); err != nil {
		return err
	}
	var currentRole, membershipID string
	err = tx.QueryRow(ctx, `SELECT id::text, role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL FOR UPDATE`, targetUserID).Scan(&membershipID, &currentRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("target has no active membership")
	}
	if err != nil {
		return fmt.Errorf("load target membership: %w", err)
	}
	if currentRole == newRole {
		return tx.Commit(ctx)
	}
	if currentRole == "owner" {
		var ownerCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE role = 'owner' AND revoked_at IS NULL`).Scan(&ownerCount); err != nil {
			return fmt.Errorf("count owners: %w", err)
		}
		if ownerCount <= 1 {
			return errors.New("cannot demote the last owner")
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE memberships SET revoked_at = now(), revoked_by = $2::uuid, revocation_reason = $3
		WHERE id = $1::uuid`, membershipID, actorUserID, reason); err != nil {
		return fmt.Errorf("revoke prior membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, role, granted_by) VALUES ($1::uuid, $2, $3::uuid)`,
		targetUserID, newRole, actorUserID); err != nil {
		return fmt.Errorf("grant new membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'membership.role_changed', 'user', $2::uuid, $3, jsonb_build_object('role', $4::text), jsonb_build_object('role', $5::text))`,
		actorUserID, targetUserID, reason, currentRole, newRole); err != nil {
		return fmt.Errorf("record role change audit: %w", err)
	}
	return tx.Commit(ctx)
}

// RevokeMember revokes a member's membership entirely (removing their access).
// Only an owner may do this, and the last owner cannot be revoked.
func (store Store) RevokeMember(ctx context.Context, actorUserID, targetUserID, reason string) error {
	if store.Pool == nil {
		return errors.New("identity PostgreSQL pool is required")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a reason is required to revoke a member", identity.ErrUnauthorized)
	}
	if actorUserID == targetUserID {
		return errors.New("you cannot revoke your own membership")
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin revoke: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := requireOwnerTx(ctx, tx, actorUserID); err != nil {
		return err
	}
	var currentRole, membershipID string
	err = tx.QueryRow(ctx, `SELECT id::text, role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL FOR UPDATE`, targetUserID).Scan(&membershipID, &currentRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("target has no active membership")
	}
	if err != nil {
		return fmt.Errorf("load target membership: %w", err)
	}
	if currentRole == "owner" {
		var ownerCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE role = 'owner' AND revoked_at IS NULL`).Scan(&ownerCount); err != nil {
			return fmt.Errorf("count owners: %w", err)
		}
		if ownerCount <= 1 {
			return errors.New("cannot revoke the last owner")
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE memberships SET revoked_at = now(), revoked_by = $2::uuid, revocation_reason = $3
		WHERE id = $1::uuid`, membershipID, actorUserID, reason); err != nil {
		return fmt.Errorf("revoke membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data)
		VALUES ($1::uuid, 'membership.revoked', 'user', $2::uuid, $3, jsonb_build_object('role', $4::text))`,
		actorUserID, targetUserID, reason, currentRole); err != nil {
		return fmt.Errorf("record revoke audit: %w", err)
	}
	return tx.Commit(ctx)
}

// SetAutoApproveLimit sets (or clears, when cents is nil) a member's per-player
// auto-approve override. Admins and owners may adjust betting limits. The
// change is audited.
func (store Store) SetAutoApproveLimit(ctx context.Context, actorUserID, targetUserID string, cents *int64) error {
	if store.Pool == nil {
		return errors.New("identity PostgreSQL pool is required")
	}
	if cents != nil && *cents < 0 {
		return errors.New("auto-approve limit must not be negative")
	}
	actorRole, err := store.activeRole(ctx, actorUserID)
	if err != nil {
		return err
	}
	if actorRole != "admin" && actorRole != "owner" {
		return fmt.Errorf("%w: only an admin or owner may set betting limits", identity.ErrUnauthorized)
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin set limit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	tag, err := tx.Exec(ctx, `UPDATE users SET wager_auto_approve_max_cents = $2, updated_at = now() WHERE id = $1::uuid`, targetUserID, cents)
	if err != nil {
		return fmt.Errorf("set auto-approve limit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("member not found")
	}
	after := "null"
	if cents != nil {
		after = fmt.Sprintf("%d", *cents)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, after_data)
		VALUES ($1::uuid, 'membership.auto_approve_limit_set', 'user', $2::uuid, 'set betting limit', jsonb_build_object('auto_approve_max_cents', $3::text))`,
		actorUserID, targetUserID, after); err != nil {
		return fmt.Errorf("record limit audit: %w", err)
	}
	return tx.Commit(ctx)
}

// SetCreditLimit sets a member's credit limit (how far their balance may go
// negative). Admins and owners may adjust it; the change is audited.
func (store Store) SetCreditLimit(ctx context.Context, actorUserID, targetUserID string, cents int64) error {
	if store.Pool == nil {
		return errors.New("identity PostgreSQL pool is required")
	}
	if cents < 0 {
		return errors.New("credit limit must not be negative")
	}
	actorRole, err := store.activeRole(ctx, actorUserID)
	if err != nil {
		return err
	}
	if actorRole != "admin" && actorRole != "owner" {
		return fmt.Errorf("%w: only an admin or owner may set credit limits", identity.ErrUnauthorized)
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin set credit limit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	tag, err := tx.Exec(ctx, `UPDATE users SET credit_limit_cents = $2, updated_at = now() WHERE id = $1::uuid`, targetUserID, cents)
	if err != nil {
		return fmt.Errorf("set credit limit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("member not found")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, after_data)
		VALUES ($1::uuid, 'membership.credit_limit_set', 'user', $2::uuid, 'set credit limit', jsonb_build_object('credit_limit_cents', $3::text))`,
		actorUserID, targetUserID, fmt.Sprintf("%d", cents)); err != nil {
		return fmt.Errorf("record credit limit audit: %w", err)
	}
	return tx.Commit(ctx)
}

func (store Store) activeRole(ctx context.Context, userID string) (string, error) {
	var role string
	err := store.Pool.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: no active membership", identity.ErrUnauthorized)
	}
	if err != nil {
		return "", fmt.Errorf("load actor role: %w", err)
	}
	return role, nil
}

func requireOwnerTx(ctx context.Context, tx pgx.Tx, actorUserID string) error {
	var role string
	err := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL`, actorUserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && role != "owner") {
		return fmt.Errorf("%w: this action requires an owner", identity.ErrUnauthorized)
	}
	if err != nil {
		return fmt.Errorf("verify owner: %w", err)
	}
	return nil
}
