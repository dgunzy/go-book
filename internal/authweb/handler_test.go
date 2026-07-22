package authweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/dgunzy/go-book/internal/oidcclient"
	"github.com/dgunzy/go-book/internal/privateweb"
)

type fakeAttempts struct {
	created     []LoginAttempt
	createErr   error
	consumed    ConsumedAttempt
	consumeErr  error
	consumeHash AttemptHash
	consumeAt   time.Time
}

func (f *fakeAttempts) Create(_ context.Context, attempt LoginAttempt) error {
	f.created = append(f.created, attempt)
	return f.createErr
}

func (f *fakeAttempts) Consume(_ context.Context, hash AttemptHash, at time.Time) (ConsumedAttempt, error) {
	f.consumeHash, f.consumeAt = hash, at
	return f.consumed, f.consumeErr
}

type fakeOIDC struct {
	authState, authNonce, authVerifier            string
	exchangeCode, exchangeVerifier, exchangeNonce string
	claims                                        oidcclient.Claims
	err                                           error
}

func (f *fakeOIDC) AuthorizationURL(state, nonce, verifier string) string {
	f.authState, f.authNonce, f.authVerifier = state, nonce, verifier
	return "https://accounts.example.test/authorize?state=" + url.QueryEscape(state)
}

func (f *fakeOIDC) Exchange(_ context.Context, code, verifier, nonce string) (oidcclient.Claims, error) {
	f.exchangeCode, f.exchangeVerifier, f.exchangeNonce = code, verifier, nonce
	return f.claims, f.err
}

type fakeIdentitySessions struct {
	completeIdentity          identity.VerifiedIdentity
	completeResult            identity.IssuedSession
	completeErr               error
	invitedToken              string
	invitedResult             identity.IssuedSession
	invitedErr                error
	resumeToken               string
	resumeResult              identity.AuthenticatedSession
	resumeErr                 error
	revokeToken, revokeReason string
	revokeErr                 error
}

func (f *fakeIdentitySessions) CompleteSignIn(_ context.Context, verified identity.VerifiedIdentity) (identity.IssuedSession, error) {
	f.completeIdentity = verified
	return f.completeResult, f.completeErr
}

func (f *fakeIdentitySessions) CompleteSignInWithInvitation(_ context.Context, verified identity.VerifiedIdentity, token string) (identity.IssuedSession, error) {
	f.completeIdentity = verified
	f.invitedToken = token
	if f.invitedErr != nil || f.invitedResult != (identity.IssuedSession{}) {
		return f.invitedResult, f.invitedErr
	}
	return f.completeResult, f.completeErr
}

func (f *fakeIdentitySessions) Resume(_ context.Context, token string) (identity.AuthenticatedSession, error) {
	f.resumeToken = token
	return f.resumeResult, f.resumeErr
}

func (f *fakeIdentitySessions) Revoke(_ context.Context, token, reason string) error {
	f.revokeToken, f.revokeReason = token, reason
	return f.revokeErr
}

func TestNewValidatesDependenciesAndTTL(t *testing.T) {
	t.Parallel()

	valid := Dependencies{Attempts: &fakeAttempts{}, OIDC: &fakeOIDC{}, Sessions: &fakeIdentitySessions{}}
	for _, test := range []struct {
		name   string
		config Config
		deps   Dependencies
	}{
		{name: "dependencies", config: Config{LoginAttemptTTL: 10 * time.Minute}},
		{name: "short TTL", config: Config{LoginAttemptTTL: time.Second}, deps: valid},
		{name: "long TTL", config: Config{LoginAttemptTTL: time.Hour}, deps: valid},
	} {
		if _, err := New(test.config, test.deps); err == nil {
			t.Errorf("New(%s) error = nil", test.name)
		}
	}
}

