package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/markbates/goth/gothic"
)

func (handler *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/index.gohtml"))
	tmpl.Execute(w, r.Context())
}

func (handler *Handler) HandleProviderLogin(w http.ResponseWriter, r *http.Request) {
	if u, err := gothic.CompleteUserAuth(w, r); err == nil {
		log.Printf("User already authenticated! %v", u)

		tmpl := template.Must(template.ParseFiles("static/templates/dashboard.gohtml"))
		tmpl.Execute(w, r.Context())
	} else {
		gothic.BeginAuthHandler(w, r)
	}
}
func (handler *Handler) HandleAuthCallbackFunction(w http.ResponseWriter, r *http.Request) {
	user, err := gothic.CompleteUserAuth(w, r)
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}

	err = handler.auth.StoreUserSession(w, r, user)
	if err != nil {
		log.Println(err)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/dashboard.gohtml"))
	tmpl.Execute(w, user)
}

func (handler *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	log.Println("Logging out...")

	err := gothic.Logout(w, r)
	if err != nil {
		log.Println(err)
		return
	}

	handler.auth.RemoveUserSession(w, r)

	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func (h *Handler) HandleHome(w http.ResponseWriter, r *http.Request) {
	user, err := h.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/dashboard.gohtml"))
	tmpl.Execute(w, user)
}

func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	user, err := h.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		return
	}
	dbUser, err := h.dao.GetUserByEmail(user.Email)

	if err != nil {
		log.Println(err)
		return
	}
	fmt.Println(dbUser)

	template := template.Must(template.ParseFiles("static/templates/test.gohtml"))
	template.Execute(w, dbUser)

}
