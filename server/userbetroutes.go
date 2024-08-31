package server

import (
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

func (handler *Handler) PlaceWager(w http.ResponseWriter, r *http.Request) {
	var dbUser *models.User
	var err error

	// Check if a user ID is provided in the query parameters
	userIDStr := r.URL.Query().Get("user_id")
	if userIDStr != "" {
		// If user ID is provided, fetch the user directly from the database
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			handler.respondWithMessage(w, "Invalid user ID")
			return
		}
		dbUser, err = handler.dao.GetUserByID(userID)
		if err != nil {
			handler.respondWithMessage(w, "Error retrieving user data")
			return
		}
	} else {
		// If no user ID is provided, get the current user from the session
		user, err := handler.auth.GetSessionUser(r)
		if err != nil {
			handler.respondWithMessage(w, "Error retrieving user session")
			return
		}
		dbUser, err = handler.dao.GetUserByEmail(user.Email)
		if err != nil {
			handler.respondWithMessage(w, "Error retrieving user data")
			return
		}
	}

	// Parse form data
	err = r.ParseForm()
	if err != nil {
		handler.respondWithMessage(w, "Error parsing form data")
		return
	}

	// Extract and validate form fields

	amount, err := strconv.ParseFloat(r.FormValue("wager_amount"), 64)
	if err != nil || amount <= 0 {
		handler.respondWithMessage(w, "Invalid wager amount")
		return
	}

	outcomeDescription := r.FormValue("outcome_description")
	if outcomeDescription == "" {
		handler.respondWithMessage(w, "Invalid outcome description")
		return
	}

	odds, err := strconv.ParseFloat(r.FormValue("odds"), 64)
	if err != nil {
		handler.respondWithMessage(w, "Invalid odds")
		return
	}

	var approvalState bool
	if dbUser.AutoApproveLimit <= int(amount) {
		approvalState = false
	} else {
		approvalState = true
	}

	// if dbUser.Balance < amount {
	// 	handler.respondWithMessage(w, fmt.Sprintf("Insufficient funds. Your balance: $%.2f, Wager amount: $%.2f", dbUser.Balance, amount))
	// 	return
	// }

	// Create a new UserBet
	userBet := &models.UserBet{
		Amount:         amount,
		PlacedAt:       time.Now(),
		Result:         "", // Not graded yet
		BetDescription: outcomeDescription,
		Odds:           odds,
		UserID:         dbUser.UserID,
		Approved:       approvalState,
	}

	// Insert the UserBet into the database
	err = handler.dao.PlaceBet(*userBet)
	if err != nil {
		log.Printf("Error creating user bet: %v", err)
		handler.respondWithMessage(w, "Error placing wager")
		return
	} else {
		// Create a transaction for the bet
		transaction := models.Transaction{
			Amount:          -amount,
			Type:            "debit",
			Description:     fmt.Sprintf("Wager placed on %s", outcomeDescription),
			TransactionDate: time.Now(),
		}
		_, err := handler.dao.CreateTransaction(*dbUser, transaction)
		if err != nil {
			log.Printf("Error creating transaction: %v", err)
			handler.respondWithMessage(w, "Error placing wager")
			return
		}
	}

	// Respond with a success message
	if approvalState {
		handler.respondWithMessage(w, fmt.Sprintf("Wager placed successfully! Amount: $%.2f, Outcome: %s, Auto approved.", amount, outcomeDescription))
		return

	} else {
		handler.respondWithMessage(w, fmt.Sprintf("Wager placed successfully! Amount: $%.2f, Outcome: %s Pending Approval.", amount, outcomeDescription))

	}

}

// Helper function to respond with a message
func (handler *Handler) respondWithMessage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "<p>%s</p>", message)
}

