package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dgunzy/go-book/models"
)

type UserDAO struct {
	db *sql.DB
}

func NewUserDAO(db *sql.DB) *UserDAO {
	return &UserDAO{db: db}
}

func (dao *UserDAO) GetUserByID(userID int) (*models.User, error) {
	query := "SELECT UserID, Username, Email, Role, Balance, FreePlayBalance, AutoApproveLimit FROM Users WHERE UserID = ?"
	row := dao.db.QueryRow(query, userID)

	var user models.User
	err := row.Scan(&user.UserID, &user.Username, &user.Email, &user.Role, &user.Balance, &user.FreePlayBalance, &user.AutoApproveLimit)
	

	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Println(err)
			return nil, errors.New("user not found")
		}
		fmt.Println(err)
		return nil, err
	}

	return &user, nil
}

func (dao *UserDAO) CreateUser(user *models.User) error {
	query := "INSERT INTO Users (Username, Email, Role, Balance, FreePlayBalance, AutoApproveLimit) VALUES (?, ?, ?, ?, ?, ?)"
	_, err := dao.db.Exec(query, user.Username, user.Email, user.Role, user.Balance, user.FreePlayBalance, user.AutoApproveLimit)
	fmt.Println(err)

	return err
}

func (dao *UserDAO) GetUserByEmail(email string) (*models.User, error) {
	query := "SELECT UserID, Username, Email, Role, Balance, FreePlayBalance, AutoApproveLimit FROM Users WHERE Email = ?"
	row := dao.db.QueryRow(query, email)

	var user models.User
	err := row.Scan(&user.UserID, &user.Username, &user.Email, &user.Role, &user.Balance, &user.FreePlayBalance, &user.AutoApproveLimit)

	if err != nil {
		if err == sql.ErrNoRows {
			// Create a new user if not found
			user = models.User{
				Username:         email,
				Email:            email,
				Role:             "user",
				Balance:          0,
				FreePlayBalance:  0,
				AutoApproveLimit: 200, // Set the default value
			}
			err = dao.CreateUser(&user)
			if err != nil {
				return nil, err
			}
			return &user, nil
		}
		return nil, err
	}

	return &user, nil
}

func (dao *UserDAO) GetAllUsers() ([]*models.User, error) {
	query := "SELECT UserID, Username, Email, Role, Balance, FreePlayBalance, AutoApproveLimit FROM Users"
	rows, err := dao.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*models.User
	for rows.Next() {
		var user models.User
		err := rows.Scan(&user.UserID, &user.Username, &user.Email, &user.Role, &user.Balance, &user.FreePlayBalance, &user.AutoApproveLimit)
		if err != nil {
			return nil, err
		}
		users = append(users, &user)
	}

	return users, nil
}
func (dao *UserDAO) UpdateUserByEmail(email string, updates map[string]interface{}) error {
	// Prepare the query
	var queryParts []string
	var values []interface{}
	queryParts = append(queryParts, "UPDATE Users SET")

	count := 0
	for key, value := range updates {
		if key != "email" && key != "user_id" {
			if count > 0 {
				queryParts = append(queryParts, ",")
			}
			queryParts = append(queryParts, fmt.Sprintf("%s = ?", key))
			values = append(values, value)
			count++
		}
	}

	queryParts = append(queryParts, "WHERE Email = ?")
	values = append(values, email)

	query := strings.Join(queryParts, " ")

	// Execute the query
	_, err := dao.db.Exec(query, values...)
	if err != nil {
		fmt.Println(err)
		return err

	}

	return nil
}
func (dao *UserDAO) UpdateUser(user *models.User) error {
	// Prepare the query
	query := `
        UPDATE Users
        SET Username = ?,
            Role = ?,
            FreePlayBalance = ?,
            AutoApproveLimit = ?
        WHERE UserID = ?
    `

	// Execute the query
	_, err := dao.db.Exec(query, user.Username, user.Role, user.FreePlayBalance, user.AutoApproveLimit, user.UserID)
	if err != nil {
		fmt.Println(err)
		return err
	}

	return nil
}
func (dao *UserDAO) AdjustUserBalance(userID int, adjustmentAmount float64) error {
	// First, get the current balance of the user
	var currentBalance float64
	query := "SELECT Balance FROM Users WHERE UserID = ?"
	err := dao.db.QueryRow(query, userID).Scan(&currentBalance)
	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Println(err)
			return errors.New("user not found")
		}
		fmt.Println(err)
		return err
	}
	fmt.Printf("current balance %.2f\n", currentBalance)
	newBalance := currentBalance + adjustmentAmount

	// Update the user's balance in the database
	updateQuery := "UPDATE Users SET Balance = ? WHERE UserID = ?"
	_, err = dao.db.Exec(updateQuery, newBalance, userID)
	if err != nil {
		fmt.Println("this was a setting error")
		fmt.Println(err)
		return err
	}

	return nil
}
