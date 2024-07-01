package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
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

func (handler *Handler) CreateNewBet(w http.ResponseWriter, r *http.Request) {
	// Parse the form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Extract and print form fields
	title := r.FormValue("Title")
	description := r.FormValue("Description")
	status := r.FormValue("Status")
	category := r.FormValue("Category")
	expiryTime := r.FormValue("ExpiryTime")
	outcomeDescriptions := r.Form["OutcomeDescription[]"]
	odds := r.Form["Odds[]"]
	bannableUsers := r.Form["bannableUsers[]"]

	// Convert odds from []string to []float64
	var oddsFloat []float64
	for _, odd := range odds {
		oddFloat, err := strconv.ParseFloat(odd, 64)
		if err != nil {
			// handle error, maybe log it or return an error response
			fmt.Println("Error converting odds to float:", err)
			return
		}
		oddsFloat = append(oddsFloat, oddFloat)
	}

	// Convert bannableUsers from []string to []int
	var bannableUsersInt []int
	for _, user := range bannableUsers {
		userID, err := strconv.Atoi(user)
		if err != nil {
			// handle error, maybe log it or return an error response
			fmt.Println("Error converting user ID to int:", err)
			return
		}
		bannableUsersInt = append(bannableUsersInt, userID)
	}

	dbReadyTime, err := utils.UIToGo(expiryTime)
	if err != nil {
		fmt.Println("Error converting expiry time to time.Time:", err)
		return
	}
	// Now oddsFloat and bannableUsersInt are ready for database operations

	fmt.Println("Title:", title)
	fmt.Println("Description:", description)
	fmt.Println("Status:", status)
	fmt.Println("Category:", category)
	fmt.Println("ExpiryTime:", dbReadyTime)
	fmt.Println("OutcomeDescriptions:", outcomeDescriptions)
	fmt.Println("Odds after conversion:", oddsFloat)
	fmt.Println("BannableUsers after conversion:", bannableUsersInt)

	outcomes := make([]models.BetOutcomes, len(outcomeDescriptions))
	for i, description := range outcomeDescriptions {
		outcomes[i] = models.BetOutcomes{
			Description: description,
			Odds:        oddsFloat[i],
		}
	}

	// Create the bet in the database
	BetToInsert := models.Bet{
		Title:          title,
		Description:    description,
		OddsMultiplier: 1,
		Status:         status,
		Category:       category,
		CreatedBy:      1,
		ExpiryTime:     dbReadyTime,
		CreatedAt:      time.Now(),
		BetOutcomes:    outcomes,
	}

	err = nil
	_, err = handler.dao.CreateBet(&BetToInsert, bannableUsersInt)

	var Message string

	if err != nil {
		fmt.Println(err)
		Message = err.Error()
	} else {
		Message = "Bet created successfully"
	}
	type TemplateData struct {
		Message string
	}

	// Create an instance of the struct with the message
	data := TemplateData{
		Message: Message,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/createbetbutton.gohtml"))
	_ = tmpl.Execute(w, data)
}

func (handler *Handler) GetBannableUsers(w http.ResponseWriter, r *http.Request) {
	// Extract the betID from the URL
	users, err := handler.dao.GetAllUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type userForm struct {
		UserID   int
		Username string
		Email    string
	}

	var userForms []userForm
	for _, user := range users {
		userForms = append(userForms, userForm{
			UserID:   user.UserID,
			Username: user.Username,
			Email:    user.Email,
		})
	}

	// Render the bet details template
	tmpl := template.Must(template.ParseFiles("static/templates/fragments/bannableusers.gohtml"))
	_ = tmpl.Execute(w, userForms)
}
