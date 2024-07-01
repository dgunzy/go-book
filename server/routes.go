package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/models"
	"github.com/markbates/goth/gothic"
)

func parseInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0.0
	}
	return f
}
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

	http.Redirect(w, r, "/cabot-book", http.StatusFound)
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

	tmpl := template.Must(template.ParseFiles("static/templates/mainpage.gohtml"))
	if err := tmpl.Execute(w, *dbUser); err != nil {
		log.Println("Error executing template:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) Navbar(w http.ResponseWriter, r *http.Request) {
	user, err := handler.auth.GetSessionUser(r)

	if err != nil {
		fmt.Println(err)
		log.Println(err)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	dbUser, err := handler.dao.GetUserByEmail(user.Email)

	if err != nil {
		fmt.Println(err)
		log.Println(err)
		return
	}

	var tmpl *template.Template
	switch dbUser.Role {
	case "admin":
		tmpl = template.Must(template.ParseFiles("static/templates/fragments/navbaradmin.gohtml"))
	case "root":
		tmpl = template.Must(template.ParseFiles("static/templates/fragments/navbarroot.gohtml"))
	default:
		tmpl = template.Must(template.ParseFiles("static/templates/fragments/navbar.gohtml"))
	}
	if err := tmpl.Execute(w, nil); err != nil {
		log.Println("Error executing template:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}

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

	type AdminDashboardStruct struct {
		User     *models.User
		AllUsers []*models.User
	}

	allUsers, err := handler.dao.GetAllUsers()
	if err != nil {
		log.Println(err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	adminStruct := AdminDashboardStruct{
		User:     dbUser,
		AllUsers: allUsers,
	}

	template := template.Must(template.ParseFiles("static/templates/admindashboard.gohtml"))
	template.Execute(w, adminStruct)
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
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	// Extract the email from the URL
	email := strings.TrimPrefix(r.URL.Path, "/update-user/")
	w.Header().Set("Cache-Control", "no-store")
	// fmt.Println(email)

	// Parse the form values
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Get the user from the database
	user, err := handler.dao.GetUserByEmail(email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Update the user fields based on the form values
	if username := r.FormValue("username"); username != "" {
		user.Username = username
	}
	if role := r.FormValue("role"); role != "" {
		// Check if the role is valid
		validRoles := []string{"user", "admin", "root"}
		isValidRole := false
		for _, validRole := range validRoles {
			if role == validRole {
				isValidRole = true
				break
			}
		}
		if !isValidRole {
			http.Error(w, "Invalid role value", http.StatusBadRequest)
			return
		}
		user.Role = role
	}
	if freePlayBalance := r.FormValue("free_play_balance"); freePlayBalance != "" {
		freePlayBalanceFloat, err := strconv.ParseFloat(freePlayBalance, 64)
		if err != nil {
			http.Error(w, "Invalid free play balance value", http.StatusBadRequest)
			return
		}
		user.FreePlayBalance = freePlayBalanceFloat
	}
	if autoApproveLimit := r.FormValue("auto_approve_limit"); autoApproveLimit != "" {
		autoApproveLimitInt, err := strconv.Atoi(autoApproveLimit)
		if err != nil {
			http.Error(w, "Invalid auto approve limit value", http.StatusBadRequest)
			return
		}
		user.AutoApproveLimit = autoApproveLimitInt
	}

	// Update the user in the database
	err = handler.dao.UpdateUser(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Render the updated user block template
	tmpl := template.Must(template.ParseFiles("static/templates/fragments/editeduser.gohtml"))
	err = tmpl.Execute(w, user)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func (handler *Handler) UpdateUserForm(w http.ResponseWriter, r *http.Request) {
	// Extract the email from the URL
	email := strings.TrimPrefix(r.URL.Path, "/user/")

	editingUser, err := handler.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		return
	}

	editingUserDb, err := handler.dao.GetUserByEmail(editingUser.Email)

	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Get the user from the database
	user, err := handler.dao.GetUserByEmail(email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// fmt.Println(user.Email)
	// fmt.Println(email)
	if editingUserDb.Role == "admin" {
		tmpl := template.Must(template.ParseFiles("static/templates/fragments/usereditformadmin.gohtml"))
		tmpl.Execute(w, user)
		return
	}

	// Render the user edit form template
	tmpl := template.Must(template.ParseFiles("static/templates/fragments/usereditform.gohtml"))
	err = tmpl.Execute(w, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func (handler *Handler) AdminUserEdit(w http.ResponseWriter, r *http.Request) {
	users, err := handler.dao.GetAllUsers()
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/adminuseredit.gohtml"))
	tmpl.Execute(w, users)
}
func (handler *Handler) AdminUserEditRemove(w http.ResponseWriter, r *http.Request) {

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/adminusereditremove.gohtml"))
	tmpl.Execute(w, nil)
}
func (handler *Handler) AdminBetEdit(w http.ResponseWriter, r *http.Request) {
	betType := strings.TrimPrefix(r.URL.Path, "/adminbetedit/")

	bets, err := handler.dao.GetBetsByCategory(betType)
	if err != nil {
		fmt.Println("Bet by category " + betType + " Not found ")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type TemplateData struct {
		Category string
		Bets     []models.Bet
	}
	data := TemplateData{
		Category: betType,
		Bets:     *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/adminbetform.gohtml"))
	tmpl.Execute(w, data)
}
func (handler *Handler) AdminBetToggle(w http.ResponseWriter, r *http.Request) {
	betType := strings.TrimPrefix(r.URL.Path, "/adminbeteditdelete/")

	data := map[string]interface{}{
		"betType": betType,
	}
	tmpl := template.Must(template.ParseFiles("static/templates/fragments/adminbeteditdelete.gohtml"))
	tmpl.Execute(w, data)

}
func (handler *Handler) CancelUserEdit(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimPrefix(r.URL.Path, "/canceluseredit/")

	user, err := handler.dao.GetUserByEmail(email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl := template.Must(template.ParseFiles("static/templates/fragments/editeduser.gohtml"))
	tmpl.Execute(w, user)

}

// TESTS

func (handler *Handler) GetAllBets(w http.ResponseWriter, r *http.Request) {
	// Get all bets from the database
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
	bets, err := handler.dao.GetAllBets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}
	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/parlay.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) GetMatchBets(w http.ResponseWriter, r *http.Request) {
	// Get all bets from the database
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
	bets, err := handler.dao.GetBetsByCategory("matchup")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}
	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/matchbets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) GetFutureBets(w http.ResponseWriter, r *http.Request) {
	// Get all bets from the database
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
	bets, err := handler.dao.GetBetsByCategory("future")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}
	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/futurebets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func (handler *Handler) GetPropBets(w http.ResponseWriter, r *http.Request) {
	// Get all bets from the database
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
	bets, err := handler.dao.GetBetsByCategory("prop")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}
	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/propbets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) UserDashboard(w http.ResponseWriter, r *http.Request) {
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

	tmpl := template.Must(template.ParseFiles("static/templates/dashboard.gohtml"))
	if err := tmpl.Execute(w, *dbUser); err != nil {
		log.Println("Error executing template:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}
