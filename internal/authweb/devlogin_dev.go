//go:build dev

package authweb

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/dgunzy/go-book/internal/identity"
)

// This file is compiled only under the `dev` build tag. It adds a
// password-free dev-login that mints a real session for an already-approved
// mock account, skipping the Google exchange. It exists solely so the private
// book can be exercised end-to-end against a disposable database with mock
// users. The production image is built without this tag, so the route is
// physically absent from the shipped binary.

var devLoginForm = template.Must(template.New("dev-login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Dev login</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>body{font-family:system-ui;margin:3rem auto;max-width:32rem;padding:0 1rem}
form{display:grid;gap:.75rem;margin-top:1rem}label{font-weight:600}
input{padding:.5rem;font-size:1rem}button{padding:.6rem;font-size:1rem;cursor:pointer}
.warn{background:#fff3cd;border:1px solid #ffe69c;padding:.75rem;border-radius:.4rem}
ul{color:#555}</style></head>
<body>
<h1>Dev login</h1>
<p class="warn"><strong>Development build only.</strong> This bypass is absent from the production image. It only signs in accounts that already exist and are approved in the connected database (seed them with <code>cabot mock-seed</code>).</p>
<form method="post" action="/dev/login">
  <label for="email">Email of a seeded, approved account</label>
  <input id="email" name="email" type="email" required placeholder="owner@cabot.test" autofocus>
  <label for="display_name">Display name (optional)</label>
  <input id="display_name" name="display_name" type="text" placeholder="Owner">
  <button type="submit">Sign in</button>
</form>
<p>Suggested seeded accounts:</p>
<ul><li>owner@cabot.test (owner)</li><li>admin@cabot.test (admin)</li><li>member@cabot.test (member)</li></ul>
</body></html>`))

func (h *Handler) registerDevRoutes() {
	h.mux.HandleFunc("GET /dev/login", h.devLoginForm)
	h.mux.HandleFunc("POST /dev/login", h.devLogin)
}

func (h *Handler) devLoginForm(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = devLoginForm.Execute(w, nil)
}

func (h *Handler) devLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.authError(w, http.StatusBadRequest, "The dev-login form could not be read.")
		return
	}
	email, err := identity.NormalizeEmail(r.PostForm.Get("email"))
	if err != nil {
		h.authError(w, http.StatusBadRequest, "Enter a valid email that has been seeded and approved.")
		return
	}
	display := normalizedDisplayName(strings.TrimSpace(r.PostForm.Get("display_name")), email)

	// A deterministic synthetic subject per email so repeated dev-logins
	// resolve to the same linked identity. The account must already be
	// approved; CompleteSignIn returns ErrSignInNotAllowed otherwise.
	verified := identity.VerifiedIdentity{
		Provider: "google", Subject: "dev|" + email, Email: email,
		EmailVerified: true, DisplayName: display,
	}
	// Honor an invite cookie exactly as the real Google callback does, so the
	// full invite flow can be exercised on the dev harness without Google.
	var issued identity.IssuedSession
	if inviteCookie, cookieErr := r.Cookie(h.cookies.invite); cookieErr == nil && inviteCookie.Value != "" {
		h.clearInviteCookie(w)
		issued, err = h.deps.Sessions.CompleteSignInWithInvitation(r.Context(), verified, inviteCookie.Value)
	} else {
		issued, err = h.deps.Sessions.CompleteSignIn(r.Context(), verified)
	}
	if err != nil {
		h.authError(w, http.StatusForbidden, "That account is not approved in the connected database. Seed it, or open a valid invite link first.")
		return
	}
	h.setSessionCookies(w, issued)
	http.Redirect(w, r, "/book", http.StatusSeeOther)
}
