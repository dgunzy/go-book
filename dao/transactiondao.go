package dao

import (
	"fmt"

	"github.com/dgunzy/go-book/models"
	"github.com/dgunzy/go-book/utils"
)

// Transaction Create/Read operations
func (dao *UserDAO) CreateTransaction(transaction *models.Transaction) (int, error) {
	// Set the transaction date to now before inserting

	transactionDateString := utils.GoToSQLite(transaction.TransactionDate)
	query := `INSERT INTO Transactions (UserID, Amount, Type, Description, TransactionDate) VALUES (?, ?, ?, ?, ?)`
	_, err := dao.db.Exec(query, transaction.UserID, transaction.Amount, transaction.Type, transaction.Description, transactionDateString)
	if err != nil {
		fmt.Println(err)
		return 0, err
	}

	if err := dao.AdjustUserBalance(transaction.UserID, transaction.Amount); err != nil {
		fmt.Println("Error adjusting user balance: ", err)
		return transaction.UserID, err
	}

	return transaction.UserID, nil
}

func (dao *UserDAO) ReadTransaction(transactionID int) (*models.Transaction, error) {
	transaction := new(models.Transaction)
	query := `SELECT * FROM Transactions WHERE TransactionID = ?`
	err := dao.db.QueryRow(query, transactionID).Scan(&transaction.TransactionID, &transaction.UserID, &transaction.Amount, &transaction.Type, &transaction.Description, &transaction.TransactionDate)
	return transaction, err
}
