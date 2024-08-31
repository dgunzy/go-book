package dao

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tursodatabase/go-libsql"
)

func StartDB() (*sql.DB, func(), func() error, error) {

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
	cleanup := func() {

		os.RemoveAll(dir)
	}
	syncDatabase := func() error {
		if err := connector.Sync(); err != nil {
			fmt.Println("Error syncing database:", err)
			return err
		}
		return nil
	}

	return db, cleanup, syncDatabase, nil
}
