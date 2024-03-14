package routes

import (
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/models"
)

type UserHandler struct {
	db      *sql.DB
	userDAO *dao.UserDAO
}

func NewUserHandler(db *sql.DB, userDAO *dao.UserDAO) *UserHandler {
	return &UserHandler{db: db, userDAO: userDAO}
}

func HomeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/index.gohtml"))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	// ... (same as before)
}

func (h *UserHandler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	// ... Authenticate the user ...
	user := &models.User{
		UserID:   1,
		Username: "john",
		Role:     "user",
	}

	token, err := auth.GenerateToken(user)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	err = h.userDAO.UpdateUserToken(user.UserID, token)
	if err != nil {
		http.Error(w, "Failed to store token", http.StatusInternalServerError)
		return
	}

	// Send the token to the client
	response := struct {
		Token string `json:"token"`
	}{
		Token: token,
	}

	jsonResponse, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Failed to marshal JSON response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(jsonResponse)
}
