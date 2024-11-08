package auth

import (
	"fmt"
	"net/http"
	"os"

	"github.com/dgunzy/go-book/dao"
	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/markbates/goth/providers/google"
)

const SessionName = "session"

type AuthService struct {
	Store sessions.Store
}

func NewAuthService(store sessions.Store) *AuthService {
	isLocalDev := os.Getenv("ENV") == "local"

	if isLocalDev {
		godotenv.Load()
	}
	gothic.Store = store
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	callbackURL := os.Getenv("CALLBACK_URL")
	if callbackURL == "" {
		callbackURL = "http://localhost:8080/auth/google/callback" // Default fallback
		fmt.Println("Warning: CALLBACK_URL not set, using default:", callbackURL)
	}
	goth.UseProviders(
		google.New(googleClientID, googleClientSecret, callbackURL),
	)
	return &AuthService{Store: store}
}

func (s *AuthService) GetSessionUser(r *http.Request) (goth.User, error) {
	session, err := s.Store.Get(r, SessionName)
	if err != nil {
		fmt.Printf("Error getting session: %v", err)
		return goth.User{}, err
	}
	u, ok := session.Values["user"].(goth.User)
	if !ok {
		fmt.Println("User not found in session")
		return goth.User{}, fmt.Errorf("user is not authenticated")
	}
	fmt.Printf("Session user retrieved: %s", u.Email)
	return u, nil
}

func (s *AuthService) StoreUserSession(w http.ResponseWriter, r *http.Request, user goth.User) error {
	session, err := s.Store.Get(r, SessionName)
	if err != nil {
		fmt.Printf("Error getting session for storing user: %v", err)
		return err
	}
	session.Values["user"] = user
	err = session.Save(r, w)
	if err != nil {
		fmt.Printf("Error saving session: %v", err)
		return err
	}
	fmt.Printf("User session stored for: %s", user.Email)
	return nil
}

func (s *AuthService) RemoveUserSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.Store.Get(r, SessionName)
	if err != nil {
		fmt.Printf("Error getting session for removal: %v", err)
		return
	}
	delete(session.Values, "user")
	session.Options.MaxAge = -1
	err = session.Save(r, w)
	if err != nil {
		fmt.Printf("Error saving session after removal: %v", err)
	} else {
		fmt.Println("User session removed successfully")
	}
}

func RequireAuth(handlerFunc http.HandlerFunc, auth *AuthService, dao *dao.UserDAO) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := auth.GetSessionUser(r)
		if err != nil {
			fmt.Printf("User is not authenticated: %v", err)
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}
		dbUser, err := dao.GetUserByEmail(user.Email)
		if err != nil {
			fmt.Printf("Error retrieving user from database: %v", err)
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
