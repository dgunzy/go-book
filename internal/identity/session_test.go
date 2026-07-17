package identity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

const (
	testSessionID  ID = "33333333-3333-4333-8333-333333333333"
	priorSessionID ID = "44444444-4444-4444-8444-444444444444"
)

type fakeStore struct {
	create func(context.Context, VerifiedIdentity, SessionDraft) (Session, Principal, error)
	use    func(context.Context, Digest, time.Time) (Session, Principal, error)
	rotate func(context.Context, Digest, SessionDraft, time.Time, string) (Session, Principal, error)
	revoke func(context.Context, Digest, time.Time, string) error
}

func (f *fakeStore) CreateSessionForIdentity(ctx context.Context, identity VerifiedIdentity, draft SessionDraft) (Session, Principal, error) {
	return f.create(ctx, identity, draft)
}

func (f *fakeStore) UseSession(ctx context.Context, hash Digest, at time.Time) (Session, Principal, error) {
	return f.use(ctx, hash, at)
}

func (f *fakeStore) RotateSession(ctx context.Context, hash Digest, draft SessionDraft, at time.Time, reason string) (Session, Principal, error) {
	return f.rotate(ctx, hash, draft, at, reason)
}

func (f *fakeStore) RevokeSession(ctx context.Context, hash Digest, at time.Time, reason string) error {
	return f.revoke(ctx, hash, at, reason)
}

func TestNewServiceValidatesDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewService(nil, time.Hour); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("nil store error = %v", err)
	}
	store := &fakeStore{}
	for _, lifetime := range []time.Duration{time.Minute, 31 * 24 * time.Hour} {
		if _, err := NewService(store, lifetime); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("lifetime %s error = %v", lifetime, err)
		}
	}
}

func TestSessionRejectsConfusedHashesAndZeroClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	token, _ := generateSecret(bytes.NewReader(bytes.Repeat([]byte{0x31}, secretBytes)))
	principal := validPrincipal(RoleMember)
	draft := SessionDraft{
		TokenHash: HashSessionToken(token), CSRFSecretHash: HashCSRFSecret(token),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}
	session := sessionFromDraft(draft, principal, "")
	if err := session.ActiveAt(time.Time{}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("zero clock error = %v", err)
	}
	session.CSRFSecretHash = session.TokenHash
	if err := session.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("reused hash error = %v", err)
	}
}

func TestCompleteSignInIssuesHashedCredentials(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 18, 30, 0, 0, time.FixedZone("ADT", -3*60*60))
	principal := validPrincipal(RoleMember)
	store := &fakeStore{}
	store.create = func(_ context.Context, gotIdentity VerifiedIdentity, draft SessionDraft) (Session, Principal, error) {
		if gotIdentity.Provider != "google" || gotIdentity.Subject != "provider-subject-123" {
			t.Fatalf("identity key = %q/%q", gotIdentity.Provider, gotIdentity.Subject)
		}
		if !draft.CreatedAt.Equal(now.UTC()) || !draft.ExpiresAt.Equal(now.UTC().Add(12*time.Hour)) {
			t.Fatalf("draft times = %s - %s", draft.CreatedAt, draft.ExpiresAt)
		}
		return sessionFromDraft(draft, principal, ""), principal, nil
	}
	service := testService(t, store, now, sequentialRandom(), 12*time.Hour)

	issued, err := service.CompleteSignIn(context.Background(), validIdentity())
	if err != nil {
		t.Fatalf("CompleteSignIn() error = %v", err)
	}
	if issued.Token.IsZero() || issued.CSRFSecret.IsZero() || issued.Token.Value() == issued.CSRFSecret.Value() {
		t.Fatal("issued credentials are missing or reused")
	}
	if !issued.Session.TokenHash.Equal(HashSessionToken(issued.Token)) || !issued.Session.CSRFSecretHash.Equal(HashCSRFSecret(issued.CSRFSecret)) {
		t.Fatal("stored hashes do not match issued credentials")
	}
	if err := ValidateCSRF(issued.Session, issued.CSRFSecret.Value()); err != nil {
		t.Fatalf("ValidateCSRF() error = %v", err)
	}
	if err := ValidateCSRF(issued.Session, issued.Token.Value()); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("wrong CSRF error = %v", err)
	}
}

func TestCompleteSignInRejectsClaimsBeforeStore(t *testing.T) {
	t.Parallel()

	store := &fakeStore{create: func(context.Context, VerifiedIdentity, SessionDraft) (Session, Principal, error) {
		t.Fatal("store called for invalid claims")
		return Session{}, Principal{}, nil
	}}
	service := testService(t, store, time.Now(), sequentialRandom(), time.Hour)
	claims := validIdentity()
	claims.EmailVerified = false
	if _, err := service.CompleteSignIn(context.Background(), claims); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("CompleteSignIn() error = %v", err)
	}
}

func TestCompleteSignInDetectsRepositoryContractViolation(t *testing.T) {
	t.Parallel()

	principal := validPrincipal(RoleMember)
	store := &fakeStore{create: func(_ context.Context, _ VerifiedIdentity, draft SessionDraft) (Session, Principal, error) {
		session := sessionFromDraft(draft, principal, "")
		session.UserID = "55555555-5555-4555-8555-555555555555"
		return session, principal, nil
	}}
	service := testService(t, store, time.Now(), sequentialRandom(), time.Hour)
	if _, err := service.CompleteSignIn(context.Background(), validIdentity()); !errors.Is(err, ErrRepositoryContract) {
		t.Fatalf("CompleteSignIn() error = %v", err)
	}
}

