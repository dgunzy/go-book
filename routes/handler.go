package routes

import (
	"html/template"
	"net/http"

	"github.com/dgunzy/go-book/dao"
)

type UserHandler struct {
	userDAO *dao.UserDAO
}

func NewUserHandler(userDAO *dao.UserDAO) *UserHandler {
	return &UserHandler{userDAO: userDAO}
}

func HomeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/index.gohtml"))
	// data := map[string]interface{}{
	// 	"Title": "HTMX Example",
	// }

	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
// 	userIDStr := r.URL.Query().Get("user_id")
// 	userID, err := strconv.Atoi(userIDStr)
// 	if err != nil {
// 		http.Error(w, "Invalid user ID", http.StatusBadRequest)
// 		return
// 	}

// 	user, err := h.userDAO.GetUserByID(userID)
// 	if err != nil {
// 		if err.Error() == "user not found" {
// 			http.Error(w, "User not found", http.StatusNotFound)
// 		} else {
// 			http.Error(w, "Internal server error", http.StatusInternalServerError)
// 		}
// 		return
// 	}

// 	response, err := json.Marshal(user)
// 	if err != nil {
// 		http.Error(w, "Internal server error", http.StatusInternalServerError)
// 		return
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	w.WriteHeader(http.StatusOK)
// 	w.Write(response)
// }
