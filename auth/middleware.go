package auth

import (
	"fmt"
	"net/http"

	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/models"
	"github.com/markbates/goth/gothic"
)

func AuthMiddleware(next http.Handler, userDao *dao.UserDAO) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := gothic.CompleteUserAuth(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Check if the user exists in the database
		dbUser, err := userDao.GetUserByEmail(user.Email)
		if err != nil {
			if err.Error() == "user not found" {
				// User doesn't exist, create a new user with the email and default values
				newUser := &models.User{
					Email: user.Email,
					Role:  "user",
				}
				err = userDao.CreateUser(newUser)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				dbUser = newUser
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		fmt.Println(dbUser)
		// Store the user information in the session
		session, _ := gothic.Store.Get(r, "_gothic_session")
		session.Values["user"] = dbUser
		err = session.Save(r, w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}
