package server

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/models"
)

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
