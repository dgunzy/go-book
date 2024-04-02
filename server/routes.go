package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	type userEditingStruct struct {
		User     *models.User
		AllUsers []*models.User
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
	userEditing := userEditingStruct{
		User:     dbUser,
		AllUsers: allUsers,
	}

	template := template.Must(template.ParseFiles("static/templates/useredit.gohtml"))
	err = template.Execute(w, userEditing)
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
	w.Header().Set("Cache-Control", "no-store")
	// fmt.Println(email)

	// Get the user from the database
	user, err := handler.dao.GetUserByEmail(email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
func (handler *Handler) CreateBet(w http.ResponseWriter, r *http.Request) {
	// Parse the form values
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
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

	// Create a new Bet struct
	bet := &models.Bet{
		Title:          r.FormValue("title"),
		Description:    r.FormValue("description"),
		OddsMultiplier: parseFloat(r.FormValue("odds_multiplier")),
		Status:         r.FormValue("status"),
		Category:       r.FormValue("category"),
		CreatedBy:      dbUser.UserID,
		CreatedAt:      time.Now().String(),
	}

	// Create a slice of BetOutcome structs
	var outcomes []*models.BetOutcome
	outcomeDescriptions := r.Form["outcome_description"]
	outcomeOdds := r.Form["outcome_odds"]

	// Parse the outcomes from the form data
	for i := range outcomeDescriptions {
		description := outcomeDescriptions[i]
		odds := parseFloat(outcomeOdds[i])
		outcome := &models.BetOutcome{
			Description: description,
			Odds:        odds,
		}
		outcomes = append(outcomes, outcome)
	}

	// Create the bet in the database
	_, err = handler.dao.CreateBet(bet, outcomes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to a success page or return a success response
	// ...
}
func (handler *Handler) ReadBet(w http.ResponseWriter, r *http.Request) {
	// Extract the betID from the URL
	betID := strings.TrimPrefix(r.URL.Path, "/bet/")
	betIDInt, err := strconv.Atoi(betID)
	if err != nil {
		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
		return
	}

	// Read the bet from the database
	bet, outcomes, err := handler.dao.ReadBet(betIDInt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Render the bet details template
	tmpl := template.Must(template.ParseFiles("static/templates/betdetails.gohtml"))
	err = tmpl.Execute(w, map[string]interface{}{
		"Bet":      bet,
		"Outcomes": outcomes,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func (handler *Handler) UpdateBet(w http.ResponseWriter, r *http.Request) {
	// Extract the betID from the URL
	betID := strings.TrimPrefix(r.URL.Path, "/bet/")
	betIDInt, err := strconv.Atoi(betID)
	if err != nil {
		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
		return
	}

	// Parse the form values
	err = r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Create a map to store the updates
	updates := make(map[string]interface{})
	// Check for each possible field to update
	// ...

	// Create a slice of BetOutcome structs
	var outcomes []*models.BetOutcome
	// Parse the outcomes from the form data
	// ...

	// Update the bet in the database
	err = handler.dao.UpdateBet(betIDInt, updates, outcomes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to the bet details page or return a success response
	// ...
}
func (handler *Handler) DeleteBet(w http.ResponseWriter, r *http.Request) {
	// Extract the betID from the URL
	betID := strings.TrimPrefix(r.URL.Path, "/bet/")
	betIDInt, err := strconv.Atoi(betID)
	if err != nil {
		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
		return
	}

	// Delete the bet from the database
	err = handler.dao.DeleteBet(betIDInt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to the bets list page or return a success response
	// ...
}
func (handler *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	// Parse the form values
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Create a new Transaction struct
	transaction := &models.Transaction{
		UserID:      parseInt(r.FormValue("user_id")),
		Amount:      parseFloat(r.FormValue("amount")),
		Type:        r.FormValue("type"),
		Description: r.FormValue("description"),
	}

	// Create the transaction in the database
	_, err = handler.dao.CreateTransaction(transaction)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to a success page or return a success response
	// ...
}
func (handler *Handler) ReadTransaction(w http.ResponseWriter, r *http.Request) {
	// Extract the transactionID from the URL
	transactionID := strings.TrimPrefix(r.URL.Path, "/transaction/")
	transactionIDInt, err := strconv.Atoi(transactionID)
	if err != nil {
		http.Error(w, "Invalid transaction ID", http.StatusBadRequest)
		return
	}

	// Read the transaction from the database
	transaction, err := handler.dao.ReadTransaction(transactionIDInt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Render the transaction details template
	tmpl := template.Must(template.ParseFiles("static/templates/transactiondetails.gohtml"))
	err = tmpl.Execute(w, transaction)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) GetUserBets(w http.ResponseWriter, r *http.Request) {
	// Extract the userID from the URL
	email := strings.TrimPrefix(r.URL.Path, "/user/")

	// Get the user's bets from the database
	userBets, err := handler.dao.GetUserBets(email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Render the user bets template
	tmpl := template.Must(template.ParseFiles("static/templates/userbets.gohtml"))
	err = tmpl.Execute(w, userBets)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) PlaceBet(w http.ResponseWriter, r *http.Request) {
	// Parse the form values
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Create a new UserBet struct
	userBet := &models.UserBet{
		UserID:    parseInt(r.FormValue("user_id")),
		BetID:     parseInt(r.FormValue("bet_id")),
		OutcomeID: parseInt(r.FormValue("outcome_id")),
		Amount:    parseFloat(r.FormValue("amount")),
	}

	// Place the bet in the database
	err = handler.dao.PlaceBet(userBet)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to the user's bets page or return a success response
	http.Redirect(w, r, "/user/"+strconv.Itoa(userBet.UserID)+"/bets", http.StatusSeeOther)
}

// TESTS

func (handler *Handler) RunUnitTests(w http.ResponseWriter, r *http.Request) {
	// Create a new user for testing

	user := &models.User{
		Username:         "testuser",
		Email:            "test" + time.Now().String() + " @example.com",
		Role:             "user",
		Balance:          1000,
		FreePlayBalance:  0,
		AutoApproveLimit: 69,
	}
	err := handler.dao.CreateUser(user)
	if err != nil {
		http.Error(w, "Failed to create test user", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Created test user: %+v\n", user)

	// Get the created user by ID
	createdUser, err := handler.dao.GetUserByEmail(user.Email)
	if err != nil {
		http.Error(w, "Failed to get test user by Email", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Retrieved test user by email: %+v\n", createdUser)

	// Create a new bet for testing
	bet := &models.Bet{
		Title:          "Test Bet",
		Description:    "This is a test bet",
		OddsMultiplier: 1.5,
		Status:         "open",
		CreatedBy:      createdUser.UserID,
	}
	outcomes := []*models.BetOutcome{
		{Description: "Outcome 1", Odds: 2.0},
		{Description: "Outcome 2", Odds: 1.8},
	}
	createdBetId, err := handler.dao.CreateBet(bet, outcomes)
	if err != nil {
		http.Error(w, "Failed to create test bet", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Created test bet: %+v\n", bet)

	// Get the created bet by ID
	fmt.Println("Created bet ID:", createdBetId)
	createdBet, createdOutcomes, err := handler.dao.ReadBet(int(createdBetId))
	if err != nil {
		http.Error(w, "Failed to get test bet by ID", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Retrieved test bet by ID: %+v\n", createdBet)
	for _, outcome := range createdOutcomes {
		fmt.Printf("Outcome: %+v\n", outcome)
	}

	// Place a bet for testing
	userBet := &models.UserBet{
		UserID:    createdUser.UserID,
		BetID:     int(createdBetId),
		OutcomeID: createdOutcomes[0].OutcomeID,
		Amount:    100,
		Result:    "ungraded",
	}
	err = handler.dao.PlaceBet(userBet)
	if err != nil {
		http.Error(w, "Failed to place test bet", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Placed test user bet: %+v\n", userBet)

	// Get the user's bets
	userBets, err := handler.dao.GetUserBets(createdUser.Email)
	if err != nil {
		http.Error(w, "Failed to get user bets", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Retrieved user bets: %+v\n", userBets)
	fmt.Println("User bet result:", userBets[0].Result)

	// Grade the bet for testing
	err = handler.dao.GradeBet(createdBet.BetID, createdOutcomes[0].OutcomeID)
	if err != nil {
		http.Error(w, "Failed to grade test bet", http.StatusInternalServerError)
		return
	}
	fmt.Println("Graded test bet successfully")

	// Create a transaction for testing
	transaction := &models.Transaction{
		UserID:      createdUser.UserID,
		Amount:      200,
		Type:        "deposit",
		Description: "Test transaction",
	}
	createdTransactionId, err := handler.dao.CreateTransaction(transaction)
	if err != nil {
		http.Error(w, "Failed to create test transaction", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Created test transaction: %+v\n", transaction)
	fmt.Println("Created transaction ID:", createdTransactionId)

	// Read the transaction for testing
	readTransaction, err := handler.dao.ReadTransaction(int(createdTransactionId))
	if err != nil {
		fmt.Println(err)
		http.Error(w, "Failed to read test transaction", http.StatusInternalServerError)
		return
	}
	fmt.Printf("Retrieved test transaction: %+v\n", readTransaction)

	// Verify the transaction details
	if readTransaction.UserID != transaction.UserID ||
		readTransaction.Amount != transaction.Amount ||
		readTransaction.Type != transaction.Type ||
		readTransaction.Description != transaction.Description {
		http.Error(w, "Transaction details do not match", http.StatusInternalServerError)
		return
	}

	// Return a success response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Unit tests passed successfully"))
}

func (handler *Handler) RunGetUserBetTest(w http.ResponseWriter, r *http.Request) {
	// Extract the userID from the URL
	email := "test2024-03-30 09:11:31.874377 -0300 ADT m=+3.118126918 @example.com"

	// Get the user's bets from the database
	userBets, err := handler.dao.GetUserBets(email)

	// Print the user bets
	fmt.Println(userBets[0])
	fmt.Println(userBets[1])
	if err != nil {
		fmt.Println(userBets)
		return
	}

	// Render the user bets template
	if err != nil {
		http.Error(w, "Get user bets failed", http.StatusInternalServerError)
		return
	}
}

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
		Bets map[*models.Bet][]*models.BetOutcome
	}
	data := TemplateData{
		User: dbUser,
		Bets: bets,
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
		Bets map[*models.Bet][]*models.BetOutcome
	}
	data := TemplateData{
		User: dbUser,
		Bets: bets,
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
		Bets map[*models.Bet][]*models.BetOutcome
	}
	data := TemplateData{
		User: dbUser,
		Bets: bets,
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
		Bets map[*models.Bet][]*models.BetOutcome
	}
	data := TemplateData{
		User: dbUser,
		Bets: bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/matchbets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