func TestResumeValidatesHashExpiryAndPrincipal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 21, 30, 0, 0, time.UTC)
	token, err := generateSecret(bytes.NewReader(bytes.Repeat([]byte{0x77}, secretBytes)))
	if err != nil {
		t.Fatal(err)
	}
	csrf, _ := generateSecret(bytes.NewReader(bytes.Repeat([]byte{0x88}, secretBytes)))
	principal := validPrincipal(RoleAdmin)
	session := Session{
		ID: testSessionID, UserID: principal.User.ID, TokenHash: HashSessionToken(token), CSRFSecretHash: HashCSRFSecret(csrf),
		CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	called := 0
	store := &fakeStore{use: func(_ context.Context, hash Digest, at time.Time) (Session, Principal, error) {
		called++
		if !hash.Equal(HashSessionToken(token)) || !at.Equal(now) {
			t.Fatal("UseSession received wrong lookup values")
		}
		return session, principal, nil
	}}
	service := testService(t, store, now, sequentialRandom(), time.Hour)

	resumed, err := service.Resume(context.Background(), token.Value())
	if err != nil || resumed.Principal.Membership.Role != RoleAdmin {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}
	if _, err := service.Resume(context.Background(), "bad-cookie"); !errors.Is(err, ErrUnauthenticated) || called != 1 {
		t.Fatalf("malformed Resume() error = %v, calls = %d", err, called)
	}

	session.ExpiresAt = now
	if _, err := service.Resume(context.Background(), token.Value()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired Resume() error = %v", err)
	}
	session.ExpiresAt = now.Add(time.Hour)
	principal.User.Status = UserSuspended
	if _, err := service.Resume(context.Background(), token.Value()); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("suspended Resume() error = %v", err)
	}
}

func TestRotateRequiresReasonAndAtomicStoreContract(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 22, 0, 0, 0, time.UTC)
	current, _ := generateSecret(bytes.NewReader(bytes.Repeat([]byte{0x91}, secretBytes)))
	principal := validPrincipal(RoleOwner)
	called := 0
	store := &fakeStore{rotate: func(_ context.Context, hash Digest, draft SessionDraft, at time.Time, reason string) (Session, Principal, error) {
		called++
		if !hash.Equal(HashSessionToken(current)) || !at.Equal(now) || reason != "role changed" {
			t.Fatalf("RotateSession values are incorrect")
		}
		return sessionFromDraft(draft, principal, priorSessionID), principal, nil
	}}
	service := testService(t, store, now, sequentialRandom(), 8*time.Hour)

	if _, err := service.Rotate(context.Background(), current.Value(), " "); !errors.Is(err, ErrInvalidSession) || called != 0 {
		t.Fatalf("empty reason error = %v, calls = %d", err, called)
	}
	issued, err := service.Rotate(context.Background(), current.Value(), " role changed ")
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if issued.Token.Value() == current.Value() || issued.Session.RotatedFrom != priorSessionID || called != 1 {
		t.Fatal("Rotate() did not return a linked replacement")
	}
}

func TestRevokeHashesTokenAndRequiresReason(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 22, 30, 0, 0, time.UTC)
	token, _ := generateSecret(bytes.NewReader(bytes.Repeat([]byte{0xa1}, secretBytes)))
	called := 0
	store := &fakeStore{revoke: func(_ context.Context, hash Digest, at time.Time, reason string) error {
		called++
		if !hash.Equal(HashSessionToken(token)) || !at.Equal(now) || reason != "signed out" {
			t.Fatal("RevokeSession values are incorrect")
		}
		return nil
	}}
	service := testService(t, store, now, sequentialRandom(), time.Hour)

	if err := service.Revoke(context.Background(), token.Value(), "signed out"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if err := service.Revoke(context.Background(), token.Value(), " "); !errors.Is(err, ErrInvalidSession) || called != 1 {
		t.Fatalf("empty reason error = %v, calls = %d", err, called)
	}
	if err := service.Revoke(context.Background(), "bad", "signed out"); !errors.Is(err, ErrUnauthenticated) || called != 1 {
		t.Fatalf("bad token error = %v, calls = %d", err, called)
	}
}

func TestSecretGenerationFailureAndCollision(t *testing.T) {
	t.Parallel()

	store := &fakeStore{create: func(context.Context, VerifiedIdentity, SessionDraft) (Session, Principal, error) {
		t.Fatal("store called after secret generation failure")
		return Session{}, Principal{}, nil
	}}
	service := testService(t, store, time.Now(), bytes.NewReader(make([]byte, secretBytes)), time.Hour)
	if _, err := service.CompleteSignIn(context.Background(), validIdentity()); !errors.Is(err, ErrSecretGeneration) {
		t.Fatalf("short random error = %v", err)
	}
	service.random = bytes.NewReader(make([]byte, secretBytes*2))
	if _, err := service.CompleteSignIn(context.Background(), validIdentity()); !errors.Is(err, ErrSecretGeneration) {
		t.Fatalf("collision error = %v", err)
	}
}

func testService(t *testing.T, store Store, now time.Time, random io.Reader, lifetime time.Duration) *Service {
	t.Helper()
	service, err := NewService(store, lifetime)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	service.random = random
	return service
}

func sessionFromDraft(draft SessionDraft, principal Principal, rotatedFrom ID) Session {
	return Session{
		ID: testSessionID, UserID: principal.User.ID,
		TokenHash: draft.TokenHash, CSRFSecretHash: draft.CSRFSecretHash,
		CreatedAt: draft.CreatedAt, LastSeenAt: draft.LastSeenAt, ExpiresAt: draft.ExpiresAt,
		RotatedFrom: rotatedFrom,
	}
}

func sequentialRandom() io.Reader {
	first := bytes.Repeat([]byte{0x41}, secretBytes)
	second := bytes.Repeat([]byte{0x42}, secretBytes)
	return bytes.NewReader(append(first, second...))
}
