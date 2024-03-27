package models

type User struct {
	UserID   int     `json:"user_id"`
	Username string  `json:"username"`
	Email    string  `json:"email"`
	Role     string  `json:"role"`
	Balance  float64 `json:"balance"`
	Token    string  `json:"token"`
}
