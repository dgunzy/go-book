package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/server"
	"github.com/gorilla/mux"
)

func ServeFavicon(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "static/images/favicon.ico")
}
func customFileServer(fs http.FileSystem) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript")
		} else if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Content-Type", "text/css")
		}
		http.FileServer(fs).ServeHTTP(w, r)
	})
}

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
	router.HandleFunc("/applicationoffline", handler.ApplicationOffline).Methods("GET")
	router.HandleFunc("/favicon.ico", ServeFavicon).Methods("GET")
	// User protected routes
	router.HandleFunc("/cabot-book", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handler.HandleHome(w, r)
		if err := syncDatabase(); err != nil {
			fmt.Println("Error syncing database:", err)
		}
	}, authService, dao.NewUserDAO(db))).Methods("GET")

	router.HandleFunc("/dashboard", auth.RequireAuth(handler.UserDashboard, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/navbar", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) { handler.Navbar(w, r) }, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/matchbets", auth.RequireAuth(handler.GetMatchBets, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/futurebets", auth.RequireAuth(handler.GetFutureBets, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/props", auth.RequireAuth(handler.GetPropBets, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/parlay", auth.RequireAuth(handler.GetAllBets, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/transactions", auth.RequireAuth(handler.ReadUserTransactions, authService, dao.NewUserDAO(db))).Methods("GET")

	// router.HandleFunc("/test", auth.RequireAuth(handler.Test, authService)).Methods("GET")

	// Admin protected routes
	router.HandleFunc("/appstatus", auth.RequireAdmin(handler.ApplicationStatus, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/toggleapplicationstate", auth.RequireAdmin(handler.ToggleApplicationState, authService, dao.NewUserDAO(db))).Methods("POST")
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
	router.HandleFunc("/create-new-bet-form", auth.RequireAdmin(handler.GetNewBetPage, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/view-bannable-users", auth.RequireAdmin(handler.GetBannableUsers, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/cancel-view-bannable-users", auth.RequireAdmin(handler.CancelViewBannableUser, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/create-new-bet", auth.RequireAdmin(handler.CreateNewBet, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/deletebet/{betID}", auth.RequireAdmin(handler.MoveBetToClosed, authService, dao.NewUserDAO(db))).Methods("POST")
	router.HandleFunc("/editbet/{betID}", auth.RequireAdmin(handler.EditBetForm, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/update-bet/{betID}", auth.RequireAdmin(handler.UpdateBet, authService, dao.NewUserDAO(db))).Methods("PUT")

	// Root protected routes
	// router.HandleFunc("/rootdashboard", auth.RequireRoot(handler.RootAdminDashboard, authService, dao.NewUserDAO(db))).Methods("GET")
	router.HandleFunc("/rootdashboard", auth.RequireRoot(handler.RootUserEditingDashboard, authService, dao.NewUserDAO(db))).Methods("GET")

	// router.HandleFunc("/rununittests", auth.RequireRoot(handler.RunUnitTests, authService, dao.NewUserDAO(db))).Methods("GET")
	// router.HandleFunc("/runusergetbettest", auth.RequireRoot(handler.RunGetUserBetTest, authService, dao.NewUserDAO(db))).Methods("GET")

	//Auth Routes
	router.HandleFunc("/auth/{provider}", handler.HandleProviderLogin).Methods("GET")
	router.HandleFunc("/home", handler.HandleAuthCallbackFunction).Methods("GET")
	router.HandleFunc("/logout/{provider}", handler.HandleLogout).Methods("GET")

	fs := customFileServer(http.Dir("./static"))
	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", fs))

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
