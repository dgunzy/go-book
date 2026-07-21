package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	minimumSessionLifetime = 5 * time.Minute
	maximumSessionLifetime = 30 * 24 * time.Hour
)

type Session struct {
	ID             ID
	UserID         ID
	TokenHash      Digest
	CSRFSecretHash Digest
	CreatedAt      time.Time
	LastSeenAt     time.Time
	ExpiresAt      time.Time
	RevokedAt      time.Time
	RevokeReason   string
	RotatedFrom    ID
}

func (s Session) Validate() error {
	if !s.ID.Valid() || !s.UserID.Valid() || s.TokenHash.IsZero() || s.CSRFSecretHash.IsZero() || s.TokenHash.Equal(s.CSRFSecretHash) {
		return fmt.Errorf("%w: valid IDs and distinct secret hashes are required", ErrInvalidSession)
	}
	if s.CreatedAt.IsZero() || s.LastSeenAt.Before(s.CreatedAt) || s.LastSeenAt.After(s.ExpiresAt) || !s.ExpiresAt.After(s.CreatedAt) {
		return fmt.Errorf("%w: timestamps are inconsistent", ErrInvalidSession)
	}
	if (s.RevokedAt.IsZero()) != (strings.TrimSpace(s.RevokeReason) == "") {
		return fmt.Errorf("%w: revocation time and reason must be set together", ErrInvalidSession)
	}
	if !s.RevokedAt.IsZero() && s.RevokedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: revocation precedes creation", ErrInvalidSession)
	}
	if s.RotatedFrom != "" && !s.RotatedFrom.Valid() {
		return fmt.Errorf("%w: rotated-from ID must be a UUID", ErrInvalidSession)
	}
	return nil
}

func (s Session) ActiveAt(now time.Time) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: current time is required", ErrInvalidSession)
	}
	if !s.RevokedAt.IsZero() {
		return ErrSessionRevoked
	}
	if !now.Before(s.ExpiresAt) {
		return ErrSessionExpired
	}
	return nil
}

type SessionDraft struct {
	TokenHash      Digest
	CSRFSecretHash Digest
	CreatedAt      time.Time
	LastSeenAt     time.Time
	ExpiresAt      time.Time
}

func (d SessionDraft) validate() error {
	if d.TokenHash.IsZero() || d.CSRFSecretHash.IsZero() || d.TokenHash.Equal(d.CSRFSecretHash) {
		return fmt.Errorf("%w: distinct token and CSRF hashes are required", ErrInvalidSession)
	}
	if d.CreatedAt.IsZero() || !d.LastSeenAt.Equal(d.CreatedAt) || !d.ExpiresAt.After(d.CreatedAt) {
		return fmt.Errorf("%w: draft timestamps are inconsistent", ErrInvalidSession)
	}
	return nil
}

// Store operations that change sessions must be atomic. CreateSessionForIdentity
// resolves only (provider, subject), verifies an active user and membership, records
// last_authenticated_at, and inserts the session in one transaction. UseSession
// rejects expired/revoked sessions and may atomically advance last_seen_at. Rotation
// revokes the current session and inserts its replacement in one transaction.
type Store interface {
	CreateSessionForIdentity(context.Context, VerifiedIdentity, SessionDraft) (Session, Principal, error)
	// CreateSessionForInvitedIdentity consumes a valid invitation (identified
	// by its raw token) to create the account and membership for a first-time
	// sign-in, then creates the session. The store hashes the token itself.
	CreateSessionForInvitedIdentity(context.Context, VerifiedIdentity, SessionDraft, string) (Session, Principal, error)
	UseSession(context.Context, Digest, time.Time) (Session, Principal, error)
	RotateSession(context.Context, Digest, SessionDraft, time.Time, string) (Session, Principal, error)
	RevokeSession(context.Context, Digest, time.Time, string) error
}

type IssuedSession struct {
	Session    Session
	Principal  Principal
	Token      Secret
	CSRFSecret Secret
}

type AuthenticatedSession struct {
	Session   Session
	Principal Principal
}

type Service struct {
	store    Store
	lifetime time.Duration
	now      func() time.Time
	random   io.Reader
}

func NewService(store Store, lifetime time.Duration) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: session store is required", ErrInvalidSession)
	}
	if lifetime < minimumSessionLifetime || lifetime > maximumSessionLifetime {
		return nil, fmt.Errorf("%w: lifetime must be between %s and %s", ErrInvalidSession, minimumSessionLifetime, maximumSessionLifetime)
	}
	return &Service{store: store, lifetime: lifetime, now: time.Now, random: rand.Reader}, nil
}

func (s *Service) CompleteSignIn(ctx context.Context, identity VerifiedIdentity) (IssuedSession, error) {
	if err := identity.Validate(); err != nil {
		return IssuedSession{}, err
	}
	token, csrf, draft, err := s.newDraft()
	if err != nil {
		return IssuedSession{}, err
	}
	session, principal, err := s.store.CreateSessionForIdentity(ctx, identity, draft)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := validateStoredSession(session, principal, draft); err != nil {
		return IssuedSession{}, err
	}
	if session.RotatedFrom != "" {
		return IssuedSession{}, ErrRepositoryContract
	}
	return IssuedSession{Session: session, Principal: principal, Token: token, CSRFSecret: csrf}, nil
}

