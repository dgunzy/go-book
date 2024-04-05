package dao

import (
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

func (dao *UserDAO) CreateBet(bet *models.Bet, outcomes []*models.BetOutcome) (int64, error) {
	// Get the current time

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
	var createdAtStr, expiryTimeStr string // Use strings to temporarily hold the timestamps

	query := `SELECT BetID, Title, Description, OddsMultiplier, Status, Category, CreatedBy, CreatedAt, ExpiryTime FROM Bets WHERE BetID = ?`
	err := dao.db.QueryRow(query, betID).Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &createdAtStr, &expiryTimeStr)
	if err != nil {
		fmt.Println(err)
		return nil, nil, err
	}

	// Convert the timestamp strings to time.Time using your utility functions
	bet.CreatedAt, err = utils.SQLiteToGo(createdAtStr)
	if err != nil {
		fmt.Println("Error parsing CreatedAt:", err)
		return nil, nil, err
	}

	bet.ExpiryTime, err = utils.SQLiteToGo(expiryTimeStr)
	if err != nil {
		fmt.Println("Error parsing ExpiryTime:", err)
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

	// Constructing the SQL query for updating the Bet
	var setParts []string
	var args []interface{}
	for key, val := range updates {
		// Assuming the caller ensures key is a valid column name
		setParts = append(setParts, fmt.Sprintf("%s = ?", key))
		if t, ok := val.(time.Time); ok {
			// If the value is a time.Time, format it for SQLite
			val = t.Format(time.RFC3339)
		}
		args = append(args, val)
	}
	if len(setParts) > 0 {
		updateQuery := "UPDATE Bets SET " + strings.Join(setParts, ", ") + " WHERE BetID = ?"
		args = append(args, betID)
		if _, err := tx.Exec(updateQuery, args...); err != nil {
			return err
		}
	}

	// Updating BetOutcomes
	// First, delete existing outcomes for the bet
	if _, err := tx.Exec("DELETE FROM BetOutcomes WHERE BetID = ?", betID); err != nil {
		return err
	}
	// Then, insert the new outcomes
	for _, outcome := range outcomes {
		if _, err := tx.Exec("INSERT INTO BetOutcomes (BetID, Description, Odds) VALUES (?, ?, ?)", betID, outcome.Description, outcome.Odds); err != nil {
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

func (dao *UserDAO) GetAllBets() (map[*models.Bet][]*models.BetOutcome, error) {
	query := `SELECT b.BetID, b.Title, b.Description, b.OddsMultiplier, b.Status, b.Category, b.CreatedBy, b.CreatedAt, b.ExpiryTime, bo.OutcomeID, bo.Description, bo.Odds FROM Bets b LEFT JOIN BetOutcomes bo ON b.BetID = bo.BetID`
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
		var createdAtStr, expiryTimeStr string // Temporary string holders for timestamps

		// Adjusted Scan to include temporary string holders for the timestamps
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &createdAtStr, &expiryTimeStr, &outcome.OutcomeID, &outcome.Description, &outcome.Odds)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		// Convert string timestamps to time.Time using utility functions
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

		// Check if the bet already exists in the map
		if _, exists := betMap[&bet]; !exists {
			betMap[&bet] = []*models.BetOutcome{} // Initialize the slice if the bet is new
		}
		betMap[&bet] = append(betMap[&bet], &outcome) // Append outcomes to the bet
	}
	return betMap, nil
}

func (dao *UserDAO) GetBetsByCategory(category string) (map[*models.Bet][]*models.BetOutcome, error) {
	query := `SELECT b.BetID, b.Title, b.Description, b.OddsMultiplier, b.Status, b.Category, b.CreatedBy, b.CreatedAt, b.ExpiryTime, bo.OutcomeID, bo.Description, bo.Odds FROM Bets b LEFT JOIN BetOutcomes bo ON b.BetID = bo.BetID WHERE b.Category = ?`
	rows, err := dao.db.Query(query, category)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	betMap := make(map[*models.Bet][]*models.BetOutcome)
	uniqueBets := make(map[int]*models.Bet) // Track unique bets by their ID

	for rows.Next() {
		var bet models.Bet
		var outcome models.BetOutcome
		var createdAtStr, expiryTimeStr string // Temporary string holders for timestamps

		// Adjusting for direct scanning into time.Time fields
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &createdAtStr, &expiryTimeStr, &outcome.OutcomeID, &outcome.Description, &outcome.Odds)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		// Convert string timestamps to time.Time
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

		// Check if we've already seen this bet; if not, add it to uniqueBets and betMap
		if existingBet, ok := uniqueBets[bet.BetID]; !ok {
			uniqueBets[bet.BetID] = &bet
			betMap[&bet] = []*models.BetOutcome{&outcome}
		} else {
			// If we've seen this bet, append the outcome to the existing slice in betMap
			betMap[existingBet] = append(betMap[existingBet], &outcome)
		}
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
