package server

import (
	"fmt"
	"net/http"
)

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
