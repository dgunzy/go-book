package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/dgunzy/go-book/dao"
	"github.com/markbates/goth/gothic"
)

func AuthMiddleware(next http.Handler, userDAO *dao.UserDAO) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract session using the key
		key := os.Getenv("SESSION")
		session, err := gothic.Store.Get(r, key)
		if err != nil {
			// Handle error
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Print all values stored in the session
		fmt.Println("Session values:")
		for k, v := range session.Values {
			fmt.Printf("Key: %v, Value: %v\n", k, v)
		}

		// Assuming the email is stored in session.Values["email"]
		email, ok := session.Values["email"].(string)
		if !ok {
			// If not found, redirect to sign-in page
			http.Redirect(w, r, "/sign-in", http.StatusTemporaryRedirect)
			return
		}

		// Check if user exists in DB
		user, err := userDAO.GetUserByEmail(email) // Assume GetUserByEmail is a method you've implemented
		if err != nil {
			// If user doesn't exist, redirect to sign-up page
			http.Redirect(w, r, "/sign-up", http.StatusTemporaryRedirect)
			return
		}

		// If user exists, add user info to context and proceed
		ctx := context.WithValue(r.Context(), "user", user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
