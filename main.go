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

	db, cleanup, syncDatabase, err := dao.StartDB()
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
	router.HandleFunc("/dashboard", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handler.HandleHome(w, r)
		if err := syncDatabase(); err != nil {
			fmt.Println("Error syncing database:", err)
		}
	}, authService)).Methods("GET")
	router.HandleFunc("/navbar", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Received request for /navbar")
		handler.Navbar(w, r)
	}, authService)).Methods("GET")
	router.HandleFunc("/matchbets", auth.RequireAuth(handler.GetMatchBets, authService)).Methods("GET")
	router.HandleFunc("/futurebets", auth.RequireAuth(handler.GetFutureBets, authService)).Methods("GET")
	router.HandleFunc("/props", auth.RequireAuth(handler.GetPropBets, authService)).Methods("GET")
	router.HandleFunc("/parlay", auth.RequireAuth(handler.GetAllBets, authService)).Methods("GET")

	// router.HandleFunc("/test", auth.RequireAuth(handler.Test, authService)).Methods("GET")

	// Admin protected routes
	router.HandleFunc("/admindashboard", auth.RequireAdmin(handler.AdminDashboard, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/user/{email}", auth.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		handler.UpdateUserForm(w, r)
		if err := syncDatabase(); err != nil {
			fmt.Println("Error syncing database:", err)
		}
	}, authService, dao.NewUserDAO(db))).Methods("POST")

	router.HandleFunc("/update-user/{email}", auth.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		handler.UpdateUser(w, r)
		if err := syncDatabase(); err != nil {
			fmt.Println("Error syncing database:", err)
		}
	}, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/adminuseredit", auth.RequireAdmin(handler.AdminUserEdit, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/adminusereditremove", auth.RequireAdmin(handler.AdminUserEditRemove, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/adminbetedit/{bettype}", auth.RequireAdmin(handler.AdminBetEdit, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/adminbeteditdelete/{bettype}", auth.RequireAdmin(handler.AdminBetToggle, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/createUserTransaction/{email}", auth.RequireAdmin(handler.AdminTransactionEdit, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/create-transaction", auth.RequireAdmin(handler.CreateTransaction, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/canceluseredit/{email}", auth.RequireAdmin(handler.CancelUserEdit, authService, dao.NewUserDAO(db))).Methods("POST")

	// Root protected routes
	// router.HandleFunc("/rootdashboard", auth.RequireRoot(handler.RootAdminDashboard, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/rootdashboard", auth.RequireRoot(handler.RootUserEditingDashboard, authService, dao.NewUserDAO(db))).Methods("GET")

	// router.HandleFunc("/rununittests", auth.RequireRoot(handler.RunUnitTests, authService, dao.NewUserDAO(db))).Methods("GET")
	// router.HandleFunc("/runusergetbettest", auth.RequireRoot(handler.RunGetUserBetTest, authService, dao.NewUserDAO(db))).Methods("GET")

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