// betFlag is a string that can be either "pending" or "approved" or "all". All displays unapproved bets only.
func (handler *Handler) GetUserBets(w http.ResponseWriter, r *http.Request) {

	betFlag := strings.TrimPrefix(r.URL.Path, "/userbets/")
	// Get the current user from the session
	user, err := handler.auth.GetSessionUser(r)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user session")
		return
	}

	// Get the user from the database
	dbUser, err := handler.dao.GetUserByEmail(user.Email)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}

	// Get all user bets from the database
	userBets, err := handler.dao.GetAllUserBets()
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user bets")
		return
	}
	if userBets == nil {
		handler.respondWithMessage(w, "No bets found")
		return
	}
	betsToDisplay := []*models.UserBet{}
	// Filter the user bets based on the betFlag

	type PageData struct {
		UserBets []*models.UserBet
		User     *models.User
	}
	data := PageData{}

	if betFlag == "pending" {
		for _, bet := range userBets {
			if bet.UserID == dbUser.UserID && !bet.Approved {
				betsToDisplay = append(betsToDisplay, bet)
			}
		}
		data = PageData{
			UserBets: betsToDisplay,
			User:     dbUser,
		}
	}
	if betFlag == "approved" {
		for _, bet := range userBets {
			if bet.UserID == dbUser.UserID && bet.Approved {
				betsToDisplay = append(betsToDisplay, bet)
			}
		}
		data = PageData{
			UserBets: betsToDisplay,
			User:     dbUser,
		}
	}

	if betFlag == "all" {
		for _, bet := range userBets {
			if !bet.Approved {
				betsToDisplay = append(betsToDisplay, bet)
			}
		}
		data = PageData{
			UserBets: betsToDisplay,
			User:     dbUser,
		}

	}

	if betFlag == "allgrade" {

		if dbUser.Role != "admin" && dbUser.Role != "root" {
			handler.respondWithMessage(w, "You do not have permission to view this page")
			return
		}
		for _, bet := range userBets {
			if bet.Result == "ungraded" {
				betsToDisplay = append(betsToDisplay, bet)
			}
		}
		data = PageData{
			UserBets: betsToDisplay,
			User:     dbUser,
		}
		tmpl := template.Must(template.ParseFiles("static/templates/fragments/userbetgrade.gohtml"))
		err = tmpl.Execute(w, data)
		if err != nil {
			fmt.Println("Error executing template:", err)
		}
		return

	}
	if data.User == nil {
		handler.respondWithMessage(w, "Internal Server Error")
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/userbets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println("Error executing template:", err)
		return
	}

}

// DeleteBet handles the deletion of a bet
func (handler *Handler) DeleteUserBet(w http.ResponseWriter, r *http.Request) {
	// Extract betID from query parameters
	betIDStr := strings.TrimPrefix(r.URL.Path, "/delete-user-bet/")
	if betIDStr == "" {
		handler.respondWithMessage(w, "Missing bet ID")
		return
	}

	betID, err := strconv.Atoi(betIDStr)
	if err != nil {
		handler.respondWithMessage(w, "Invalid bet ID")
		return
	}

	// Get the bet from the database
	gradedBet, err := handler.dao.GetUserBetID(betID)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving bet data")
		return
	}

	dbUser, err := handler.dao.GetUserByID(gradedBet.UserID)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}

	transaction := models.Transaction{
		Amount:          gradedBet.Amount,
		Type:            "credit",
		Description:     fmt.Sprintf("Refund on bet %s", gradedBet.BetDescription),
		TransactionDate: time.Now(),
	}
	_, err = handler.dao.CreateTransaction(*dbUser, transaction)
	if err != nil {
		log.Printf("Error creating transaction: %v", err)
		handler.respondWithMessage(w, "Error placing wager")
		return
	}

	// Delete the bet
	err = handler.dao.DeleteUserBetByID(betID)
	if err != nil {
		handler.respondWithMessage(w, fmt.Sprintf("Error deleting bet: %v", err))
		return
	}

	handler.respondWithMessage(w, fmt.Sprintf("Bet with ID %d deleted successfully", betID))
}

// ApproveBet handles the approval of a bet
func (handler *Handler) ApproveUserBet(w http.ResponseWriter, r *http.Request) {
	// Extract betID from query parameters
	betIDStr := strings.TrimPrefix(r.URL.Path, "/approve-user-bet/")
	if betIDStr == "" {
		handler.respondWithMessage(w, "Missing bet ID")
		return
	}

	betID, err := strconv.Atoi(betIDStr)
	if err != nil {
		handler.respondWithMessage(w, "Invalid bet ID")
		return
	}

	// Approve the bet
	err = handler.dao.ApproveUserBet(betID)
	if err != nil {
		handler.respondWithMessage(w, fmt.Sprintf("Error approving bet: %v", err))
		return
	}

	handler.respondWithMessage(w, fmt.Sprintf("Bet with ID %d approved successfully", betID))
}

