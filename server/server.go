package server

import (
	"database/sql"
	"fmt"
	"net/http"

	"time"

	"github.com/dgunzy/go-book/dao"
)

type Server struct {
	port int
	db   *sql.DB
}

func NewServer() (*http.Server, func()) {
	port := 8080
	db, cleanup, _ := dao.StartDB()

	NewServer := &Server{
		port: port,

		db: db,
	}

	// Declare Server config
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", NewServer.port),
		Handler:      NewServer.RegisterRoutes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return server, func() {
		db.Close()
		cleanup()
	}
}
