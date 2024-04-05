package server

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/models"
)

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