// GradeBet handles the grading of a bet
func (handler *Handler) GradeUserBet(w http.ResponseWriter, r *http.Request) {

	path := strings.TrimPrefix(r.URL.Path, "/grade-user-bet/")
	parts := strings.Split(path, "/")

	if len(parts) != 2 {
		handler.respondWithMessage(w, "Invalid URL format")
		return
	}

	betIDStr := parts[0]
	result := parts[1]

	if betIDStr == "" || result == "" {
		handler.respondWithMessage(w, "Missing bet ID or result")
		return
	}

	betID, err := strconv.Atoi(betIDStr)
	if err != nil {
		handler.respondWithMessage(w, "Invalid bet ID")
		return
	}

	if result != "win" && result != "lose" {
		handler.respondWithMessage(w, "Invalid result: must be 'win' or 'lose'")
		return
	}

	// Grade the bet
	gradedBet, err := handler.dao.GradeUserBet(betID, result)
	if err != nil {
		handler.respondWithMessage(w, fmt.Sprintf("Error grading bet: %v", err))
		return
	}

	dbUser, err := handler.dao.GetUserByID(gradedBet.UserID)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}

	if result == "win" {
		decimalOdds := utils.AmericanToDecimal(int(gradedBet.Odds))
		result := gradedBet.Amount * decimalOdds
		roundedResult := math.Round(result*100) / 100
		transaction := models.Transaction{
			Amount:          roundedResult,
			Type:            "credit",
			Description:     fmt.Sprintf("Won bet on %s", gradedBet.BetDescription),
			TransactionDate: time.Now(),
		}
		_, err := handler.dao.CreateTransaction(*dbUser, transaction)
		if err != nil {
			log.Printf("Error creating transaction: %v", err)
			handler.respondWithMessage(w, "Error placing wager")
			return
		}

	}

	handler.respondWithMessage(w, fmt.Sprintf("Bet with ID %d graded as %s successfully", betID, result))
}

func (handler *Handler) PlaceWagerForUser(w http.ResponseWriter, r *http.Request) {
	// Parse form data
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

	// Fetch the user from the database
	dbUser, err := handler.dao.GetUserByID(userID)
	if err != nil {
		handler.respondWithMessage(w, "Error retrieving user data")
		return
	}

	outcomeDescription := r.FormValue("outcome_description")
	if outcomeDescription == "" {
		handler.respondWithMessage(w, "Invalid outcome description")
		return
	}

	odds, err := strconv.ParseFloat(r.FormValue("odds"), 64)
	if err != nil {
		handler.respondWithMessage(w, "Invalid odds")
		return
	}

	amount, err := strconv.ParseFloat(r.FormValue("wager_amount"), 64)
	if err != nil || amount <= 0 {
		handler.respondWithMessage(w, "Invalid wager amount")
		return
	}
	// if amount > dbUser.Balance {
	// 	handler.respondWithMessage(w, fmt.Sprintf("Insufficient funds. User balance: $%.2f, Wager amount: $%.2f", dbUser.Balance, amount))
	// 	return
	// }

	// Create a new UserBet
	userBet := &models.UserBet{
		Amount:         amount,
		PlacedAt:       time.Now(),
		Result:         "ungraded", // Not graded yet
		BetDescription: outcomeDescription,
		Odds:           odds,
		UserID:         dbUser.UserID,
		Approved:       true, // Auto-approve admin-placed bets

	}

	// Insert the UserBet into the database
	err = handler.dao.PlaceBet(*userBet)
	if err != nil {
		log.Printf("Error creating user bet: %v", err)
		handler.respondWithMessage(w, "Error placing wager")
		return
	}

	// Create a transaction for the bet
	transaction := models.Transaction{
		Amount:          -amount,
		Type:            "debit",
		Description:     fmt.Sprintf("Admin placed wager on %s", outcomeDescription),
		TransactionDate: time.Now(),
	}
	_, err = handler.dao.CreateTransaction(*dbUser, transaction)
	if err != nil {
		log.Printf("Error creating transaction: %v", err)
		handler.respondWithMessage(w, "Error recording transaction")
		return
	}

	// Respond with a success message
	handler.respondWithMessage(w, fmt.Sprintf("Wager placed successfully for user %s! Amount: $%.2f, Outcome: %s", dbUser.Username, amount, outcomeDescription))
}
