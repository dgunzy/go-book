package server

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/dgunzy/go-book/models"
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
	betID, err := strconv.Atoi(r.FormValue("bet_id"))
	if err != nil {
		handler.respondWithMessage(w, "Invalid BetID")
		return
	}

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

	if dbUser.Balance < amount {
		handler.respondWithMessage(w, fmt.Sprintf("Insufficient funds. Your balance: $%.2f, Wager amount: $%.2f", dbUser.Balance, amount))
		return
	}

	// Create a new UserBet
	userBet := &models.UserBet{
		Amount:         amount,
		PlacedAt:       time.Now(),
		Result:         "", // Not graded yet
		BetDescription: outcomeDescription,
		Odds:           odds,
		BetId:          betID,
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
