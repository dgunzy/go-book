package dao

import (
	"fmt"

	"github.com/dgunzy/go-book/models"
)

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
