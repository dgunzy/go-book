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

	// Extract form fields
	title := r.FormValue("Title")
	description := r.FormValue("Description")
	status := r.FormValue("Status")
	category := r.FormValue("Category")
	expiryTime := r.FormValue("ExpiryTime")
	outcomeDescriptions := r.Form["OutcomeDescription[]"]
	odds := r.Form["Odds[]"]
	bannableUsers := r.Form["bannableUsers[]"]

	// Convert odds from []string to []float64 (decimal odds)
	var oddsFloat []float64
	for _, odd := range odds {
		decimalOdds, err := utils.AmericanStringToDecimal(odd)
		if err != nil {
			fmt.Println("Error converting odds to decimal:", err)
			http.Error(w, "Invalid odds format", http.StatusBadRequest)
			return
		}
		oddsFloat = append(oddsFloat, decimalOdds)
	}

	// Convert bannableUsers from []string to []int
	var bannableUsersInt []int
	for _, user := range bannableUsers {
		userID, err := strconv.Atoi(user)
		if err != nil {
			fmt.Println("Error converting user ID to int:", err)
			http.Error(w, "Invalid user ID format", http.StatusBadRequest)
			return
		}
		bannableUsersInt = append(bannableUsersInt, userID)
	}

	dbReadyTime, err := utils.UIToGo(expiryTime)
	if err != nil {
		fmt.Println("Error converting expiry time to time.Time:", err)
		http.Error(w, "Invalid expiry time format", http.StatusBadRequest)
		return
	}

	// Now oddsFloat (in decimal format) and bannableUsersInt are ready for database operations
	fmt.Println("Title:", title)
	fmt.Println("Description:", description)
	fmt.Println("Status:", status)
	fmt.Println("Category:", category)
	fmt.Println("ExpiryTime:", dbReadyTime)
	fmt.Println("OutcomeDescriptions:", outcomeDescriptions)
	fmt.Println("Odds after conversion to decimal:", oddsFloat)
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

func (handler *Handler) MoveBetToClosed(w http.ResponseWriter, r *http.Request) {
	// Extract the betID from the URL
	betID := strings.TrimPrefix(r.URL.Path, "/deletebet/")
	betIDInt, err := strconv.Atoi(betID)
	if err != nil {
		http.Error(w, "Invalid bet ID", http.StatusBadRequest)
		return
	}

	// Read the bet from the database
	err = handler.dao.DeleteBet(betIDInt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to the bet details page
	http.Redirect(w, r, "/admindashboard", http.StatusSeeOther)
}

func (handler *Handler) GetNewBetPage(w http.ResponseWriter, r *http.Request) {
	_, err := handler.auth.GetSessionUser(r)
	if err != nil {
		log.Println(err)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/betcreate.gohtml"))
	if err := tmpl.Execute(w, nil); err != nil {
		log.Println("Error executing template:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
	betCategory := "prop"
	bets, err := handler.dao.GetAllLegalBetsByCategory(&betCategory, dbUser.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range *bets {
		for j := range (*bets)[i].BetOutcomes {
			decimalOdds := (*bets)[i].BetOutcomes[j].Odds
			americanOdds := utils.DecimalToAmerican(decimalOdds)
			(*bets)[i].BetOutcomes[j].Odds = float64(americanOdds)
		}
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}

	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/propbets.gohtml"))
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
	betCategory := "future"
	bets, err := handler.dao.GetAllLegalBetsByCategory(&betCategory, dbUser.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range *bets {
		for j := range (*bets)[i].BetOutcomes {
			decimalOdds := (*bets)[i].BetOutcomes[j].Odds
			americanOdds := utils.DecimalToAmerican(decimalOdds)
			(*bets)[i].BetOutcomes[j].Odds = float64(americanOdds)
		}
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}

	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/futurebets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (handler *Handler) GetMatchBets(w http.ResponseWriter, r *http.Request) {
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

	betCategory := "matchup"
	bets, err := handler.dao.GetAllLegalBetsByCategory(&betCategory, dbUser.UserID)
	if err != nil {
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
		User *models.User
		Bets []models.Bet
	}

	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/matchbets.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	bets, err := handler.dao.GetAllLegalBetsByCategory(nil, dbUser.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range *bets {
		for j := range (*bets)[i].BetOutcomes {
			decimalOdds := (*bets)[i].BetOutcomes[j].Odds
			americanOdds := utils.DecimalToAmerican(decimalOdds)
			(*bets)[i].BetOutcomes[j].Odds = float64(americanOdds)
		}
	}

	type TemplateData struct {
		User *models.User
		Bets []models.Bet
	}

	data := TemplateData{
		User: dbUser,
		Bets: *bets,
	}

	tmpl := template.Must(template.ParseFiles("static/templates/parlay.gohtml"))
	err = tmpl.Execute(w, data)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