func TestLoginPageUsesOnlySafeLocalReturnPath(t *testing.T) {
	t.Parallel()

	h := newTestHandler(t, false, &fakeAttempts{}, &fakeOIDC{}, &fakeIdentitySessions{})
	for _, test := range []struct {
		name, raw, want string
	}{
		{name: "member query", raw: "/book/wagers?status=open", want: "%2Fbook%2Fwagers%3Fstatus%3Dopen"},
		{name: "absolute", raw: "https://evil.example/steal", want: "%2Fbook"},
		{name: "scheme relative", raw: "//evil.example/steal", want: "%2Fbook"},
		{name: "encoded scheme relative", raw: "%2F%2Fevil.example/steal", want: "%2Fbook"},
		{name: "backslash", raw: "/\\evil.example", want: "%2Fbook"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/login?next="+url.QueryEscape(test.raw), nil)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("response = %d %s, want encoded next %q", response.Code, response.Body.String(), test.want)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("login page is cacheable")
			}
		})
	}
}

func TestStartGooglePersistsAttemptAndSetsDeployedCookie(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	attempts, provider := &fakeAttempts{}, &fakeOIDC{}
	h := newTestHandler(t, true, attempts, provider, &fakeIdentitySessions{})
	h.now = func() time.Time { return now }
	h.secret = secretSequence(t, 0x11, 0x22, 0x33)
	request := httptest.NewRequest(http.MethodGet, "/auth/google?next=%2Fbook%2Fledger", nil)
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	if response.Code != http.StatusFound || !strings.HasPrefix(response.Header().Get("Location"), "https://accounts.example.test/") {
		t.Fatalf("response = %d location %q", response.Code, response.Header().Get("Location"))
	}
	if len(attempts.created) != 1 {
		t.Fatalf("created attempts = %d", len(attempts.created))
	}
	attempt := attempts.created[0]
	if attempt.ReturnPath != "/book/ledger" || !attempt.CreatedAt.Equal(now) || !attempt.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("attempt = %#v", attempt)
	}
	if !attempt.StateHash.Equal(HashState(provider.authState)) || !attempt.NonceHash.Equal(HashNonce(provider.authNonce)) || attempt.PKCEVerifier != provider.authVerifier {
		t.Fatal("persisted attempt does not match OIDC authorization values")
	}
	if len(provider.authVerifier) != 43 || provider.authState == provider.authNonce || provider.authNonce == provider.authVerifier {
		t.Fatal("state, nonce, and PKCE values are missing or reused")
	}
	cookie := findCookie(t, response.Result().Cookies(), "__Host-cabot_oidc")
	if cookie.Value != provider.authNonce || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("attempt cookie = %#v", cookie)
	}
}

func TestStartGoogleDoesNotRedirectWhenAttemptCannotPersist(t *testing.T) {
	t.Parallel()

	attempts := &fakeAttempts{createErr: errors.New("postgres credentials")}
	h := newTestHandler(t, false, attempts, &fakeOIDC{}, &fakeIdentitySessions{})
	h.secret = secretSequence(t, 1, 2, 3)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/auth/google", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "postgres") || response.Header().Get("Location") != "" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestCallbackConsumesAttemptIssuesSessionAndRedirects(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	nonce, token, csrf := testSecret(t, 0x44), testSecret(t, 0x55), testSecret(t, 0x66)
	attempts := &fakeAttempts{consumed: ConsumedAttempt{
		NonceHash: HashNonce(nonce.Value()), PKCEVerifier: testSecret(t, 0x77).Value(), ReturnPath: "/book/wagers?status=open",
	}}
	provider := &fakeOIDC{claims: oidcclient.Claims{
		Subject: "google-subject", Email: "member@example.com", EmailVerified: true, DisplayName: "Approved Member",
	}}
	sessions := &fakeIdentitySessions{completeResult: issuedSession(now, token, csrf)}
	h := newTestHandler(t, true, attempts, provider, sessions)
	h.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?state=returned-state&code=authorization-code", nil)
	request.AddCookie(&http.Cookie{Name: "__Host-cabot_oidc", Value: nonce.Value()})
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/book/wagers?status=open" {
		t.Fatalf("response = %d location %q body %q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if !attempts.consumeHash.Equal(HashState("returned-state")) || !attempts.consumeAt.Equal(now) {
		t.Fatal("callback consumed the wrong attempt")
	}
	if provider.exchangeCode != "authorization-code" || provider.exchangeVerifier != attempts.consumed.PKCEVerifier || provider.exchangeNonce != nonce.Value() {
		t.Fatal("OIDC exchange did not receive the bound callback values")
	}
	if sessions.completeIdentity.Provider != "google" || sessions.completeIdentity.Subject != "google-subject" || sessions.completeIdentity.Email != "member@example.com" {
		t.Fatalf("verified identity = %#v", sessions.completeIdentity)
	}
	cookies := response.Result().Cookies()
	for _, name := range []string{"__Host-cabot_session", "__Host-cabot_csrf"} {
		cookie := findCookie(t, cookies, name)
		if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("session cookie %s = %#v", name, cookie)
		}
	}
	if findCookie(t, cookies, "__Host-cabot_session").Value != token.Value() || findCookie(t, cookies, "__Host-cabot_csrf").Value != csrf.Value() {
		t.Fatal("issued cookie values do not match the identity service")
	}
	if findCookie(t, cookies, "__Host-cabot_oidc").MaxAge != -1 {
		t.Fatal("OIDC attempt cookie was not cleared")
	}
}

