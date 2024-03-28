package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

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

	template := template.Must(template.ParseFiles("static/templates/admindashboard.gohtml"))
	template.Execute(w, allUsers)
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
	// for _, user := range allUsers {
	// 	fmt.Println(user)
	// }
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

func (handler *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	// Extract the email from the URL
	email := strings.TrimPrefix(r.URL.Path, "/user/")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Println(email)

	// Parse the form values
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Create a map to store the updates
	updates := make(map[string]interface{})

	// Check for each possible field to update
	if username, ok := r.PostForm["username"]; ok {
		updates["username"] = username[0]
	}
	if role, ok := r.PostForm["role"]; ok {
		// Check if the role is valid
		validRoles := []string{"user", "admin", "root"}
		isValidRole := false
		for _, validRole := range validRoles {
			if role[0] == validRole {
				isValidRole = true
				break
			}
		}
		if !isValidRole {
			allUsers, err := handler.dao.GetAllUsers()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			tmpl := template.Must(template.ParseFiles("static/templates/useredit.gohtml"))
			tmpl.Execute(w, allUsers)
			return
		}

		updates["role"] = role[0]
	}
	if balance, ok := r.PostForm["balance"]; ok {
		balanceFloat, err := strconv.ParseFloat(balance[0], 64)
		if err != nil {
			http.Error(w, "Invalid balance value", http.StatusBadRequest)
			return
		}
		updates["balance"] = balanceFloat
	}
	if freePlayBalance, ok := r.PostForm["free_play_balance"]; ok {
		freePlayBalanceFloat, err := strconv.ParseFloat(freePlayBalance[0], 64)
		if err != nil {
			http.Error(w, "Invalid free play balance value", http.StatusBadRequest)
			return
		}
		updates["free_play_balance"] = freePlayBalanceFloat
	}
	if autoApproveLimit, ok := r.PostForm["auto_approve_limit"]; ok {
		autoApproveLimitInt, err := strconv.Atoi(autoApproveLimit[0])
		if err != nil {
			http.Error(w, "Invalid auto approve limit value", http.StatusBadRequest)
			return
		}
		updates["auto_approve_limit"] = autoApproveLimitInt
	}

	// Update the user in the database
	err = handler.dao.UpdateUserByEmail(email, updates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allUsers, err := handler.dao.GetAllUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/useredit.gohtml"))
	err = tmpl.Execute(w, allUsers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func (handler *Handler) createBetHandler(w http.ResponseWriter, r *http.Request) {
	// Call dao.CreateBet here
	// email := strings.TrimPrefix(r.URL.Path, "/bets/")

}

func (handler *Handler) readBetHandler(w http.ResponseWriter, r *http.Request) {
	// Call dao.ReadBet here
}

func (handler *Handler) updateBetHandler(w http.ResponseWriter, r *http.Request) {
	// Call dao.UpdateBet here
}

func (handler *Handler) deleteBetHandler(w http.ResponseWriter, r *http.Request) {
	// Call dao.DeleteBet here
}

func (handler *Handler) createTransactionHandler(w http.ResponseWriter, r *http.Request) {
	// Call dao.CreateTransaction here
}

func (handler *Handler) readTransactionHandler(w http.ResponseWriter, r *http.Request) {
	// Call dao.ReadTransaction here
}
