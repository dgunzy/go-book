package auth

import (
	"fmt"
	"net/http"

	"github.com/gorilla/sessions"
)

type SessionOptions struct {
	CookiesKey string
	MaxAge     int
	HttpOnly   bool // Should be true if the site is served over HTTP (development environment)
	Secure     bool // Should be true if the site is served over HTTPS (production environment)
	SameSite   http.SameSite
}

func NewCookieStore(opts SessionOptions) *sessions.CookieStore {
	store := sessions.NewCookieStore([]byte(opts.CookiesKey))
	maxAge := 86400 * 7 // 7 days
	store.MaxAge(maxAge)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   opts.Secure, // true for HTTPS
		SameSite: http.SameSiteLaxMode,
	}
	fmt.Printf("New CookieStore created with MaxAge: %d, HttpOnly: %v, Secure: %v, SameSite: %v, Domain: %s\n",
		store.Options.MaxAge, store.Options.HttpOnly, store.Options.Secure, store.Options.SameSite, store.Options.Domain)
	return store
}
