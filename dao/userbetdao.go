package dao

import (
	"fmt"

	"github.com/dgunzy/go-book/models"
)

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
