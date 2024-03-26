package main

import (
	"fmt"
	"log"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/server"
)

func main() {
	auth.NewAuth()
	server, cleanup := server.NewServer()

	fmt.Println("Server running on 8080")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
	defer cleanup()
}
