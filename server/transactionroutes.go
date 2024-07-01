package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/dgunzy/go-book/models"
)

func (handler *Handler) AdminTransactionEdit(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimPrefix(r.URL.Path, "/createUserTransaction/")

	dbUser, err := handler.dao.GetUserByEmail(email)

	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/adminusertransactionform.gohtml"))
	tmpl.Execute(w, dbUser)
}
func (handler *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	// Parse the form values
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	UserID := parseInt(r.FormValue("user_id"))
	// Get the user from the database
	user, err := handler.dao.GetUserByID(UserID)
	if err != nil {
		fmt.Println("error getting user by id ")
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	transactionAmount := parseFloat(r.FormValue("amount"))
	if transactionAmount < 0 {
		fmt.Println("error transaction amount is less than 0")
		http.Error(w, "Transaction amount is less than 0", http.StatusBadRequest)
		return
	}

	if r.FormValue("type") == "debit" {
		// Create a new debit number
		transactionAmount = transactionAmount * -1
	}
	// Create a new Transaction struct
	transaction := models.Transaction{
		Amount:          transactionAmount,
		Type:            r.FormValue("type"),
		Description:     r.FormValue("description"),
		TransactionDate: time.Now(),
	}

	// Create the transaction in the database
	userId, err := handler.dao.CreateTransaction(*user, transaction)
	if err != nil {
		fmt.Println("error creating transactions")
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dbUser, err := handler.dao.GetUserByID(int(userId))
	if err != nil {
		fmt.Println("error getting user by id ")
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("static/templates/fragments/editeduser.gohtml"))
	err = tmpl.Execute(w, dbUser)
	if err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// func (handler *Handler) ReadTransaction(w http.ResponseWriter, r *http.Request) {
// 	// Extract the transactionID from the URL
// 	transactionID := strings.TrimPrefix(r.URL.Path, "/transaction/")
// 	transactionIDInt, err := strconv.Atoi(transactionID)
// 	if err != nil {
// 		http.Error(w, "Invalid transaction ID", http.StatusBadRequest)
// 		return
// 	}

// 	// Read the transaction from the database
// 	transaction, err := handler.dao.ReadUserTransactions(transactionIDInt)
// 	if err != nil {
// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Render the transaction details template
// 	tmpl := template.Must(template.ParseFiles("static/templates/transactiondetails.gohtml"))
// 	err = tmpl.Execute(w, transaction)
// 	if err != nil {
// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// }
