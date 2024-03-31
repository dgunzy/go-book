package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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
func (dao *UserDAO) CreateBet(bet *models.Bet, outcomes []*models.BetOutcome) (int64, error) {
	// Get the current time
	currentTime := time.Now()

	// Insert the bet into the database
	result, err := dao.db.Exec("INSERT INTO bets (title, description, OddsMultiplier, status, createdBy, createdAt) VALUES (?, ?, ?, ?, ?, ?)",
		bet.Title, bet.Description, bet.OddsMultiplier, bet.Status, bet.CreatedBy, currentTime.String())
	if err != nil {
		fmt.Println(err)
		return 0, err
	}

	// Get the last inserted ID
	betID, err := result.LastInsertId()
	if err != nil {
		fmt.Println(err)
		return 0, err
	}

	// Insert the bet outcomes into the database
	for _, outcome := range outcomes {
		_, err := dao.db.Exec("INSERT INTO betOutcomes (betId, description, odds) VALUES (?, ?, ?)",
			betID, outcome.Description, outcome.Odds)
		if err != nil {
			fmt.Println(err)
			return 0, err
		}
	}

	return betID, nil
}

func (dao *UserDAO) ReadBet(betID int) (*models.Bet, []*models.BetOutcome, error) {
	bet := new(models.Bet)
	query := `SELECT * FROM Bets WHERE BetID = ?`
	err := dao.db.QueryRow(query, betID).Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.CreatedBy, &bet.CreatedAt)
	if err != nil {
		fmt.Println(err)
		return nil, nil, err
	}

	var outcomes []*models.BetOutcome
	query = `SELECT OutcomeID, Description, Odds FROM BetOutcomes WHERE BetID = ?`
	rows, err := dao.db.Query(query, betID)
	if err != nil {
		fmt.Println(err)
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		outcome := new(models.BetOutcome)
		err = rows.Scan(&outcome.OutcomeID, &outcome.Description, &outcome.Odds)
		if err != nil {
			fmt.Println(err)
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
func (dao *UserDAO) CreateTransaction(transaction *models.Transaction) (int64, error) {
	query := `INSERT INTO Transactions (UserID, Amount, Type, Description) VALUES (?, ?, ?, ?)`
	result, err := dao.db.Exec(query, transaction.UserID, transaction.Amount, transaction.Type, transaction.Description)
	if err != nil {
		fmt.Println(err)
		return 0, err
	}
	lastId, err := result.LastInsertId()
	return lastId, err
}

func (dao *UserDAO) ReadTransaction(transactionID int) (*models.Transaction, error) {
	transaction := new(models.Transaction)
	query := `SELECT * FROM Transactions WHERE TransactionID = ?`
	err := dao.db.QueryRow(query, transactionID).Scan(&transaction.TransactionID, &transaction.UserID, &transaction.Amount, &transaction.Type, &transaction.Description, &transaction.TransactionDate)
	return transaction, err
}
func (dao *UserDAO) GetAllBets() (map[*models.Bet][]*models.BetOutcome, error) {
	query := "SELECT b.BetID, b.Title, b.Description, b.OddsMultiplier, b.Status, b.CreatedBy, b.CreatedAt, bo.OutcomeID, bo.Description, bo.Odds FROM Bets b LEFT JOIN BetOutcomes bo ON b.BetID = bo.BetID"
	rows, err := dao.db.Query(query)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	betMap := make(map[*models.Bet][]*models.BetOutcome)

	for rows.Next() {
		var bet models.Bet
		var outcome models.BetOutcome

		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.CreatedBy, &bet.CreatedAt, &outcome.OutcomeID, &outcome.Description, &outcome.Odds)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		// Check if the bet already exists in the map
		existingBet, ok := findBetInMap(betMap, bet.BetID)
		if !ok {
			// If the bet doesn't exist, add it to the map
			betMap[&bet] = []*models.BetOutcome{}
			existingBet = &bet
		}

		// Append the outcome to the bet's outcomes
		betMap[existingBet] = append(betMap[existingBet], &outcome)
	}

	return betMap, nil
}
func findBetInMap(betMap map[*models.Bet][]*models.BetOutcome, betID int) (*models.Bet, bool) {
	for bet := range betMap {
		if bet.BetID == betID {
			return bet, true
		}
	}
	return nil, false
}

func (dao *UserDAO) GetUserBets(userEmail string) ([]*models.UserBet, error) {
	id, err := dao.GetUserByEmail(userEmail)
	if err != nil {
		return nil, err
	}
	query := "SELECT UserBetID, UserID, BetID, OutcomeID, Amount, PlacedAt, CAST(Result AS TEXT) AS Result FROM UserBets WHERE UserID = ?"
	rows, err := dao.db.Query(query, id.UserID)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	// Print the result of the SQL query
	columns, err := rows.Columns()
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range columns {
		valuePtrs[i] = &values[i]
	}
	for rows.Next() {
		err := rows.Scan(valuePtrs...)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		fmt.Printf("Query Result: %v\n", values)
	}

	// Reset the rows pointer to the beginning
	rows.Close()
	rows, err = dao.db.Query(query, id.UserID)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	var userBets []*models.UserBet
	for rows.Next() {
		userBet := new(models.UserBet)
		err := rows.Scan(&userBet.UserBetID, &userBet.UserID, &userBet.BetID, &userBet.OutcomeID, &userBet.Amount, &userBet.PlacedAt, &userBet.Result)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		userBets = append(userBets, userBet)
	}
	return userBets, nil
}

// func (dao *UserDAO) GetUserBets(userEmail string) ([]*models.UserBet, error) {
// 	query := `
//         SELECT ub.UserBetID, ub.UserID, ub.BetID, ub.OutcomeID, ub.Amount, ub.PlacedAt, ub.Result
//         FROM UserBets ub
//         INNER JOIN Users u ON ub.UserID = u.UserID
//         WHERE u.Email = ?
//     `
// 	rows, err := dao.db.Query(query, userEmail)
// 	if err != nil {
// 		fmt.Println(err)
// 		return nil, err
// 	}
// 	defer rows.Close()

// 	var userBets []*models.UserBet
// 	for rows.Next() {
// 		userBet := new(models.UserBet)
// 		var result sql.NullString
// 		err := rows.Scan(&userBet.UserBetID, &userBet.UserID, &userBet.BetID, &userBet.OutcomeID, &userBet.Amount, &userBet.PlacedAt, &result)
// 		if err != nil {
// 			fmt.Println(err)
// 			return nil, err
// 		}

// 		//REmove this line
// 		// fmt.Printf("Scanned values: UserBetID=%d, UserID=%d, BetID=%d, OutcomeID=%d, Amount=%.2f, PlacedAt=%v, Result=%v\n",
// 		// 	userBet.UserBetID, userBet.UserID, userBet.BetID, userBet.OutcomeID, userBet.Amount, userBet.PlacedAt, result)

// 		// if result.Valid {
// 		// 	userBet.Result = result.String
// 		// } else {
// 		// 	userBet.Result = "ungraded" // Set a default value for Result if it's NULL
// 		// }
// 		userBets = append(userBets, userBet)
// 	}

// 	return userBets, nil
// }

func (dao *UserDAO) PlaceBet(userBet *models.UserBet) error {
	tx, err := dao.db.Begin()
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer tx.Rollback()

	// Deduct the bet amount from the user's balance
	query := "UPDATE Users SET Balance = Balance - ? WHERE UserID = ?"
	_, err = tx.Exec(query, userBet.Amount, userBet.UserID)
	if err != nil {
		fmt.Println(err)
		return err
	}

	// Insert the user bet with the Result set to "ungraded"
	query = "INSERT INTO UserBets (UserID, BetID, OutcomeID, Amount, Result) VALUES (?, ?, ?, ?, ?)"
	_, err = tx.Exec(query, userBet.UserID, userBet.BetID, userBet.OutcomeID, userBet.Amount, "ungraded")
	if err != nil {
		fmt.Println(err)
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
