package models

type User struct {
	UserID           int     `json:"user_id"`
	Username         string  `json:"username"`
	Email            string  `json:"email"`
	Role             string  `json:"role"`
	Balance          float64 `json:"balance"`
	FreePlayBalance  float64 `json:"free_play_balance"`
	AutoApproveLimit int     `json:"auto_approve_limit"`
}