// CompleteSignInWithInvitation completes a first-time sign-in by consuming an
// invitation token, creating the invited member and their session. It is used
// when a user arrives through an invite link rather than a pre-approved email.
func (s *Service) CompleteSignInWithInvitation(ctx context.Context, identity VerifiedIdentity, inviteToken string) (IssuedSession, error) {
	if err := identity.Validate(); err != nil {
		return IssuedSession{}, err
	}
	token, csrf, draft, err := s.newDraft()
	if err != nil {
		return IssuedSession{}, err
	}
	session, principal, err := s.store.CreateSessionForInvitedIdentity(ctx, identity, draft, inviteToken)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := validateStoredSession(session, principal, draft); err != nil {
		return IssuedSession{}, err
	}
	if session.RotatedFrom != "" {
		return IssuedSession{}, ErrRepositoryContract
	}
	return IssuedSession{Session: session, Principal: principal, Token: token, CSRFSecret: csrf}, nil
}

func (s *Service) Resume(ctx context.Context, rawToken string) (AuthenticatedSession, error) {
	token, err := ParseSecret(rawToken)
	if err != nil {
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	session, principal, err := s.store.UseSession(ctx, HashSessionToken(token), now)
	if err != nil {
		return AuthenticatedSession{}, err
	}
	if !session.TokenHash.Equal(HashSessionToken(token)) {
		return AuthenticatedSession{}, ErrRepositoryContract
	}
	if err := validateActiveSession(session, principal, now); err != nil {
		return AuthenticatedSession{}, err
	}
	return AuthenticatedSession{Session: session, Principal: principal}, nil
}

func (s *Service) Rotate(ctx context.Context, rawToken, reason string) (IssuedSession, error) {
	current, err := ParseSecret(rawToken)
	if err != nil {
		return IssuedSession{}, ErrUnauthenticated
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return IssuedSession{}, fmt.Errorf("%w: rotation reason is required", ErrInvalidSession)
	}
	token, csrf, draft, err := s.newDraft()
	if err != nil {
		return IssuedSession{}, err
	}
	now := draft.CreatedAt
	session, principal, err := s.store.RotateSession(ctx, HashSessionToken(current), draft, now, reason)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := validateStoredSession(session, principal, draft); err != nil {
		return IssuedSession{}, err
	}
	if !session.RotatedFrom.Valid() {
		return IssuedSession{}, ErrRepositoryContract
	}
	return IssuedSession{Session: session, Principal: principal, Token: token, CSRFSecret: csrf}, nil
}

func (s *Service) Revoke(ctx context.Context, rawToken, reason string) error {
	token, err := ParseSecret(rawToken)
	if err != nil {
		return ErrUnauthenticated
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: revocation reason is required", ErrInvalidSession)
	}
	return s.store.RevokeSession(ctx, HashSessionToken(token), s.now().UTC().Truncate(time.Microsecond), reason)
}

func ValidateCSRF(session Session, presented string) error {
	if !VerifyCSRFSecret(session.CSRFSecretHash, presented) {
		return ErrInvalidCSRF
	}
	return nil
}

func (s *Service) newDraft() (Secret, Secret, SessionDraft, error) {
	token, err := generateSecret(s.random)
	if err != nil {
		return Secret{}, Secret{}, SessionDraft{}, err
	}
	csrf, err := generateSecret(s.random)
	if err != nil {
		return Secret{}, Secret{}, SessionDraft{}, err
	}
	if token.equal(csrf) {
		return Secret{}, Secret{}, SessionDraft{}, ErrSecretGeneration
	}
	// PostgreSQL timestamptz preserves microseconds. Normalize here so the store
	// contract compares the same timestamps that can be persisted and returned.
	now := s.now().UTC().Truncate(time.Microsecond)
	draft := SessionDraft{
		TokenHash: HashSessionToken(token), CSRFSecretHash: HashCSRFSecret(csrf),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.lifetime),
	}
	if err := draft.validate(); err != nil {
		return Secret{}, Secret{}, SessionDraft{}, err
	}
	return token, csrf, draft, nil
}

func validateStoredSession(session Session, principal Principal, draft SessionDraft) error {
	if err := session.ActiveAt(draft.CreatedAt); err != nil {
		return fmt.Errorf("%w: %v", ErrRepositoryContract, err)
	}
	if err := principal.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrRepositoryContract, err)
	}
	if session.UserID != principal.User.ID || !session.TokenHash.Equal(draft.TokenHash) ||
		!session.CSRFSecretHash.Equal(draft.CSRFSecretHash) || !session.CreatedAt.Equal(draft.CreatedAt) ||
		!session.LastSeenAt.Equal(draft.LastSeenAt) || !session.ExpiresAt.Equal(draft.ExpiresAt) {
		return ErrRepositoryContract
	}
	return nil
}

func validateActiveSession(session Session, principal Principal, now time.Time) error {
	if err := session.ActiveAt(now); err != nil {
		if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrSessionRevoked) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrRepositoryContract, err)
	}
	if err := principal.Validate(); err != nil {
		if errors.Is(err, ErrSignInNotAllowed) {
			return ErrUnauthenticated
		}
		return fmt.Errorf("%w: %v", ErrRepositoryContract, err)
	}
	if session.UserID != principal.User.ID {
		return ErrRepositoryContract
	}
	return nil
}