func TestCallbackRejectsReplayBrowserMismatchAndParameterPollution(t *testing.T) {
	t.Parallel()

	nonce := testSecret(t, 0x21)
	for _, test := range []struct {
		name, target string
		attempt      ConsumedAttempt
		consumeErr   error
		cookie       string
	}{
		{name: "replayed", target: "/auth/callback?state=s&code=c", consumeErr: ErrInvalidLoginAttempt, cookie: nonce.Value()},
		{name: "browser mismatch", target: "/auth/callback?state=s&code=c", attempt: ConsumedAttempt{NonceHash: HashNonce(testSecret(t, 0x22).Value()), PKCEVerifier: testSecret(t, 0x23).Value(), ReturnPath: "/book"}, cookie: nonce.Value()},
		{name: "duplicate state", target: "/auth/callback?state=s&state=other&code=c", cookie: nonce.Value()},
		{name: "missing cookie", target: "/auth/callback?state=s&code=c"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeOIDC{}
			sessions := &fakeIdentitySessions{}
			h := newTestHandler(t, false, &fakeAttempts{consumed: test.attempt, consumeErr: test.consumeErr}, provider, sessions)
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "cabot_oidc", Value: test.cookie})
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || provider.exchangeCode != "" || sessions.completeIdentity.Subject != "" {
				t.Fatalf("response = %d exchange=%q identity=%#v", response.Code, provider.exchangeCode, sessions.completeIdentity)
			}
		})
	}
}

func TestCallbackMapsUnapprovedMemberToForbidden(t *testing.T) {
	t.Parallel()

	nonce := testSecret(t, 0x31)
	attempts := &fakeAttempts{consumed: ConsumedAttempt{NonceHash: HashNonce(nonce.Value()), PKCEVerifier: testSecret(t, 0x32).Value(), ReturnPath: "/book"}}
	provider := &fakeOIDC{claims: oidcclient.Claims{Subject: "unknown", Email: "unknown@example.com", EmailVerified: true, DisplayName: "Unknown"}}
	sessions := &fakeIdentitySessions{completeErr: identity.ErrSignInNotAllowed}
	h := newTestHandler(t, false, attempts, provider, sessions)
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?state=s&code=c", nil)
	request.AddCookie(&http.Cookie{Name: "cabot_oidc", Value: nonce.Value()})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "not been approved") || strings.Contains(response.Header().Get("Set-Cookie"), "cabot_session=") {
		t.Fatalf("response = %d %q cookies=%q", response.Code, response.Body.String(), response.Header().Values("Set-Cookie"))
	}
}

