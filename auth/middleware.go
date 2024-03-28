package auth

import (
	"log"
	"net/http"

	"github.com/dgunzy/go-book/dao"
)

func RequireAdmin(handlerFunc http.HandlerFunc, auth *AuthService, dao *dao.UserDAO) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := auth.GetSessionUser(r)
		if err != nil {
			log.Println("User is not authenticated!")
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}
		log.Printf("user is authenticated! user: %v!", session.Email)

		dbUser, err := dao.GetUserByEmail(session.Email)
		if err != nil {
			log.Println("Error retrieving user from database:", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if dbUser.Role == "admin" || dbUser.Role == "root" {
			handlerFunc(w, r)
		} else {
			log.Println("User does not have admin access!")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	}
}

func RequireRoot(handlerFunc http.HandlerFunc, auth *AuthService, dao *dao.UserDAO) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := auth.GetSessionUser(r)
		if err != nil {
			log.Println("User is not authenticated!")
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}
		log.Printf("user is authenticated! user: %v!", session.Email)

		dbUser, err := dao.GetUserByEmail(session.Email)
		if err != nil {
			log.Println("Error retrieving user from database:", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if dbUser.Role != "root" {
			log.Println("User does not have root access!")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		handlerFunc(w, r)

	}
}
