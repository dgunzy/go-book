package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/routes"
)

func main() {
	app := routes.SetupRoutes()
	dao.StartDB()
	fmt.Println("Server running on 8080")

	if err := http.ListenAndServe(":8080", app); err != nil {
		log.Fatal(err)
	}
}
