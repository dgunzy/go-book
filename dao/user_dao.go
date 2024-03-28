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
			return nil, errors.New("user not found")
		}
		return nil, err
	}

	return &user, nil
}

func (dao *UserDAO) UpdateUserToken(userID int, token string) error {
	query := "UPDATE Users SET Token = ? WHERE UserID = ?"
	_, err := dao.db.Exec(query, token, userID)
	return err
}

func (dao *UserDAO) CreateUser(user *models.User) error {
	query := "INSERT INTO Users (Username, Email, Role, Balance, FreePlayBalance, AutoApproveLimit) VALUES (?, ?, ?, ?, ?, ?)"
	_, err := dao.db.Exec(query, user.Username, user.Email, user.Role, user.Balance, user.FreePlayBalance, user.AutoApproveLimit)
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
func (dao *UserDAO) CreateBet(bet *models.Bet, outcomes []*models.BetOutcome) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `INSERT INTO Bets (Title, Description, OddsMultiplier, Status, CreatedBy) VALUES (?, ?, ?, ?, ?)`
	res, err := tx.Exec(query, bet.Title, bet.Description, bet.OddsMultiplier, bet.Status, bet.CreatedBy)
	if err != nil {
		return err
	}

	betID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	for _, outcome := range outcomes {
		query := `INSERT INTO BetOutcomes (BetID, Description, Odds) VALUES (?, ?, ?)`
		_, err = tx.Exec(query, betID, outcome.Description, outcome.Odds)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (dao *UserDAO) ReadBet(betID int) (*models.Bet, []*models.BetOutcome, error) {
	bet := new(models.Bet)
	query := `SELECT * FROM Bets WHERE BetID = ?`
	err := dao.db.QueryRow(query, betID).Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.CreatedBy, &bet.CreatedAt)
	if err != nil {
		return nil, nil, err
	}

	var outcomes []*models.BetOutcome
	query = `SELECT OutcomeID, Description, Odds FROM BetOutcomes WHERE BetID = ?`
	rows, err := dao.db.Query(query, betID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		outcome := new(models.BetOutcome)
		err = rows.Scan(&outcome.OutcomeID, &outcome.Description, &outcome.Odds)
		if err != nil {
			return nil, nil, err
		}
		outcomes = append(outcomes, outcome)
	}

	return bet, outcomes, nil
}

// UpdateBet updates a bet with the given ID and the provided updates
func (dao *UserDAO) UpdateBet(betID int, updates map[string]interface{}, outcomes []*models.BetOutcome) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Update Bets table
	var queryParts []string
	var values []interface{}
	queryParts = append(queryParts, "UPDATE Bets SET")
	count := 0
	for key, value := range updates {
		if key != "bet_id" {
			if count > 0 {
				queryParts = append(queryParts, ",")
			}
			queryParts = append(queryParts, fmt.Sprintf("%s = ?", key))
			values = append(values, value)
			count++
		}
	}
	queryParts = append(queryParts, "WHERE BetID = ?")
	values = append(values, betID)
	query := strings.Join(queryParts, " ")
	_, err = tx.Exec(query, values...)
	if err != nil {
		return err
	}

	// Update BetOutcomes table
	_, err = tx.Exec("DELETE FROM BetOutcomes WHERE BetID = ?", betID)
	if err != nil {
		return err
	}

	for _, outcome := range outcomes {
		query := `INSERT INTO BetOutcomes (BetID, Description, Odds) VALUES (?, ?, ?)`
		_, err = tx.Exec(query, betID, outcome.Description, outcome.Odds)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
func (dao *UserDAO) DeleteBet(betID int) error {
	query := `DELETE FROM Bets WHERE BetID = ?`
	_, err := dao.db.Exec(query, betID)
	return err
}

// Transaction Create/Read operations
func (dao *UserDAO) CreateTransaction(transaction *models.Transaction) error {
	query := `INSERT INTO Transactions (UserID, Amount, Type, Description) VALUES (?, ?, ?, ?)`
	_, err := dao.db.Exec(query, transaction.UserID, transaction.Amount, transaction.Type, transaction.Description)
	return err
}

func (dao *UserDAO) ReadTransaction(transactionID int) (*models.Transaction, error) {
	transaction := new(models.Transaction)
	query := `SELECT * FROM Transactions WHERE TransactionID = ?`
	err := dao.db.QueryRow(query, transactionID).Scan(&transaction.TransactionID, &transaction.UserID, &transaction.Amount, &transaction.Type, &transaction.Description, &transaction.TransactionDate)
	return transaction, err
}
func (dao *UserDAO) GetAllBets() ([]*models.Bet, error) {
	query := "SELECT BetID, Title, Description, OddsMultiplier, Status, CreatedBy, CreatedAt FROM Bets"
	rows, err := dao.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bets []*models.Bet
	for rows.Next() {
		bet := new(models.Bet)
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.CreatedBy, &bet.CreatedAt)
		if err != nil {
			return nil, err
		}
		bets = append(bets, bet)
	}

	return bets, nil
}
func (dao *UserDAO) GetUserBets(userID int) ([]*models.UserBet, error) {
	query := "SELECT UserBetID, UserID, BetID, OutcomeID, Amount, PlacedAt, Result FROM UserBets WHERE UserID = ?"
	rows, err := dao.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var userBets []*models.UserBet
	for rows.Next() {
		userBet := new(models.UserBet)
		err := rows.Scan(&userBet.UserBetID, &userBet.UserID, &userBet.BetID, &userBet.OutcomeID, &userBet.Amount, &userBet.PlacedAt, &userBet.Result)
		if err != nil {
			return nil, err
		}
		userBets = append(userBets, userBet)
	}

	return userBets, nil
}
func (dao *UserDAO) PlaceBet(userBet *models.UserBet) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Deduct the bet amount from the user's balance
	query := "UPDATE Users SET Balance = Balance - ? WHERE UserID = ?"
	_, err = tx.Exec(query, userBet.Amount, userBet.UserID)
	if err != nil {
		return err
	}

	// Insert the user bet
	query = "INSERT INTO UserBets (UserID, BetID, OutcomeID, Amount) VALUES (?, ?, ?, ?)"
	_, err = tx.Exec(query, userBet.UserID, userBet.BetID, userBet.OutcomeID, userBet.Amount)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (dao *UserDAO) GradeBet(betID int, winningOutcomeID int) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Update the Bets table to mark the bet as graded
	query := "UPDATE Bets SET Status = 'graded' WHERE BetID = ?"
	_, err = tx.Exec(query, betID)
	if err != nil {
		return err
	}

	// Update the UserBets table with the result
	query = "UPDATE UserBets SET Result = CASE WHEN OutcomeID = ? THEN 'win' ELSE 'loss' END WHERE BetID = ?"
	_, err = tx.Exec(query, winningOutcomeID, betID)
	if err != nil {
		return err
	}

	// Get the bet details and outcomes
	bet, outcomes, err := dao.ReadBet(betID)
	if err != nil {
		return err
	}

	// Find the winning outcome odds
	var winningOutcomeOdds float64
	for _, outcome := range outcomes {
		if outcome.OutcomeID == winningOutcomeID {
			winningOutcomeOdds = outcome.Odds
			break
		}
	}

	// Update the user balances for winning bets
	query = `
        UPDATE Users
        SET Balance = Balance + (UserBets.Amount * ?)
        FROM UserBets
        WHERE UserBets.BetID = ? AND UserBets.OutcomeID = ? AND UserBets.Result = 'win'
    `
	_, err = tx.Exec(query, bet.OddsMultiplier*winningOutcomeOdds, betID, winningOutcomeID)
	if err != nil {
		return err
	}

	return tx.Commit()
}
