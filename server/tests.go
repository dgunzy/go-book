package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/dgunzy/go-book/models"
)

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
	// if err != nil {
	// 	http.Error(w, "Get user bets failed", http.StatusInternalServerError)
	// 	return
	// }
}
