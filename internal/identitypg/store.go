// Package identitypg implements identity session and approval storage in PostgreSQL.
package identitypg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

func (store Store) CreateSessionForIdentity(ctx context.Context, verified identity.VerifiedIdentity, draft identity.SessionDraft) (identity.Session, identity.Principal, error) {
	return store.withPrincipalTransaction(ctx, func(tx pgx.Tx) (identity.Session, identity.Principal, error) {
		principal, err := resolveApprovedIdentity(ctx, tx, verified)
		if err != nil {
			return identity.Session{}, identity.Principal{}, err
		}
		session, err := insertSession(ctx, tx, principal.User.ID, draft, "")
		return session, principal, err
	})
}

func (store Store) UseSession(ctx context.Context, tokenHash identity.Digest, now time.Time) (identity.Session, identity.Principal, error) {
	if store.Pool == nil {
		return identity.Session{}, identity.Principal{}, errors.New("identity PostgreSQL pool is required")
	}
	row := store.Pool.QueryRow(ctx, sessionPrincipalSQL+`
WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > $2
  AND u.status = 'active' AND m.revoked_at IS NULL`, tokenHash.Bytes(), now)
	session, principal, err := scanSessionPrincipal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Session{}, identity.Principal{}, identity.ErrUnauthenticated
	}
	if err != nil {
		return identity.Session{}, identity.Principal{}, fmt.Errorf("find active session: %w", err)
	}
	if now.Sub(session.LastSeenAt) >= 5*time.Minute {
		if _, err := store.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1::uuid AND last_seen_at < $2`, session.ID, now); err != nil {
			return identity.Session{}, identity.Principal{}, fmt.Errorf("touch session: %w", err)
		}
		session.LastSeenAt = now
	}
	return session, principal, nil
}

func (store Store) RotateSession(ctx context.Context, currentHash identity.Digest, draft identity.SessionDraft, now time.Time, reason string) (identity.Session, identity.Principal, error) {
	return store.withPrincipalTransaction(ctx, func(tx pgx.Tx) (identity.Session, identity.Principal, error) {
		row := tx.QueryRow(ctx, sessionPrincipalSQL+`
WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > $2
  AND u.status = 'active' AND m.revoked_at IS NULL
FOR UPDATE OF s, u, m`, currentHash.Bytes(), now)
		current, principal, err := scanSessionPrincipal(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.Session{}, identity.Principal{}, identity.ErrUnauthenticated
		}
		if err != nil {
			return identity.Session{}, identity.Principal{}, fmt.Errorf("lock session for rotation: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2, revoke_reason = $3 WHERE id = $1::uuid`, current.ID, now, reason); err != nil {
			return identity.Session{}, identity.Principal{}, fmt.Errorf("revoke rotated session: %w", err)
		}
		session, err := insertSession(ctx, tx, principal.User.ID, draft, current.ID)
		return session, principal, err
	})
}

