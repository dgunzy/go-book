package dao

import (
	"fmt"
	"time"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

func (dao *UserDAO) GetUserBets(userEmail string) ([]*models.UserBet, error) {
	var userBets []*models.UserBet

	// Query to get user ID based on email
	userIDQuery := "SELECT UserID FROM Users WHERE Email = ?"
	var userID int
	err := dao.db.QueryRow(userIDQuery, userEmail).Scan(&userID)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	// Query to get user bets based on user ID
	query := `SELECT Amount, PlacedAt, Result, BetDescription, Odds, BetId, UserID, Approved FROM UserBets WHERE UserID = ?`
	rows, err := dao.db.Query(query, userID)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ub models.UserBet
		err := rows.Scan(&ub.Amount, &ub.PlacedAt, &ub.Result, &ub.BetDescription, &ub.Odds, &ub.BetId, &ub.UserID, &ub.Approved)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		userBets = append(userBets, &ub)
	}

	if err = rows.Err(); err != nil {
		fmt.Println(err)
		return nil, err
	}

	return userBets, nil
}

func (dao *UserDAO) PlaceBet(userBet models.UserBet) error {
	tx, err := dao.db.Begin()
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer tx.Rollback()

	dbTime := utils.GoToSQLite(userBet.PlacedAt)

	// Insert the new bet into the UserBets table
	insertQuery := `INSERT INTO UserBets (UserID, Amount, BetDescription, Odds, Result, PlacedAt, Approved) VALUES (?, ?, ?, ?, 'ungraded', ?, ?)`
	_, err = tx.Exec(insertQuery, userBet.UserID, userBet.Amount, userBet.BetDescription, userBet.Odds, dbTime, userBet.Approved)
	if err != nil {
		fmt.Println(err)
		return err
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		fmt.Println(err)
		return err
	}

	return nil
}

func (dao *UserDAO) GradeUBet(betID int, winningOutcomeDescription string) error {
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

	// Update the UserBets table to mark the bet as graded with the result
	betsToGrade, err := dao.GetUserBetsByBetID(betID)
	if err != nil {
		return err
	}

	for _, betToGrade := range betsToGrade {
		if betToGrade.Result != "ungraded" {
			return fmt.Errorf("bet %d has already been graded", betID)
		}

		var wonBet bool
		if betToGrade.BetDescription == winningOutcomeDescription {
			betToGrade.Result = "win"
			wonBet = true
		} else {
			betToGrade.Result = "loss"
			wonBet = false
		}

		// Create a transaction for the bet result
		var user models.User
		user.UserID = betToGrade.UserID
		var transactionAmount float64
		var resultType, betDescription string
		if wonBet {
			transactionAmount = betToGrade.Amount * betToGrade.Odds
			resultType = "credit"
			betDescription = fmt.Sprintf("Bet %d result: %s Win.", betID, winningOutcomeDescription)
		} else {
			transactionAmount = -betToGrade.Amount
			resultType = "debit"
			betDescription = fmt.Sprintf("Bet %d  result: %s Loss.", betID, winningOutcomeDescription)

		}
		transaction := models.Transaction{
			Amount:          transactionAmount,
			Type:            resultType,
			Description:     betDescription,
			TransactionDate: time.Now(),
		}

		_, err = dao.CreateTransaction(user, transaction)
		if err != nil {
			return err
		}
	}
	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		return err
	}
	return nil
}

func (dao *UserDAO) GetUserBetsByBetID(betID int) ([]*models.UserBet, error) {
	var userBets []*models.UserBet
	query := `SELECT Amount, PlacedAt, Result, BetDescription, Odds, BetId FROM UserBets WHERE UserBetID = ?`
	rows, err := dao.db.Query(query, betID)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ub models.UserBet
		err := rows.Scan(&ub.Amount, &ub.PlacedAt, &ub.Result, &ub.BetDescription, &ub.Odds, &ub.BetId)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		userBets = append(userBets, &ub)
	}
	if err = rows.Err(); err != nil {
		fmt.Println(err)
		return nil, err
	}
	return userBets, nil
}

// ApproveOrRejectUserBet updates the approved status of a UserBet based on the provided betID and status.
func (dao *UserDAO) ApproveOrRejectUserBet(betID int, status bool) error {
	query := `UPDATE UserBets SET Approved = ? WHERE BetId = ?`
	_, err := dao.db.Exec(query, status, betID)
	if err != nil {
		fmt.Println(err)
		return err
	}
	return nil
}

// RemoveUnapprovedBet deletes a UserBet if its approved status is false.
func (dao *UserDAO) RemoveUnapprovedBet(betID int) error {
	query := `DELETE FROM UserBets WHERE BetId = ? AND Approved = false`
	_, err := dao.db.Exec(query, betID)
	if err != nil {
		fmt.Println(err)
		return err
	}
	return nil
}
