package dao

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/dgunzy/go-book/models"
)

type UserDAO struct {
	db *sql.DB
}

func NewUserDAO(db *sql.DB) *UserDAO {
	return &UserDAO{db: db}
}

func (dao *UserDAO) GetUserByID(userID int) (*models.User, error) {
	query := "SELECT UserID, Username, Email, Role, Balance FROM Users WHERE UserID = ?"
	row := dao.db.QueryRow(query, userID)

	var user models.User
	err := row.Scan(&user.UserID, &user.Username, &user.Email, &user.Role, &user.Balance)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("user not found")
		}
		return nil, err
	}

	return &user, nil
}

func (dao *UserDAO) TestDatabaseConnection() error {
	// Create the "test" table if it doesn't exist
	createTableQuery := `
        CREATE TABLE IF NOT EXISTS test (
            ID INTEGER PRIMARY KEY,
            NAME TEXT
        )
    `
	_, err := dao.db.Exec(createTableQuery)
	if err != nil {
		return err
	}

	// Insert a sample row into the "test" table
	insertQuery := "INSERT INTO test (ID, NAME) VALUES (?, ?)"
	_, err = dao.db.Exec(insertQuery, 2, "John Doe")
	if err != nil {
		return err
	}

	// Query the "test" table and print the result
	selectQuery := "SELECT ID, NAME FROM test"
	rows, err := dao.db.Query(selectQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var name string
		err := rows.Scan(&id, &name)
		if err != nil {
			return err
		}
		fmt.Printf("ID: %d, Name: %s\n", id, name)
	}

	return nil
}

func (dao *UserDAO) UpdateUserToken(userID int, token string) error {
	query := "UPDATE Users SET Token = ? WHERE UserID = ?"
	_, err := dao.db.Exec(query, token, userID)
	return err
}

func (dao *UserDAO) CreateUser(user *models.User) error {
	query := "INSERT INTO Users (Username, Email, Role, Balance) VALUES (?, ?, ?, ?)"
	_, err := dao.db.Exec(query, user.Username, user.Email, user.Role, user.Balance)
	return err
}

func (dao *UserDAO) GetUserByEmail(email string) (*models.User, error) {
	query := "SELECT UserID, Username, Email, Role, Balance FROM Users WHERE Email = ?"
	row := dao.db.QueryRow(query, email)

	var user models.User
	err := row.Scan(&user.UserID, &user.Username, &user.Email, &user.Role, &user.Balance)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("user not found")
		}
		return nil, err
	}

	return &user, nil
}
