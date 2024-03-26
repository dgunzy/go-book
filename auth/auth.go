package auth

import (
	"log"
	"os"

	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/markbates/goth/providers/google"
)

const (
	MaxAge = 86400 * 30
	IsProd = true
)

func NewAuth() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file")
	}

	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")

	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")

	key := os.Getenv("SESSION")
	store := sessions.NewCookieStore([]byte(key))

	store.MaxAge(MaxAge)
	store.Options.Path = "/"
	store.Options.HttpOnly = true
	store.Options.Secure = IsProd

	gothic.Store = store

	goth.UseProviders(
		google.New(googleClientID, googleClientSecret, "http://localhost:8080/home"),
	)

}
