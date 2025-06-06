package dao

import (
	"fmt"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

func (dao *UserDAO) CreateBet(bet *models.Bet, BannedUsers []int) (betId int64, error error) {
	// Need to check that the descriptions are unique here
	// Check if the descriptions are unique
	betOutcomeMap := make(map[string]bool)

	for i := 0; i < len(bet.BetOutcomes); i++ {
		if _, ok := betOutcomeMap[bet.BetOutcomes[i].Description]; ok {
			return 0, fmt.Errorf("duplicate bet outcome description: %s", bet.BetOutcomes[i].Description)
		}
		betOutcomeMap[bet.BetOutcomes[i].Description] = true
	}
	createdAtSQLite := utils.GoToSQLite(bet.CreatedAt)
	expiryTimeSQLite := utils.GoToSQLite(bet.ExpiryTime)

	// Insert the bet into the database
	result, err := dao.db.Exec("INSERT INTO bets (title, description, OddsMultiplier, status, category, createdBy, createdAt, expiryTime) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		bet.Title, bet.Description, bet.OddsMultiplier, bet.Status, bet.Category, bet.CreatedBy, createdAtSQLite, expiryTimeSQLite)
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

	// Insert the banned users into the database if the slice is not empty
	if len(BannedUsers) > 0 {
		for _, user := range BannedUsers {
			_, err := dao.db.Exec("INSERT INTO bannedPlayers (userID, betID) VALUES (?, ?)", user, betID)
			if err != nil {
				fmt.Println(err)
				return betID, err
			}
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
func (dao *UserDAO) UpdateBet(bet *models.Bet, BannedUsers []int) error {
	// Start a transaction
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Check if the descriptions are unique
	betOutcomeMap := make(map[string]bool)
	for _, outcome := range bet.BetOutcomes {
		if _, ok := betOutcomeMap[outcome.Description]; ok {
			return fmt.Errorf("duplicate bet outcome description: %s", outcome.Description)
		}
		betOutcomeMap[outcome.Description] = true
	}

	expiryTimeSQLite := utils.GoToSQLite(bet.ExpiryTime)

	// Update the bet in the database
	_, err = tx.Exec("UPDATE bets SET title = ?, description = ?, OddsMultiplier = ?, status = ?, category = ?, expiryTime = ? WHERE BetID = ?",
		bet.Title, bet.Description, bet.OddsMultiplier, bet.Status, bet.Category, expiryTimeSQLite, bet.BetID)
	if err != nil {
		return err
	}

	// Delete existing outcomes for this bet
	_, err = tx.Exec("DELETE FROM betOutcomes WHERE betId = ?", bet.BetID)
	if err != nil {
		return err
	}

	// Insert the updated bet outcomes
	for _, outcome := range bet.BetOutcomes {
		_, err := tx.Exec("INSERT INTO betOutcomes (betId, description, odds) VALUES (?, ?, ?)",
			bet.BetID, outcome.Description, outcome.Odds)
		if err != nil {
			return err
		}
	}

	// // Delete existing banned users for this bet
	// _, err = tx.Exec("DELETE FROM bannedPlayers WHERE betID = ?", bet.BetID)
	// if err != nil {
	// 	return err
	// }

	// // Insert the updated banned users
	// if len(BannedUsers) > 0 {
	// 	for _, user := range BannedUsers {
	// 		_, err := tx.Exec("INSERT INTO bannedPlayers (userID, betID) VALUES (?, ?)", user, bet.BetID)
	// 		if err != nil {
	// 			return err
	// 		}
	// 	}
	// }

	// Commit the transaction
	return tx.Commit()
}

func (dao *UserDAO) DeleteBet(betID int) error {
	query := `UPDATE Bets SET Status = ? WHERE BetID = ?`
	_, err := dao.db.Exec(query, "closed", betID)
	return err
}

func (dao *UserDAO) GetAllBets() (*[]models.Bet, error) {
	query := `
		SELECT b.BetID, b.Title, b.Description, b.OddsMultiplier, b.Status, b.Category, b.CreatedBy, b.CreatedAt, b.ExpiryTime
		FROM Bets b
		WHERE b.Status != 'closed'
	`
	rows, err := dao.db.Query(query)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	var bets []models.Bet
	var ExpiryTime string
	var CreatedAt string

	for rows.Next() {
		var bet models.Bet
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &CreatedAt, &ExpiryTime)
		if err != nil {
			return nil, err
		}
		bet.CreatedAt, err = utils.SQLiteToGo(CreatedAt)
		if err != nil {
			fmt.Println("Error parsing CreatedAt:", err)
			return nil, err
		}
		bet.ExpiryTime, err = utils.SQLiteToGo(ExpiryTime)
		if err != nil {
			fmt.Println("Error parsing ExpiryTime:", err)
			return nil, err
		}

		// Fetch bet outcomes for each bet
		outcomesQuery := `
            SELECT Description, Odds
            FROM BetOutcomes
            WHERE BetID = ?
        `
		outcomeRows, err := dao.db.Query(outcomesQuery, bet.BetID)
		if err != nil {
			return nil, err
		}

		var outcomes []models.BetOutcomes
		for outcomeRows.Next() {
			var outcome models.BetOutcomes
			err := outcomeRows.Scan(&outcome.Description, &outcome.Odds)
			if err != nil {
				outcomeRows.Close()
				return nil, err
			}
			outcomes = append(outcomes, outcome)
		}
		outcomeRows.Close()

		bet.BetOutcomes = outcomes
		bets = append(bets, bet)
	}

	return &bets, nil
}

func (dao *UserDAO) GetBetsByCategory(category string) (*[]models.Bet, error) {
	query := `
        SELECT b.BetID, b.Title, b.Description, b.OddsMultiplier, b.Status, b.Category, b.CreatedBy, b.CreatedAt, b.ExpiryTime
        FROM Bets b
        WHERE b.Category = ? AND b.Status != 'closed'
    `
	rows, err := dao.db.Query(query, category)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	var bets []models.Bet
	var ExpiryTime string
	var CreatedAt string

	for rows.Next() {
		var bet models.Bet
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &CreatedAt, &ExpiryTime)
		if err != nil {
			return nil, err
		}
		bet.CreatedAt, err = utils.SQLiteToGo(CreatedAt)
		if err != nil {
			fmt.Println("Error parsing CreatedAt:", err)
			return nil, err
		}
		bet.ExpiryTime, err = utils.SQLiteToGo(ExpiryTime)
		if err != nil {
			fmt.Println("Error parsing ExpiryTime:", err)
			return nil, err
		}

		// Fetch bet outcomes for each bet
		outcomesQuery := `
            SELECT Description, Odds
            FROM BetOutcomes
            WHERE BetID = ?
        `
		outcomeRows, err := dao.db.Query(outcomesQuery, bet.BetID)
		if err != nil {
			return nil, err
		}

		var outcomes []models.BetOutcomes
		for outcomeRows.Next() {
			var outcome models.BetOutcomes
			err := outcomeRows.Scan(&outcome.Description, &outcome.Odds)
			if err != nil {
				outcomeRows.Close()
				return nil, err
			}
			outcomes = append(outcomes, outcome)
		}
		outcomeRows.Close()

		bet.BetOutcomes = outcomes
		bets = append(bets, bet)
	}

	return &bets, nil
}

// GetAllBetsByCategory returns all bets for a given category, excluding bets the user is banned from.
func (dao *UserDAO) GetAllLegalBetsByCategory(category *string, userID int) (*[]models.Bet, error) {
	query := `
		SELECT b.BetID, b.Title, b.Description, b.OddsMultiplier, b.Status, b.Category, b.CreatedBy, b.CreatedAt, b.ExpiryTime
		FROM Bets b
		WHERE b.BetID NOT IN (
			SELECT bp.BetID
			FROM BannedPlayers bp
			WHERE bp.UserID = ?
		)
		AND b.ExpiryTime > CURRENT_TIMESTAMP
		AND b.Status != 'closed'
	`
	params := []interface{}{userID}

	if category != nil {
		query += " AND b.Category = ?"
		params = append(params, *category)
	}

	rows, err := dao.db.Query(query, params...)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	var bets []models.Bet
	var ExpiryTime string
	var CreatedAt string

	for rows.Next() {
		var bet models.Bet
		err := rows.Scan(&bet.BetID, &bet.Title, &bet.Description, &bet.OddsMultiplier, &bet.Status, &bet.Category, &bet.CreatedBy, &CreatedAt, &ExpiryTime)
		if err != nil {
			return nil, err
		}
		bet.CreatedAt, err = utils.SQLiteToGo(CreatedAt)
		if err != nil {
			fmt.Println("Error parsing CreatedAt:", err)
			return nil, err
		}
		bet.ExpiryTime, err = utils.SQLiteToGo(ExpiryTime)
		if err != nil {
			fmt.Println("Error parsing ExpiryTime:", err)
			return nil, err
		}
		// Fetch bet outcomes for each bet
		outcomesQuery := `
			SELECT Description, Odds
			FROM BetOutcomes
			WHERE BetID = ?
		`
		outcomeRows, err := dao.db.Query(outcomesQuery, bet.BetID)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		defer outcomeRows.Close()

		for outcomeRows.Next() {
			var outcome models.BetOutcomes
			err := outcomeRows.Scan(&outcome.Description, &outcome.Odds)
			if err != nil {
				return nil, err
			}
			bet.BetOutcomes = append(bet.BetOutcomes, outcome)
		}

		bets = append(bets, bet)
	}

	return &bets, nil
}

// CreateBannedPlayer adds a new banned player to a bet.
func (dao *UserDAO) CreateBannedPlayer(userID, betID int) error {
	insertQuery := `
        INSERT INTO BannedPlayers (UserID, BetID)
        VALUES (?, ?)
    `
	_, err := dao.db.Exec(insertQuery, userID, betID)
	if err != nil {
		return err
	}
	return nil
}

// DeleteBannedPlayer removes a banned player from a bet.
func (dao *UserDAO) DeleteBannedPlayer(userID, betID int) error {
	deleteQuery := `
        DELETE FROM BannedPlayers
        WHERE UserID = ? AND BetID = ?
    `
	_, err := dao.db.Exec(deleteQuery, userID, betID)
	if err != nil {
		return err
	}
	return nil
}

func (dao *UserDAO) GetBannedPlayers(betID int) (*[]models.User, error) {
	query := `
		SELECT u.UserID, u.Username, u.Email, u.Role, u.Balance, u.FreePlayBalance, u.AutoApproveLimit
		FROM Users u
		JOIN BannedPlayers bp ON u.UserID = bp.UserID
		WHERE bp.BetID = ?
	`
	rows, err := dao.db.Query(query, betID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bannedPlayers := make([]models.User, 0)

	for rows.Next() {
		var user models.User
		err := rows.Scan(&user.UserID, &user.Username, &user.Email, &user.Role, &user.Balance, &user.FreePlayBalance, &user.AutoApproveLimit)
		if err != nil {
			return nil, err
		}
		bannedPlayers = append(bannedPlayers, user)
	}

	return &bannedPlayers, nil
}