func TestNormalizedDisplayNameFallsBackAndBoundsProviderData(t *testing.T) {
	t.Parallel()

	if got := normalizedDisplayName("", "member@example.com"); got != "member" {
		t.Fatalf("fallback display name = %q", got)
	}
	if got := normalizedDisplayName(strings.Repeat("x", 121), "member@example.com"); len([]rune(got)) != 120 {
		t.Fatalf("bounded display name length = %d", len([]rune(got)))
	}
}

func TestCallbackDoesNotDiscloseProviderErrors(t *testing.T) {
	t.Parallel()

	nonce := testSecret(t, 0x71)
	attempts := &fakeAttempts{consumed: ConsumedAttempt{NonceHash: HashNonce(nonce.Value()), PKCEVerifier: testSecret(t, 0x72).Value(), ReturnPath: "/book"}}
	provider := &fakeOIDC{err: errors.New("oauth token endpoint secret response")}
	h := newTestHandler(t, false, attempts, provider, &fakeIdentitySessions{})
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?state=s&code=c", nil)
	request.AddCookie(&http.Cookie{Name: "cabot_oidc", Value: nonce.Value()})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestLogoutRequiresValidSessionCSRFAndRevokes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	token, csrf := testSecret(t, 0x81), testSecret(t, 0x82)
	sessions := &fakeIdentitySessions{resumeResult: authenticatedSession(now, token, csrf)}
	h := newTestHandler(t, true, &fakeAttempts{}, &fakeOIDC{}, sessions)
	h.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(url.Values{"csrf_token": []string{csrf.Value()}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "__Host-cabot_session", Value: token.Value()})
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" || sessions.revokeToken != token.Value() || sessions.revokeReason != "user signed out" {
		t.Fatalf("response=%d location=%q revoke=%q/%q", response.Code, response.Header().Get("Location"), sessions.revokeToken, sessions.revokeReason)
	}
	for _, name := range []string{"__Host-cabot_session", "__Host-cabot_csrf"} {
		if findCookie(t, response.Result().Cookies(), name).MaxAge != -1 {
			t.Fatalf("cookie %s was not cleared", name)
		}
	}
}

func TestLogoutRejectsInvalidAndDuplicateCSRF(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	token, csrf := testSecret(t, 0x91), testSecret(t, 0x92)
	for _, body := range []string{
		url.Values{"csrf_token": []string{token.Value()}}.Encode(),
		"csrf_token=" + url.QueryEscape(csrf.Value()) + "&csrf_token=duplicate",
		"",
	} {
		sessions := &fakeIdentitySessions{resumeResult: authenticatedSession(now, token, csrf)}
		h := newTestHandler(t, false, &fakeAttempts{}, &fakeOIDC{}, sessions)
		request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: "cabot_session", Value: token.Value()})
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || sessions.revokeToken != "" {
			t.Fatalf("body %q response=%d revoke=%q", body, response.Code, sessions.revokeToken)
		}
	}
}

func TestLogoutPreservesSessionOnInfrastructureFailure(t *testing.T) {
	t.Parallel()

	token, csrf := testSecret(t, 0x93), testSecret(t, 0x94)
	sessions := &fakeIdentitySessions{resumeErr: errors.New("database unavailable")}
	h := newTestHandler(t, false, &fakeAttempts{}, &fakeOIDC{}, sessions)
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(url.Values{"csrf_token": []string{csrf.Value()}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "cabot_session", Value: token.Value()})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || len(response.Header().Values("Set-Cookie")) != 0 || sessions.revokeToken != "" {
		t.Fatalf("response=%d cookies=%#v revoke=%q", response.Code, response.Header().Values("Set-Cookie"), sessions.revokeToken)
	}
}

