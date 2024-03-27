package server

import (
	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/dao"
)

type Handler struct {
	dao  *dao.UserDAO
	auth *auth.AuthService
}

func New(dao *dao.UserDAO, auth *auth.AuthService) *Handler {
	return &Handler{
		dao:  dao,
		auth: auth,
	}
}
