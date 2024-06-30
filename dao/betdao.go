package dao

import (
	"fmt"
	"strings"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

func (dao *UserDAO) CreateBet(bet *models.Bet) (int64, error) {
	//Need to check that the descriptions are unique here
	// Check if the descriptions are unique
	betOutcomeMap := make(map[string]bool)

	for i := 0; i < len(bet.BetOutcomes); i++ {
		if _, ok := betOutcomeMap[bet.BetOutcomes[i].Description]; ok {
			return 0, fmt.Errorf("duplicate bet outcome description: %s", bet.BetOutcomes[i].Description)
		}
		betOutcomeMap[bet.BetOutcomes[i].Description] = true
	}

	// Insert the bet into the database
	result, err := dao.db.Exec("INSERT INTO bets (title, description, OddsMultiplier, status, category, createdBy, createdAt, expiryTime) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		bet.Title, bet.Description, bet.OddsMultiplier, bet.Status, bet.Category, bet.CreatedBy, bet.CreatedAt, bet.ExpiryTime)
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
	for _, outcome := range bet.BetOutcomes {
		_, err := dao.db.Exec("INSERT INTO betOutcomes (betId, description, odds) VALUES (?, ?, ?)",
			betID, outcome.Description, outcome.Odds)
		if err != nil {
			fmt.Println(err)
			return 0, err
		}
	}
	return betID, nil
}

func (dao *UserDAO) ReadBet(betID int) (*models.Bet, error) {
	bet := new(models.Bet)
	var createdAtStr, expiryTimeStr string // Use strings to temporarily hold the timestamps

	query := `SELECT BetID, Title, Description, OddsMultiplier, Status, Category, CreatedBy, CreatedAt, ExpiryTime FROM Bets WHERE BetID = ?`
	err := dao.db.QueryRow(query, betID).Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &createdAtStr, &expiryTimeStr)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	// Convert the timestamp strings to time.Time using your utility functions
	bet.CreatedAt, err = utils.SQLiteToGo(createdAtStr)
	if err != nil {
		fmt.Println("Error parsing CreatedAt:", err)
		return nil, err
	}

	bet.ExpiryTime, err = utils.SQLiteToGo(expiryTimeStr)
	if err != nil {
		fmt.Println("Error parsing ExpiryTime:", err)
		return nil, err
	}
	query = `SELECT  Description, Odds FROM BetOutcomes WHERE BetID = ?`
	rows, err := dao.db.Query(query, betID)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		outcome := new(models.BetOutcomes)
		err = rows.Scan(&outcome.Description, &outcome.Odds)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		bet.BetOutcomes = append(bet.BetOutcomes, *outcome)
	}
	return bet, nil
}

// UpdateBet updates a bet with the given ID and the provided updates
func (dao *UserDAO) UpdateBet(betID int, updates map[string]interface{}) error {
	// Generate the SQL query string
	query := "UPDATE Bets SET"
	params := []interface{}{}
	for key, value := range updates {
		query += fmt.Sprintf(" %s = ?,", key)
		params = append(params, value)
	}
	// Remove the trailing comma
	query = strings.TrimSuffix(query, ",")
	query += " WHERE BetID = ?"
	params = append(params, betID)

	// Execute the update query
	_, err := dao.db.Exec(query, params...)
	if err != nil {
		fmt.Println(err)
		return err
	}

	return nil
}
func (dao *UserDAO) DeleteBet(betID int) error {
	query := `DELETE FROM Bets WHERE BetID = ?`
	_, err := dao.db.Exec(query, betID)
	return err
}

func (dao *UserDAO) GetAllBets() (*[]models.Bet, error) {
	betsQuery := `
		SELECT BetID, Title, Description, OddsMultiplier, Status, Category, CreatedBy, CreatedAt, ExpiryTime
		FROM Bets
	`
	rows, err := dao.db.Query(betsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Map to hold bets with their BetID as key
	betSlice := make([]models.Bet, 0)

	for rows.Next() {
		var bet models.Bet
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &bet.CreatedAt, &bet.ExpiryTime)
		if err != nil {
			return nil, err
		}
		betSlice = append(betSlice, bet)
	}

	// Query to fetch all bet outcomes
	outcomesQuery := `
		SELECT BetID, Description, Odds
		FROM BetOutcomes
	`
	outcomeRows, err := dao.db.Query(outcomesQuery)
	if err != nil {
		return nil, err
	}
	defer outcomeRows.Close()

	for outcomeRows.Next() {
		var outcome models.BetOutcomes
		var betID int
		err := outcomeRows.Scan(&betID, &outcome.Description, &outcome.Odds)
		if err != nil {
			return nil, err
		}
		for i := range betSlice {
			if betSlice[i].BetID == betID {
				betSlice[i].BetOutcomes = append(betSlice[i].BetOutcomes, outcome)
			}
		}
	}
	return &betSlice, nil
}

func (dao *UserDAO) GetBetsByCategory(category string) (*[]models.Bet, error) {
	betsQuery := `
		SELECT BetID, Title, Description, OddsMultiplier, Status, Category, CreatedBy, CreatedAt, ExpiryTime
		FROM Bets
		WHERE Category = ?
	`
	rows, err := dao.db.Query(betsQuery, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Map to hold bets with their BetID as key
	betSlice := make([]models.Bet, 0)

	for rows.Next() {
		var bet models.Bet
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &bet.CreatedAt, &bet.ExpiryTime)
		if err != nil {
			return nil, err
		}
		betSlice = append(betSlice, bet)
	}

	// Query to fetch all bet outcomes
	outcomesQuery := `
		SELECT BetID, Description, Odds
		FROM BetOutcomes
	`
	outcomeRows, err := dao.db.Query(outcomesQuery)
	if err != nil {
		return nil, err
	}
	defer outcomeRows.Close()

	for outcomeRows.Next() {
		var outcome models.BetOutcomes
		var betID int
		err := outcomeRows.Scan(&betID, &outcome.Description, &outcome.Odds)
		if err != nil {
			return nil, err
		}
		for i := range betSlice {
			if betSlice[i].BetID == betID {
				betSlice[i].BetOutcomes = append(betSlice[i].BetOutcomes, outcome)
			}
		}
	}
	return &betSlice, nil
}
