// Package authweb provides the OIDC browser flow and cookie-session adapter.
package authweb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/dgunzy/go-book/internal/oidcclient"
)

const (
	defaultReturnPath = "/book"
	maxFormBytes      = 8 << 10
)

type OIDCClient interface {
	AuthorizationURL(state, nonce, verifier string) string
	Exchange(context.Context, string, string, string) (oidcclient.Claims, error)
}

type IdentitySessions interface {
	CompleteSignIn(context.Context, identity.VerifiedIdentity) (identity.IssuedSession, error)
	CompleteSignInWithInvitation(context.Context, identity.VerifiedIdentity, string) (identity.IssuedSession, error)
	Resume(context.Context, string) (identity.AuthenticatedSession, error)
	Revoke(context.Context, string, string) error
}

type Config struct {
	Deployed        bool
	Acceptance      bool
	LoginAttemptTTL time.Duration
}

type Dependencies struct {
	Attempts AttemptStore
	OIDC     OIDCClient
	Sessions IdentitySessions
}

type Handler struct {
	mux     *http.ServeMux
	deps    Dependencies
	config  Config
	cookies cookieNames
	now     func() time.Time
	secret  func() (identity.Secret, error)
	login   *template.Template
}

func New(config Config, deps Dependencies) (*Handler, error) {
	if deps.Attempts == nil || deps.OIDC == nil || deps.Sessions == nil {
		return nil, errors.New("authentication web dependencies must all be configured")
	}
	if config.LoginAttemptTTL < time.Minute || config.LoginAttemptTTL > 30*time.Minute {
		return nil, errors.New("login attempt TTL must be between 1 and 30 minutes")
	}
	login, err := template.New("login").Parse(loginPage)
	if err != nil {
		return nil, fmt.Errorf("parse login template: %w", err)
	}
	handler := &Handler{
		mux: http.NewServeMux(), deps: deps, config: config, cookies: namesFor(config.Deployed),
		now: time.Now, secret: identity.GenerateSecret, login: login,
	}
	handler.routes()
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /login", h.showLogin)
	h.mux.HandleFunc("GET /invite/{token}", h.acceptInvite)
	h.mux.HandleFunc("GET /auth/google", h.startGoogle)
	h.mux.HandleFunc("GET /auth/callback", h.callback)
	h.mux.HandleFunc("POST /logout", h.logout)
	// registerDevRoutes adds a password-free dev-login only in binaries built
	// with the `dev` build tag; the production build compiles a no-op, so the
	// route does not exist in the shipped image at all.
	h.registerDevRoutes()
}

func (h *Handler) SessionReader() *SessionReader {
	return &SessionReader{Sessions: h.deps.Sessions, Cookies: h.cookies, Acceptance: h.config.Acceptance}
}

func (h *Handler) showLogin(w http.ResponseWriter, r *http.Request) {
	next := safeReturnPath(r.URL.Query().Get("next"), defaultReturnPath)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.login.Execute(w, struct{ StartURL string }{StartURL: authStartURL(next)}); err != nil {
		return
	}
}

// acceptInvite stashes an invite token in a short-lived cookie and starts the
// Google sign-in. The token is validated for real when it is consumed after
// the OIDC round-trip in callback; here it only needs to be well-formed.
func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !validInviteToken(token) {
		h.authError(w, http.StatusBadRequest, "This invite link is not valid.")
		return
	}
	h.setInviteCookie(w, token, h.now().UTC().Add(h.config.LoginAttemptTTL))
	http.Redirect(w, r, authStartURL(defaultReturnPath), http.StatusSeeOther)
}

func validInviteToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}

