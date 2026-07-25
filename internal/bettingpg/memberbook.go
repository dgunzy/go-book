package bettingpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// MemberBookRow is one member's standing with the book, for the admin who is
// about to act on their behalf: what they hold, what they may still stake, and
// how their wagers fill.
type MemberBookRow struct {
	UserID string
	Name   string
	Email  string
	// Balance is their cash position: negative means they owe the book.
	Balance ledger.Money
	// CreditLimit is how far the balance may go negative; CreditAvailable is
	// what is left of it after the current balance.
	CreditLimit     ledger.Money
	CreditAvailable ledger.Money
	// AutoApproveLimit is the largest stake that fills without review for this
	// member, and AutoApprovePersonal says whether it is their own override
	// rather than the book default.
	AutoApproveLimit    ledger.Money
	AutoApprovePersonal bool
}

// MemberBook reads one member's balance, credit, and approval limit. It names
// a single member's money, so callers must gate it on an admin session.
func (s Store) MemberBook(ctx context.Context, userID string, defaultAutoApproveCents int64) (MemberBookRow, error) {
	if s.DB == nil {
		return MemberBookRow{}, errors.New("bettingpg: PostgreSQL pool is required")
	}
	if !isUUID(userID) {
		return MemberBookRow{}, fmt.Errorf("%w: user %s", betting.ErrNotFound, userID)
	}

	row := MemberBookRow{UserID: userID}
	var balanceCents, creditLimitCents int64
	var autoApprove *int64
	rows, err := s.DB.Query(ctx, `
		SELECT u.display_name, u.email, u.credit_limit_cents, u.wager_auto_approve_max_cents,
		       coalesce((SELECT b.balance_cents FROM ledger_account_balances b
		                 WHERE b.owner_user_id = u.id AND b.account_type = 'user_cash'
		                   AND b.currency::text = $2), 0)
		FROM users u WHERE u.id = $1::uuid`, userID, string(bookCurrency))
	if err != nil {
		return MemberBookRow{}, fmt.Errorf("load member book: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return MemberBookRow{}, fmt.Errorf("load member book: %w", err)
		}
		return MemberBookRow{}, fmt.Errorf("%w: user %s", betting.ErrNotFound, userID)
	}
	if err := rows.Scan(&row.Name, &row.Email, &creditLimitCents, &autoApprove, &balanceCents); err != nil {
		return MemberBookRow{}, fmt.Errorf("scan member book: %w", err)
	}
	rows.Close()

	row.Balance = ledger.Money{Cents: balanceCents, Currency: bookCurrency}
	row.CreditLimit = ledger.Money{Cents: creditLimitCents, Currency: bookCurrency}
	// What they may still stake is the credit line plus whatever they hold, or
	// nothing at all once the line is used up.
	available := creditLimitCents + balanceCents
	if available < 0 {
		available = 0
	}
	row.CreditAvailable = ledger.Money{Cents: available, Currency: bookCurrency}

	limit := defaultAutoApproveCents
	if autoApprove != nil {
		limit = *autoApprove
		row.AutoApprovePersonal = true
	}
	row.AutoApproveLimit = ledger.Money{Cents: limit, Currency: bookCurrency}
	return row, nil
}

// bookCurrency mirrors the dashboard's: the book reports in one currency
// rather than adding across them.
const bookCurrency = ledger.CAD
