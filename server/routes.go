package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
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

func (handler *Handler) ApplicationOffline(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/applicationoffline.gohtml"))
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
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
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
		fmt.Println("error getting db user in navbar ", err)
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

	// fmt.Println("Navbar role: ", dbUser.Role)
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

func (handler *Handler) ApplicationStatus(w http.ResponseWriter, r *http.Request) {

	type Data struct {
		ApplicationOnline bool
	}

	data := Data{
		ApplicationOnline: auth.IsApplicationOnline(),
	}
	template := template.Must(template.ParseFiles("static/templates/fragments/appstatus.gohtml"))
	err := template.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func (handler *Handler) ToggleApplicationState(w http.ResponseWriter, r *http.Request) {
	if auth.IsApplicationOnline() {
		auth.SetApplicationOffline()
	} else {
		auth.SetApplicationOnline()

	}
	http.Redirect(w, r, "/appstatus", http.StatusFound)
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

	// Convert odds to American format
	for i := range *bets {
		for j := range (*bets)[i].BetOutcomes {
			decimalOdds := (*bets)[i].BetOutcomes[j].Odds
			americanOdds := utils.DecimalToAmerican(decimalOdds)
			(*bets)[i].BetOutcomes[j].Odds = float64(americanOdds)
		}
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

func (handler *Handler) CancelViewBannableUser(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/fragments/bannableuserbutton.gohtml"))
	tmpl.Execute(w, nil)
}
