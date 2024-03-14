package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/routes"
)

func main() {
	db, userDAO, err := dao.StartDB()
	if err != nil {
		log.Fatal("Failed to start database:", err)
	}
	defer db.Close()

	userHandler := routes.NewUserHandler(db, userDAO)

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir("routing/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", routes.HomeHandler)
	mux.HandleFunc("/home", userHandler.LoginHandler)
	mux.HandleFunc("/user", userHandler.GetUser)

	fmt.Println("Server running on 8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}