func (h *Handler) startGoogle(w http.ResponseWriter, r *http.Request) {
	next := safeReturnPath(r.URL.Query().Get("next"), defaultReturnPath)
	state, err := h.secret()
	if err != nil {
		h.internalError(w)
		return
	}
	nonce, err := h.secret()
	if err != nil {
		h.internalError(w)
		return
	}
	verifier, err := h.secret()
	if err != nil {
		h.internalError(w)
		return
	}
	if state.Value() == nonce.Value() || state.Value() == verifier.Value() || nonce.Value() == verifier.Value() {
		h.internalError(w)
		return
	}
	now := h.now().UTC().Truncate(time.Microsecond)
	attempt := LoginAttempt{
		StateHash: HashState(state.Value()), NonceHash: HashNonce(nonce.Value()),
		PKCEVerifier: verifier.Value(), ReturnPath: next, CreatedAt: now, ExpiresAt: now.Add(h.config.LoginAttemptTTL),
	}
	if err := h.deps.Attempts.Create(r.Context(), attempt); err != nil {
		h.internalError(w)
		return
	}
	h.setAttemptCookie(w, nonce.Value(), attempt.ExpiresAt)
	destination := h.deps.OIDC.AuthorizationURL(state.Value(), nonce.Value(), verifier.Value())
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && (h.config.Deployed || parsed.Scheme != "http")) {
		h.internalError(w)
		return
	}
	http.Redirect(w, r, destination, http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		h.clearAttemptCookie(w)
		h.authError(w, http.StatusBadRequest, "Sign-in was not completed.")
		return
	}
	state, stateOK := oneQueryValue(r.URL.Query(), "state")
	code, codeOK := oneQueryValue(r.URL.Query(), "code")
	if !stateOK || !codeOK {
		h.clearAttemptCookie(w)
		h.authError(w, http.StatusBadRequest, "The sign-in response was invalid or expired.")
		return
	}
	nonceCookie, err := r.Cookie(h.cookies.attempt)
	if err != nil {
		h.clearAttemptCookie(w)
		h.authError(w, http.StatusBadRequest, "The sign-in response was invalid or expired.")
		return
	}
	nonce, err := identity.ParseSecret(nonceCookie.Value)
	if err != nil {
		h.clearAttemptCookie(w)
		h.authError(w, http.StatusBadRequest, "The sign-in response was invalid or expired.")
		return
	}
	now := h.now().UTC().Truncate(time.Microsecond)
	attempt, err := h.deps.Attempts.Consume(r.Context(), HashState(state), now)
	h.clearAttemptCookie(w)
	if errors.Is(err, ErrInvalidLoginAttempt) {
		h.authError(w, http.StatusBadRequest, "The sign-in response was invalid or expired.")
		return
	}
	if err != nil {
		h.internalError(w)
		return
	}
	if !attempt.NonceHash.Equal(HashNonce(nonce.Value())) {
		h.authError(w, http.StatusBadRequest, "The sign-in response was invalid or expired.")
		return
	}
	claims, err := h.deps.OIDC.Exchange(r.Context(), code, attempt.PKCEVerifier, nonce.Value())
	if err != nil {
		h.authError(w, http.StatusBadGateway, "The identity provider could not complete sign-in.")
		return
	}
	verified := identity.VerifiedIdentity{
		Provider: "google", Subject: claims.Subject, Email: claims.Email,
		EmailVerified: claims.EmailVerified, DisplayName: normalizedDisplayName(claims.DisplayName, claims.Email),
	}
	// If the sign-in was started from an invite link, consume the invitation
	// to create the member; otherwise require a pre-approved account.
	var issued identity.IssuedSession
	if inviteCookie, cookieErr := r.Cookie(h.cookies.invite); cookieErr == nil && inviteCookie.Value != "" {
		h.clearInviteCookie(w)
		issued, err = h.deps.Sessions.CompleteSignInWithInvitation(r.Context(), verified, inviteCookie.Value)
	} else {
		issued, err = h.deps.Sessions.CompleteSignIn(r.Context(), verified)
	}
	if errors.Is(err, identity.ErrSignInNotAllowed) {
		h.authError(w, http.StatusForbidden, "This account has not been approved for the private book. If you have an invite link, open it again.")
		return
	}
	if err != nil {
		h.internalError(w)
		return
	}
	h.setSessionCookies(w, issued)
	http.Redirect(w, r, attempt.ReturnPath, http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		h.authError(w, http.StatusBadRequest, "The sign-out request was invalid.")
		return
	}
	csrf, ok := oneFormValue(r.PostForm, "csrf_token")
	if !ok {
		h.authError(w, http.StatusForbidden, "The sign-out request was invalid.")
		return
	}
	sessionCookie, err := r.Cookie(h.cookies.session)
	if err != nil {
		h.clearSessionCookies(w)
		h.authError(w, http.StatusForbidden, "The sign-out request was invalid.")
		return
	}
	authenticated, err := h.deps.Sessions.Resume(r.Context(), sessionCookie.Value)
	if errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identity.ErrSessionExpired) || errors.Is(err, identity.ErrSessionRevoked) {
		h.clearSessionCookies(w)
		h.authError(w, http.StatusForbidden, "The sign-out request was invalid.")
		return
	}
	if err != nil {
		h.internalError(w)
		return
	}
	if identity.ValidateCSRF(authenticated.Session, csrf) != nil {
		h.authError(w, http.StatusForbidden, "The sign-out request was invalid.")
		return
	}
	if err := h.deps.Sessions.Revoke(r.Context(), sessionCookie.Value, "user signed out"); err != nil && !errors.Is(err, identity.ErrUnauthenticated) {
		h.internalError(w)
		return
	}
	h.clearSessionCookies(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) internalError(w http.ResponseWriter) {
	h.authError(w, http.StatusInternalServerError, "Unable to complete this request.")
}

func (h *Handler) authError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, message)
}

func oneQueryValue(values url.Values, name string) (string, bool) {
	items, ok := values[name]
	return oneValue(items, ok)
}

func oneFormValue(values url.Values, name string) (string, bool) {
	items, ok := values[name]
	return oneValue(items, ok)
}

func oneValue(items []string, ok bool) (string, bool) {
	return first(items), ok && len(items) == 1 && len(items[0]) <= 4096 && strings.TrimSpace(items[0]) != ""
}

func first(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

func authStartURL(next string) string {
	return (&url.URL{Path: "/auth/google", RawQuery: url.Values{"next": []string{next}}.Encode()}).String()
}

func safeReturnPath(candidate, fallback string) string {
	if fallback == "" {
		fallback = defaultReturnPath
	}
	if candidate == "" || len(candidate) > 2048 || strings.ContainsAny(candidate, "\\\r\n\x00") {
		return fallback
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" || parsed.Fragment != "" ||
		!strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") {
		return fallback
	}
	return parsed.RequestURI()
}

func normalizedDisplayName(name, email string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(strings.SplitN(email, "@", 2)[0])
	}
	if name == "" {
		name = strings.TrimSpace(email)
	}
	runes := []rune(name)
	if len(runes) > 120 {
		name = string(runes[:120])
	}
	return name
}

const loginPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow"><title>Sign in | Cabot Cup</title>
<link rel="stylesheet" href="/assets/site.css"></head>
<body class="private-body"><main class="private-main"><section class="private-forbidden">
<p class="private-kicker">Private book</p><h1>Sign in</h1>
<a class="primary-link" href="{{.StartURL}}">Continue with Google</a>
<a href="/">Return to the public archive</a></section></main></body></html>`
