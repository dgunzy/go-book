package dao

import (
	"fmt"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

// Transaction Create/Read operations
func (dao *UserDAO) CreateTransaction(User models.User, transaction models.Transaction) (userID int, error error) {
	// Set the transaction date to now before inserting

	transactionDateString := utils.GoToSQLite(transaction.TransactionDate)
	query := `INSERT INTO Transactions (UserID, Amount, Type, Description, TransactionDate) VALUES (?, ?, ?, ?, ?)`
	_, err := dao.db.Exec(query, User.UserID, transaction.Amount, transaction.Type, transaction.Description, transactionDateString)
	if err != nil {
		fmt.Println(err)
		return 0, err
	}

	if err := dao.AdjustUserBalance(User.UserID, transaction.Amount); err != nil {
		fmt.Println("Error adjusting user balance: ", err)
		return User.UserID, err
	}

	return User.UserID, nil
}

func (dao *UserDAO) ReadUserTransactions(UserId int, Transaction *[]models.Transaction) error {
	query := `SELECT * FROM Transactions WHERE UserID = ?`
	rows, err := dao.db.Query(query, UserId)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var CreatedAt string
		var ignore interface{}
		transaction := new(models.Transaction)
		err := rows.Scan(&ignore, &ignore, &transaction.Amount, &transaction.Type, &transaction.Description, &CreatedAt)

		if err != nil {
			fmt.Println(err)
			return err
		}

		transaction.TransactionDate, err = utils.SQLiteToGo(CreatedAt)
		if err != nil {
			fmt.Println(err)
			return err
		}
		*Transaction = append(*Transaction, *transaction)
	}

	if err := rows.Err(); err != nil {
		fmt.Println(err)
		return err
	}

	return nil
}
