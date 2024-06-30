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

func (dao *UserDAO) ReadUserTransactions(User *models.User) error {
	query := `SELECT * FROM Transactions WHERE UserID = ?`
	rows, err := dao.db.Query(query, User.UserID)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		transaction := new(models.Transaction)
		err := rows.Scan(&transaction.Amount, &transaction.Type, &transaction.Description, &transaction.TransactionDate)
		if err != nil {
			fmt.Println(err)
			return err
		}
		User.Transactions = append(User.Transactions, *transaction)
	}

	if err := rows.Err(); err != nil {
		fmt.Println(err)
		return err
	}

	return nil
}
