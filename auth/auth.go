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

const SessionName = "session"

type AuthService struct {
	Store sessions.Store
}

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
	return &AuthService{Store: store}
}

func (s *AuthService) GetSessionUser(r *http.Request) (goth.User, error) {
	session, err := s.Store.Get(r, SessionName)
	if err != nil {
		return goth.User{}, err
	}
	u, ok := session.Values["user"].(goth.User)
	if !ok {
		return goth.User{}, fmt.Errorf("user is not authenticated")
	}
	return u, nil
}

func (s *AuthService) StoreUserSession(w http.ResponseWriter, r *http.Request, user goth.User) error {
	session, _ := s.Store.Get(r, SessionName)
	session.Values["user"] = user
	return session.Save(r, w)
}

func (s *AuthService) RemoveUserSession(w http.ResponseWriter, r *http.Request) {
	session, _ := s.Store.Get(r, SessionName)
	delete(session.Values, "user")
	session.Options.MaxAge = -1
	session.Save(r, w)
}

func RequireAuth(handlerFunc http.HandlerFunc, auth *AuthService, dao *dao.UserDAO) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := auth.GetSessionUser(r)
		if err != nil {
			log.Println("User is not authenticated:", err)
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}

		dbUser, err := dao.GetUserByEmail(user.Email)
		if err != nil {
			log.Println("Error retrieving user from database:", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if dbUser.Role == "user" {
			if IsApplicationOnline() {
				handlerFunc(w, r)
			} else {
				http.Redirect(w, r, "/applicationoffline", http.StatusTemporaryRedirect)
			}
			return
		}

		handlerFunc(w, r)
	}
}