func TestSessionReaderAdaptsValidatedCookies(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	token, csrf := testSecret(t, 0xa1), testSecret(t, 0xa2)
	sessions := &fakeIdentitySessions{resumeResult: authenticatedSession(now, token, csrf)}
	reader := &SessionReader{Sessions: sessions, Cookies: namesFor(false), Acceptance: true}
	request := httptest.NewRequest(http.MethodGet, "/book", nil)
	request.AddCookie(&http.Cookie{Name: "cabot_session", Value: token.Value()})
	request.AddCookie(&http.Cookie{Name: "cabot_csrf", Value: csrf.Value()})

	got, err := reader.CurrentSession(request)
	if err != nil {
		t.Fatalf("CurrentSession() error = %v", err)
	}
	if got.UserID != "11111111-1111-4111-8111-111111111111" || got.DisplayName != "Approved Member" || got.Role != privateweb.RoleAdmin || !got.Active || got.CSRFToken != csrf.Value() || !got.Acceptance {
		t.Fatalf("CurrentSession() = %#v", got)
	}

	missing := httptest.NewRequest(http.MethodGet, "/book", nil)
	if _, err := reader.CurrentSession(missing); !errors.Is(err, privateweb.ErrNoSession) {
		t.Fatalf("missing cookie error = %v", err)
	}
	wrongCSRF := httptest.NewRequest(http.MethodGet, "/book", nil)
	wrongCSRF.AddCookie(&http.Cookie{Name: "cabot_session", Value: token.Value()})
	wrongCSRF.AddCookie(&http.Cookie{Name: "cabot_csrf", Value: token.Value()})
	if _, err := reader.CurrentSession(wrongCSRF); !errors.Is(err, privateweb.ErrNoSession) {
		t.Fatalf("wrong CSRF error = %v", err)
	}
	sessions.resumeErr = errors.New("database unavailable")
	if _, err := reader.CurrentSession(request); err == nil || errors.Is(err, privateweb.ErrNoSession) {
		t.Fatalf("infrastructure error = %v", err)
	}
}

func newTestHandler(t *testing.T, deployed bool, attempts AttemptStore, provider OIDCClient, sessions IdentitySessions) *Handler {
	t.Helper()
	handler, err := New(Config{Deployed: deployed, LoginAttemptTTL: 10 * time.Minute}, Dependencies{
		Attempts: attempts, OIDC: provider, Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func testSecret(t *testing.T, value byte) identity.Secret {
	t.Helper()
	raw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	secret, err := identity.ParseSecret(raw)
	if err != nil {
		t.Fatalf("ParseSecret() error = %v", err)
	}
	return secret
}

func secretSequence(t *testing.T, values ...byte) func() (identity.Secret, error) {
	t.Helper()
	index := 0
	return func() (identity.Secret, error) {
		if index >= len(values) {
			return identity.Secret{}, errors.New("secret sequence exhausted")
		}
		secret := testSecret(t, values[index])
		index++
		return secret, nil
	}
}

func issuedSession(now time.Time, token, csrf identity.Secret) identity.IssuedSession {
	authenticated := authenticatedSession(now, token, csrf)
	return identity.IssuedSession{
		Session: authenticated.Session, Principal: authenticated.Principal,
		Token: token, CSRFSecret: csrf,
	}
}

func authenticatedSession(now time.Time, token, csrf identity.Secret) identity.AuthenticatedSession {
	userID := identity.ID("11111111-1111-4111-8111-111111111111")
	return identity.AuthenticatedSession{
		Session: identity.Session{
			ID: "33333333-3333-4333-8333-333333333333", UserID: userID,
			TokenHash: identity.HashSessionToken(token), CSRFSecretHash: identity.HashCSRFSecret(csrf),
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(12 * time.Hour),
		},
		Principal: identity.Principal{
			User: identity.User{ID: userID, DisplayName: "Approved Member", Email: "member@example.com", Status: identity.UserActive},
			Membership: identity.Membership{
				ID: "22222222-2222-4222-8222-222222222222", UserID: userID,
				Role: identity.RoleAdmin, GrantedAt: now.Add(-time.Hour),
			},
		},
	}
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found in %#v", name, cookies)
	return nil
}
