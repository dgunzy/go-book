package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/dgunzy/go-book/models"
)

func (handler *Handler) GetUserInfoForm(w http.ResponseWriter, r *http.Request) {
	// Get the current user from the session
	user, err := handler.auth.GetSessionUser(r)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user session")
		return
	}

	fmt.Println("User info form hit")
	// Get the user from the database
	dbUser, err := handler.dao.GetUserByEmail(user.Email)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}
	if dbUser.Role == "user" {
		handler.respondWithMessage(w, "You do not have permission to view this page")
		return

	}
	allUsers, err := handler.dao.GetAllUsers()
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}

	type TemplateData struct {
		Users []*models.User
	}

	data := TemplateData{
		Users: allUsers,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/userinfoform.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
	}

}
func (handler *Handler) GetUserBetsTransactions(w http.ResponseWriter, r *http.Request) {
	// Get the current user from the session
	err := r.ParseForm()
	if err != nil {
		handler.respondWithMessage(w, "Error parsing form data")
		return
	}

	// Extract and validate form fields
	userIDStr := r.FormValue("user_id")
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		handler.respondWithMessage(w, "Invalid user ID")
		return
	}

	dbUser, err := handler.dao.GetUserByID(userID)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}

	userTransactions := []models.Transaction{}
	err = handler.dao.ReadUserTransactions(dbUser.UserID, &userTransactions)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user transactions")
		return
	}

	userBets, err := handler.dao.GetAllUserGradedBets(dbUser.UserID)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user bets")
		return
	}

	type TemplateData struct {
		User         models.User
		Transactions []models.Transaction
		UserBets     []*models.UserBet
	}

	data := TemplateData{
		User:         *dbUser,
		Transactions: userTransactions,
		UserBets:     userBets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/userinfodisplay.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
	}
}
