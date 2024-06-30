package server

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

func (handler *Handler) ReadBet(w http.ResponseWriter, r *http.Request) {
	// Extract the betID from the URL
	betID := strings.TrimPrefix(r.URL.Path, "/bet/")
	betIDInt, err := strconv.Atoi(betID)
	if err != nil {
		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
		return
	}

	// Read the bet from the database
	bet, err := handler.dao.ReadBet(betIDInt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Render the bet details template
	tmpl := template.Must(template.ParseFiles("static/templates/betdetails.gohtml"))
	err = tmpl.Execute(w, bet)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// func (handler *Handler) DeleteBet(w http.ResponseWriter, r *http.Request) {
// 	// Extract the betID from the URL
// 	betID := strings.TrimPrefix(r.URL.Path, "/bet/")
// 	betIDInt, err := strconv.Atoi(betID)
// 	if err != nil {
// 		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
// 		return
// 	}

// 	// Delete the bet from the database
// 	err = handler.dao.DeleteBet(betIDInt)
// 	if err != nil {
// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Redirect to the bets list page or return a success response
// 	// ...
// }

// func (handler *Handler) CreateBet(w http.ResponseWriter, r *http.Request) {
// 	// Parse the form values
// 	err := r.ParseForm()
// 	if err != nil {
// 		http.Error(w, "Invalid form data", http.StatusBadRequest)
// 		return
// 	}
// 	user, err := handler.auth.GetSessionUser(r)
// 	if err != nil {
// 		log.Println(err)
// 		return
// 	}
// 	dbUser, err := handler.dao.GetUserByEmail(user.Email)
// 	if err != nil {
// 		log.Println(err)
// 		return
// 	}

// 	expiryTimeStr := r.FormValue("expiry_time")
// 	expiryTime, err := utils.UIToGo(expiryTimeStr)
// 	if err != nil {
// 		http.Error(w, "Invalid expiry time format", http.StatusBadRequest)
// 		return
// 	}
// 	createdAt := time.Now()
// 	// Create a new Bet struct
// 	bet := &models.Bet{
// 		Title:          r.FormValue("title"),
// 		Description:    r.FormValue("description"),
// 		OddsMultiplier: parseFloat(r.FormValue("odds_multiplier")),
// 		Status:         r.FormValue("status"),
// 		Category:       r.FormValue("category"),
// 		CreatedBy:      dbUser.UserID,
// 		CreatedAt:      createdAt,
// 		ExpiryTime:     expiryTime,
// 	}

// 	// Create a slice of BetOutcome structs
// 	var outcomes []*models.BetOutcome
// 	outcomeDescriptions := r.Form["outcome_description"]
// 	outcomeOdds := r.Form["outcome_odds"]

// 	// Parse the outcomes from the form data
// 	for i := range outcomeDescriptions {
// 		description := outcomeDescriptions[i]
// 		odds := parseFloat(outcomeOdds[i])
// 		outcome := &models.BetOutcome{
// 			Description: description,
// 			Odds:        odds,
// 		}
// 		outcomes = append(outcomes, outcome)
// 	}

// 	// Create the bet in the database
// 	_, err = handler.dao.CreateBet(bet, outcomes)
// 	if err != nil {
// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Redirect to a success page or return a success response
// 	// ...
// }

// func (handler *Handler) UpdateBet(w http.ResponseWriter, r *http.Request) {
// 	// Extract the betID from the URL
// 	betID := strings.TrimPrefix(r.URL.Path, "/bet/")
// 	betIDInt, err := strconv.Atoi(betID)
// 	if err != nil {
// 		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
// 		return
// 	}

// 	// Parse the form values
// 	err = r.ParseForm()
// 	if err != nil {
// 		http.Error(w, "Invalid form data", http.StatusBadRequest)
// 		return
// 	}

// 	// Create a map to store the updates
// 	updates := make(map[string]interface{})
// 	// Check for each possible field to update
// 	// ...

// 	// Create a slice of BetOutcome structs
// 	var outcomes []*models.BetOutcome
// 	// Parse the outcomes from the form data
// 	// ...

// 	// Update the bet in the database
// 	err = handler.dao.UpdateBet(betIDInt, updates, outcomes)
// 	if err != nil {
// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Redirect to the bet details page or return a success response
// 	// ...
// }