func (store Store) RevokeSession(ctx context.Context, tokenHash identity.Digest, now time.Time, reason string) error {
	if store.Pool == nil {
		return errors.New("identity PostgreSQL pool is required")
	}
	tag, err := store.Pool.Exec(ctx, `
UPDATE sessions SET revoked_at = $2, revoke_reason = $3
WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash.Bytes(), now, reason)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrUnauthenticated
	}
	return nil
}

func (store Store) withPrincipalTransaction(ctx context.Context, operation func(pgx.Tx) (identity.Session, identity.Principal, error)) (identity.Session, identity.Principal, error) {
	if store.Pool == nil {
		return identity.Session{}, identity.Principal{}, errors.New("identity PostgreSQL pool is required")
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identity.Session{}, identity.Principal{}, fmt.Errorf("begin identity transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	session, principal, err := operation(tx)
	if err != nil {
		return identity.Session{}, identity.Principal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.Session{}, identity.Principal{}, fmt.Errorf("commit identity transaction: %w", err)
	}
	return session, principal, nil
}

func resolveApprovedIdentity(ctx context.Context, tx pgx.Tx, verified identity.VerifiedIdentity) (identity.Principal, error) {
	principal, err := scanPrincipal(tx.QueryRow(ctx, principalByIdentitySQL, verified.Provider, verified.Subject))
	if err == nil {
		if _, err := tx.Exec(ctx, `
UPDATE auth_identities SET last_authenticated_at = now(), email = $3, email_verified = true
WHERE provider = $1 AND subject = $2`, verified.Provider, verified.Subject, verified.Email); err != nil {
			return identity.Principal{}, fmt.Errorf("record authentication: %w", err)
		}
		return principal, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return identity.Principal{}, fmt.Errorf("resolve OIDC identity: %w", err)
	}

	principal, err = scanPrincipal(tx.QueryRow(ctx, principalByApprovedEmailSQL, verified.Email))
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Principal{}, identity.ErrSignInNotAllowed
	}
	if err != nil {
		return identity.Principal{}, fmt.Errorf("resolve approved email: %w", err)
	}
	profile, err := json.Marshal(map[string]string{"display_name": verified.DisplayName})
	if err != nil {
		return identity.Principal{}, fmt.Errorf("encode identity profile: %w", err)
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO auth_identities (user_id, provider, subject, email, email_verified, profile, last_authenticated_at)
VALUES ($1::uuid, $2, $3, $4, true, $5::jsonb, now())
ON CONFLICT (provider, subject) DO UPDATE
SET last_authenticated_at = now(), email = EXCLUDED.email, email_verified = true
WHERE auth_identities.user_id = EXCLUDED.user_id`,
		principal.User.ID, verified.Provider, verified.Subject, verified.Email, string(profile))
	if err != nil {
		return identity.Principal{}, fmt.Errorf("link approved OIDC identity: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return identity.Principal{}, identity.ErrSignInNotAllowed
	}
	return principal, nil
}

const principalColumns = `
SELECT u.id::text, u.display_name, coalesce(u.email, ''), u.status,
       m.id::text, m.user_id::text, m.role, m.granted_at,
       m.revoked_at`

const principalByIdentitySQL = principalColumns + `
FROM auth_identities ai
JOIN users u ON u.id = ai.user_id
JOIN memberships m ON m.user_id = u.id AND m.revoked_at IS NULL
WHERE ai.provider = $1 AND ai.subject = $2 AND u.status = 'active'
FOR UPDATE OF ai, u, m`

const principalByApprovedEmailSQL = principalColumns + `
FROM users u
JOIN memberships m ON m.user_id = u.id AND m.revoked_at IS NULL
WHERE lower(u.email) = lower($1) AND u.status = 'active'
FOR UPDATE OF u, m`

const sessionPrincipalSQL = `
SELECT s.id::text, s.user_id::text, s.token_hash, s.csrf_secret_hash,
       s.created_at, s.last_seen_at, s.expires_at,
       s.revoked_at, coalesce(s.revoke_reason, ''),
       coalesce(s.rotated_from::text, ''),
       u.id::text, u.display_name, coalesce(u.email, ''), u.status,
       m.id::text, m.user_id::text, m.role, m.granted_at,
       m.revoked_at
FROM sessions s
JOIN users u ON u.id = s.user_id
JOIN memberships m ON m.user_id = u.id `

type rowScanner interface{ Scan(...any) error }

func scanPrincipal(row rowScanner) (identity.Principal, error) {
	var principal identity.Principal
	var status, role string
	var revoked *time.Time
	err := row.Scan(
		&principal.User.ID, &principal.User.DisplayName, &principal.User.Email, &status,
		&principal.Membership.ID, &principal.Membership.UserID, &role,
		&principal.Membership.GrantedAt, &revoked,
	)
	if err != nil {
		return identity.Principal{}, err
	}
	principal.User.Status = identity.UserStatus(status)
	principal.Membership.Role = identity.Role(role)
	if revoked != nil {
		principal.Membership.RevokedAt = *revoked
	}
	return principal, nil
}

func scanSessionPrincipal(row rowScanner) (identity.Session, identity.Principal, error) {
	var session identity.Session
	var principal identity.Principal
	var tokenHash, csrfHash []byte
	var status, role string
	var sessionRevoked, membershipRevoked *time.Time
	err := row.Scan(
		&session.ID, &session.UserID, &tokenHash, &csrfHash,
		&session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt,
		&sessionRevoked, &session.RevokeReason, &session.RotatedFrom,
		&principal.User.ID, &principal.User.DisplayName, &principal.User.Email, &status,
		&principal.Membership.ID, &principal.Membership.UserID, &role,
		&principal.Membership.GrantedAt, &membershipRevoked,
	)
	if err != nil {
		return identity.Session{}, identity.Principal{}, err
	}
	var digestErr error
	session.TokenHash, digestErr = identity.DigestFromBytes(tokenHash)
	if digestErr != nil {
		return identity.Session{}, identity.Principal{}, digestErr
	}
	session.CSRFSecretHash, digestErr = identity.DigestFromBytes(csrfHash)
	if digestErr != nil {
		return identity.Session{}, identity.Principal{}, digestErr
	}
	if sessionRevoked != nil {
		session.RevokedAt = *sessionRevoked
	}
	principal.User.Status = identity.UserStatus(status)
	principal.Membership.Role = identity.Role(role)
	if membershipRevoked != nil {
		principal.Membership.RevokedAt = *membershipRevoked
	}
	return session, principal, nil
}

func insertSession(ctx context.Context, tx pgx.Tx, userID identity.ID, draft identity.SessionDraft, rotatedFrom identity.ID) (identity.Session, error) {
	row := tx.QueryRow(ctx, `
INSERT INTO sessions (user_id, token_hash, csrf_secret_hash, created_at, last_seen_at, expires_at, rotated_from)
VALUES ($1::uuid, $2, $3, $4, $5, $6, nullif($7, '')::uuid)
RETURNING id::text, user_id::text, token_hash, csrf_secret_hash, created_at, last_seen_at,
          expires_at, revoked_at, coalesce(revoke_reason, ''),
          coalesce(rotated_from::text, '')`,
		userID, draft.TokenHash.Bytes(), draft.CSRFSecretHash.Bytes(), draft.CreatedAt,
		draft.LastSeenAt, draft.ExpiresAt, rotatedFrom)
	var session identity.Session
	var tokenHash, csrfHash []byte
	var revoked *time.Time
	if err := row.Scan(&session.ID, &session.UserID, &tokenHash, &csrfHash, &session.CreatedAt,
		&session.LastSeenAt, &session.ExpiresAt, &revoked, &session.RevokeReason, &session.RotatedFrom); err != nil {
		return identity.Session{}, fmt.Errorf("insert session: %w", err)
	}
	var err error
	session.TokenHash, err = identity.DigestFromBytes(tokenHash)
	if err != nil {
		return identity.Session{}, err
	}
	session.CSRFSecretHash, err = identity.DigestFromBytes(csrfHash)
	if err != nil {
		return identity.Session{}, err
	}
	if revoked != nil {
		session.RevokedAt = *revoked
	}
	return session, nil
}
