package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/server"
	"github.com/gorilla/mux"
)

func main() {

	db, cleanup, err := dao.StartDB()
	if err != nil {
		fmt.Println(err)
		return
	}
	sessionKey := os.Getenv("SESSION")

	timeInSeconds := time.Duration(7 * 24 * time.Hour).Seconds()

	sessionStore := auth.NewCookieStore(auth.SessionOptions{
		CookiesKey: sessionKey,
		MaxAge:     int(timeInSeconds),
		Secure:     true,
		HttpOnly:   false,
	})
	initStorage(db)

	authService := auth.NewAuthService(sessionStore)

	router := mux.NewRouter()

	handler := server.New(dao.NewUserDAO(db), authService)
	// Public routes
	router.HandleFunc("/", handler.HandleLogin).Methods("GET")
	router.HandleFunc("/login", handler.HandleLogin).Methods("GET")
	// router.HandleFunc("/edituser", handler.HandleLogin).Methods("GET")

	// User protected routes
	router.HandleFunc("/dashboard", auth.RequireAuth(handler.HandleHome, authService)).Methods("GET")
	// router.HandleFunc("/test", auth.RequireAuth(handler.Test, authService)).Methods("GET")

	// Admin protected routes
	router.HandleFunc("/admindashboard", auth.RequireAdmin(handler.RootAdminDashboard, authService, dao.NewUserDAO(db))).Methods("GET")

	// Root protected routes
	router.HandleFunc("/rootdashboard", auth.RequireRoot(handler.RootAdminDashboard, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/useredit", auth.RequireRoot(handler.RootUserEditingDashboard, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/user/{email}", auth.RequireRoot(handler.UpdateUser, authService, dao.NewUserDAO(db))).Methods("POST")
	//Auth Routes
	router.HandleFunc("/auth/{provider}", handler.HandleProviderLogin).Methods("GET")
	router.HandleFunc("/home", handler.HandleAuthCallbackFunction).Methods("GET")
	router.HandleFunc("/logout/{provider}", handler.HandleLogout).Methods("GET")

	fs := http.FileServer(http.Dir("/routing/static"))
	router.Handle("/static/", http.StripPrefix("/static/", fs))

	fmt.Println("Server running on 8080")
	http.ListenAndServe(":8080", router)
	defer cleanup()
}

func initStorage(db *sql.DB) {
	err := db.Ping()
	if err != nil {
		log.Fatal(err)
	}

	log.Println("DB: Successfully connected!")
}
