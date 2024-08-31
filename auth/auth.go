package auth

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/dgunzy/go-book/dao"
	"github.com/gorilla/sessions"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/markbates/goth/providers/google"
)

type AuthService struct{}

func NewAuthService(store sessions.Store) *AuthService {
	gothic.Store = store
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	callbackURL := os.Getenv("CALLBACK_URL")
	if callbackURL == "" {
		callbackURL = "http://localhost:8080/auth/google/callback" // Default fallback
		log.Println("Warning: CALLBACK_URL not set, using default:", callbackURL)
	}
	goth.UseProviders(
		google.New(googleClientID, googleClientSecret, callbackURL),
	)
	return &AuthService{}
}
func (s *AuthService) GetSessionUser(r *http.Request) (goth.User, error) {
	session, err := gothic.Store.Get(r, SessionName)
	if err != nil {
		return goth.User{}, err
	}

	u := session.Values["user"]
	if u == nil {
		return goth.User{}, fmt.Errorf("user is not authenticated! %v", u)
	}

	return u.(goth.User), nil
}
func (s *AuthService) StoreUserSession(w http.ResponseWriter, r *http.Request, user goth.User) error {
	// Get a session. We're ignoring the error resulted from decoding an
	// existing session: Get() always returns a session, even if empty.
	session, _ := gothic.Store.Get(r, SessionName)

	session.Values["user"] = user

	err := session.Save(r, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return err
	}

	return nil
}
func (s *AuthService) RemoveUserSession(w http.ResponseWriter, r *http.Request) {
	session, err := gothic.Store.Get(r, SessionName)
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	session.Values["user"] = goth.User{}
	// delete the cookie immediately
	session.Options.MaxAge = -1

	session.Save(r, w)
}

func RequireAuth(handlerFunc http.HandlerFunc, auth *AuthService, dao *dao.UserDAO) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := auth.GetSessionUser(r)
		if err != nil {
			log.Println("User is not authenticated!")
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}

		// log.Printf("user is authenticated! user: %v!", session.Email)

		dbUser, err := dao.GetUserByEmail(user.Email)
		if err != nil {
			log.Println("Error retrieving user from database:", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if dbUser.Role == "user" {
			if IsApplicationOnline() {
				handlerFunc(w, r)
				return
			} else {
				http.Redirect(w, r, "/applicationoffline", http.StatusTemporaryRedirect)
				return
			}
		}
		handlerFunc(w, r)
	}

}
