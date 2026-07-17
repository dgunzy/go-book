package authweb

import (
	"errors"
	"net/http"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/dgunzy/go-book/internal/privateweb"
)

type cookieNames struct {
	session string
	csrf    string
	attempt string
	secure  bool
}

func namesFor(deployed bool) cookieNames {
	if deployed {
		return cookieNames{
			session: "__Host-cabot_session", csrf: "__Host-cabot_csrf",
			attempt: "__Host-cabot_oidc", secure: true,
		}
	}
	return cookieNames{session: "cabot_session", csrf: "cabot_csrf", attempt: "cabot_oidc"}
}

func (h *Handler) setAttemptCookie(w http.ResponseWriter, value string, expires time.Time) {
	h.setCookie(w, h.cookies.attempt, value, expires)
}

func (h *Handler) clearAttemptCookie(w http.ResponseWriter) {
	h.clearCookie(w, h.cookies.attempt)
}

func (h *Handler) setSessionCookies(w http.ResponseWriter, issued identity.IssuedSession) {
	h.setCookie(w, h.cookies.session, issued.Token.Value(), issued.Session.ExpiresAt)
	h.setCookie(w, h.cookies.csrf, issued.CSRFSecret.Value(), issued.Session.ExpiresAt)
}

func (h *Handler) clearSessionCookies(w http.ResponseWriter) {
	h.clearCookie(w, h.cookies.session)
	h.clearCookie(w, h.cookies.csrf)
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	maxAge := int(expires.Sub(h.now().UTC()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires, MaxAge: maxAge,
		Secure: h.cookies.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: "/", Expires: time.Unix(1, 0), MaxAge: -1,
		Secure: h.cookies.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// SessionReader adapts identity sessions to the private web read model. Both
// cookies must validate against the server-side hashes before a session is exposed.
type SessionReader struct {
	Sessions IdentitySessions
	Cookies  cookieNames
}

func (s *SessionReader) CurrentSession(r *http.Request) (privateweb.Session, error) {
	if s == nil || s.Sessions == nil {
		return privateweb.Session{}, errors.New("identity sessions are not configured")
	}
	tokenCookie, err := r.Cookie(s.Cookies.session)
	if err != nil {
		return privateweb.Session{}, privateweb.ErrNoSession
	}
	csrfCookie, err := r.Cookie(s.Cookies.csrf)
	if err != nil {
		return privateweb.Session{}, privateweb.ErrNoSession
	}
	authenticated, err := s.Sessions.Resume(r.Context(), tokenCookie.Value)
	if errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identity.ErrSessionExpired) || errors.Is(err, identity.ErrSessionRevoked) {
		return privateweb.Session{}, privateweb.ErrNoSession
	}
	if err != nil {
		return privateweb.Session{}, err
	}
	if err := identity.ValidateCSRF(authenticated.Session, csrfCookie.Value); err != nil {
		return privateweb.Session{}, privateweb.ErrNoSession
	}
	return privateweb.Session{
		UserID: string(authenticated.Principal.User.ID), DisplayName: authenticated.Principal.User.DisplayName,
		Role: privateweb.Role(authenticated.Principal.Membership.Role), Active: true, CSRFToken: csrfCookie.Value,
	}, nil
}
