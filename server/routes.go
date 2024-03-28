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
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}

	if err != nil {
		log.Println(err)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
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

func (handler *Handler) HandleHome(w http.ResponseWriter, r *http.Request) {
	user, err := handler.auth.GetSessionUser(r)

	if err != nil {
		log.Println(err)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	dbUser, err := handler.dao.GetUserByEmail(user.Email)

	if err != nil {
		log.Println(err)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/dashboard.gohtml"))
	tmpl.Execute(w, dbUser)
}

// Admin routes

func (handler *Handler) AdminDashboard(w http.ResponseWriter, r *http.Request) {
	user, err := handler.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		return

	}
	dbUser, err := handler.dao.GetUserByEmail(user.Email)
	if err != nil {
		log.Println(err)
		return
	}

	if dbUser.Role != "admin" || dbUser.Role != "root" {
		log.Println(err)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	template := template.Must(template.ParseFiles("static/templates/admindashboard.gohtml"))
	template.Execute(w, dbUser)
}

// Root routes
func (handler *Handler) RootAdminDashboard(w http.ResponseWriter, r *http.Request) {
	user, err := handler.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		return
	}

	dbUser, err := handler.dao.GetUserByEmail(user.Email)

	if err != nil {
		log.Println(err)
		return
	}

	if dbUser.Role != "root" {
		log.Println(err)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	template := template.Must(template.ParseFiles("static/templates/rootdashboard.gohtml"))
	template.Execute(w, dbUser)
}
func (handler *Handler) RootUserEditingDashboard(w http.ResponseWriter, r *http.Request) {
	user, err := handler.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		return
	}

	dbUser, err := handler.dao.GetUserByEmail(user.Email)
	if err != nil {
		log.Println(err)
		return
	}

	if dbUser.Role != "root" {
		log.Println("User does not have root access!")
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	allUsers, err := handler.dao.GetAllUsers()
	if err != nil {
		log.Println(err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	template := template.Must(template.ParseFiles("static/templates/useredit.gohtml"))
	err = template.Execute(w, allUsers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
// 	user, err := h.auth.GetSessionUser(r)
// 	if err != nil {
// 		log.Println(err)
// 		return
// 	}
// 	dbUser, err := h.dao.GetUserByEmail(user.Email)

// 	if err != nil {
// 		log.Println(err)
// 		return
// 	}

// 	template := template.Must(template.ParseFiles("static/templates/test.gohtml"))
// 	template.Execute(w, dbUser)

// }
