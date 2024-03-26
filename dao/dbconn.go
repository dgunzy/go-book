package dao

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"

	"github.com/tursodatabase/go-libsql"
)

func StartDB() (*sql.DB, func(), error) {

	err := godotenv.Load()

	if err != nil {
		fmt.Println("Error getting env")
	}

	dbName := "cabot-book"
	primaryUrl := os.Getenv("DBURI")
	authToken := os.Getenv("DBAUTH")

	dir, err := os.MkdirTemp("", "libsql-*")
	if err != nil {
		fmt.Println("Error creating temporary directory:", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(dir, dbName)

	connector, err := libsql.NewEmbeddedReplicaConnector(
		dbPath,
		primaryUrl,
		libsql.WithAuthToken(authToken),
	)
	if err != nil {
		fmt.Println("Error creating connector:", err)
		os.Exit(1)
	}

	db := sql.OpenDB(connector)
	// userDAO := NewUserDAO(db)

	// bungus := userDAO.TestDatabaseConnection()
	// if bungus != nil {
	// 	fmt.Println(bungus)
	// }
	cleanup := func() {

		os.RemoveAll(dir)
	}

	return db, cleanup, nil
}
