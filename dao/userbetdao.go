package dao

import (
	"database/sql"
	"fmt"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

func (dao *UserDAO) PlaceBet(userBet models.UserBet) error {
	tx, err := dao.db.Begin()
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer tx.Rollback()

	dbTime := utils.GoToSQLite(userBet.PlacedAt)

	insertQuery := `INSERT INTO UserBets (UserID, Amount, BetDescription, Odds, Result, PlacedAt, Approved) VALUES (?, ?, ?, ?, 'ungraded', ?, ?)`
	_, err = tx.Exec(insertQuery, userBet.UserID, userBet.Amount, userBet.BetDescription, userBet.Odds, dbTime, userBet.Approved)
	if err != nil {
		fmt.Println(err)
		return err
	}

	return tx.Commit()
}

func (dao *UserDAO) GetAllUserBets() ([]*models.UserBet, error) {
	var userBets []*models.UserBet
	query := `
		SELECT UserBetID, Amount, PlacedAt, Result, BetDescription, Odds, UserID, Approved 
		FROM UserBets
		ORDER BY 
			CASE 
				WHEN Result = 'ungraded' THEN 0 
				WHEN Result = 'win' THEN 1
				WHEN Result = 'loss' THEN 2
				ELSE 3
			END
	`
	rows, err := dao.db.Query(query)
	if err != nil {
		fmt.Println("Error querying all user bets:", err)
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ub models.UserBet
		var placedAtStr string
		err := rows.Scan(&ub.UserBetID, &ub.Amount, &placedAtStr, &ub.Result, &ub.BetDescription, &ub.Odds, &ub.UserID, &ub.Approved)
		if err != nil {
			fmt.Println("Error scanning user bet row:", err)
			return nil, err
		}

		ub.PlacedAt, err = utils.SQLiteToGo(placedAtStr)
		if err != nil {
			fmt.Printf("Error parsing PlacedAt time: %v\n", err)
			return nil, err
		}

		userBets = append(userBets, &ub)
	}

	if err = rows.Err(); err != nil {
		fmt.Println("Error after iterating user bet rows:", err)
		return nil, err
	}

	return userBets, nil
}

func (dao *UserDAO) DeleteUserBetByID(betID int) error {
	query := `DELETE FROM UserBets WHERE UserBetID = ?`
	_, err := dao.db.Exec(query, betID)
	if err != nil {
		fmt.Println("Error deleting bet:", err)
		return err
	}
	return nil
}

func (dao *UserDAO) ApproveUserBet(betID int) error {
	query := `UPDATE UserBets SET Approved = TRUE WHERE UserBetID = ?`
	_, err := dao.db.Exec(query, betID)
	if err != nil {
		fmt.Println("Error approving bet:", err)
		return err
	}
	return nil
}

func (dao *UserDAO) GradeUserBet(userBetID int, result string) (models.UserBet, error) {
	var gradedBet models.UserBet
	if result != "win" && result != "lose" && result != "tie" {
		return gradedBet, fmt.Errorf("invalid result: must be 'win' or 'lose' or 'tie'")
	}

	query := `UPDATE UserBets SET Result = ? WHERE UserBetID = ? AND Result = 'ungraded'`
	res, err := dao.db.Exec(query, result, userBetID)
	if err != nil {
		fmt.Println("Error grading bet:", err)
		return gradedBet, err
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		fmt.Println("Error getting rows affected:", err)
		return gradedBet, err
	}

	if rowsAffected == 0 {
		return gradedBet, fmt.Errorf("no ungraded bet found with ID %d", userBetID)
	}

	userBet, err := dao.GetUserBetID(userBetID)
	if err != nil {
		fmt.Println("Error getting user bet:", err)
		return gradedBet, err
	}

	return *userBet, nil
}

func (dao *UserDAO) GetUserBetID(userBetId int) (*models.UserBet, error) {
	gradedBet := new(models.UserBet)
	var placedAtStr string
	query := `SELECT UserBetID, Amount, PlacedAt, Result, BetDescription, Odds, UserID, Approved FROM UserBets WHERE UserBetID = ?`

	err := dao.db.QueryRow(query, userBetId).Scan(
		&gradedBet.UserBetID,
		&gradedBet.Amount,
		&placedAtStr,
		&gradedBet.Result,
		&gradedBet.BetDescription,
		&gradedBet.Odds,
		&gradedBet.UserID,
		&gradedBet.Approved,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no bet found with ID %d", userBetId)
		}
		fmt.Println("Error retrieving user bet:", err)
		return nil, err
	}

	gradedBet.PlacedAt, err = utils.SQLiteToGo(placedAtStr)
	if err != nil {
		fmt.Printf("Error parsing PlacedAt time: %v\n", err)
		return nil, err
	}

	return gradedBet, nil
}
