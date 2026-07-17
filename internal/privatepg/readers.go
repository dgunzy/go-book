// Package privatepg implements the authenticated private UI read models with
// user-scoped PostgreSQL queries.
package privatepg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/privateweb"
	"github.com/jackc/pgx/v5"
)

type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Readers struct{ db DB }

func New(db DB) (*Readers, error) {
	if db == nil {
		return nil, errors.New("private PostgreSQL readers require a database")
	}
	return &Readers{db: db}, nil
}

func (r *Readers) DashboardSummary(ctx context.Context, userID string) (privateweb.DashboardSummary, error) {
	if err := requireUserID(userID); err != nil {
		return privateweb.DashboardSummary{}, err
	}
	balances, err := r.balanceRows(ctx, userID)
	if err != nil {
		return privateweb.DashboardSummary{}, fmt.Errorf("load balances: %w", err)
	}

	var open, pending, settled int64
	if err = r.db.QueryRow(ctx, dashboardCountsSQL, userID).Scan(&open, &pending, &settled); err != nil {
		return privateweb.DashboardSummary{}, fmt.Errorf("load wager summary: %w", err)
	}
	openCount, err := countToInt(open)
	if err != nil {
		return privateweb.DashboardSummary{}, err
	}
	pendingCount, err := countToInt(pending)
	if err != nil {
		return privateweb.DashboardSummary{}, err
	}
	settledCount, err := countToInt(settled)
	if err != nil {
		return privateweb.DashboardSummary{}, err
	}

	recent, err := r.ledgerRows(ctx, userID, 5)
	if err != nil {
		return privateweb.DashboardSummary{}, fmt.Errorf("load recent ledger activity: %w", err)
	}
	return privateweb.DashboardSummary{
		Balances: balances, OpenWagers: openCount, PendingWagers: pendingCount,
		SettledWagers: settledCount, RecentActivity: recent,
	}, nil
}

func (r *Readers) LedgerRows(ctx context.Context, userID string) ([]privateweb.LedgerRow, error) {
	if err := requireUserID(userID); err != nil {
		return nil, err
	}
	rows, err := r.ledgerRows(ctx, userID, 0)
	if err != nil {
		return nil, fmt.Errorf("load ledger rows: %w", err)
	}
	return rows, nil
}

func (r *Readers) WagerRows(ctx context.Context, userID string) ([]privateweb.WagerRow, error) {
	if err := requireUserID(userID); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, wagersSQL, userID)
	if err != nil {
		return nil, fmt.Errorf("query wagers: %w", err)
	}
	defer rows.Close()

	result := make([]privateweb.WagerRow, 0)
	for rows.Next() {
		var row privateweb.WagerRow
		var odds int32
		var stakeCents, potentialProfitCents int64
		var currencyCode string
		var legacyRow bool
		if err = rows.Scan(
			&row.PlacedAt, &row.Market, &row.Selection, &odds, &stakeCents,
			&currencyCode, &potentialProfitCents, &row.Status, &legacyRow,
		); err != nil {
			return nil, fmt.Errorf("scan wager: %w", err)
		}
		currency, err := ledger.ParseCurrency(strings.TrimSpace(currencyCode))
		if err != nil {
			return nil, fmt.Errorf("wager currency: %w", err)
		}
		row.Stake, err = ledger.NewMoney(stakeCents, currency)
		if err != nil {
			return nil, fmt.Errorf("wager stake: %w", err)
		}
		row.Odds, err = ledger.NewAmericanOdds(odds)
		if err != nil {
			return nil, fmt.Errorf("wager odds: %w", err)
		}
		if legacyRow {
			row.PotentialProfit, err = row.Odds.Profit(row.Stake)
		} else {
			row.PotentialProfit, err = ledger.NewMoney(potentialProfitCents, currency)
		}
		if err != nil {
			return nil, fmt.Errorf("wager potential profit: %w", err)
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wagers: %w", err)
	}
	return result, nil
}

func (r *Readers) ReconciliationSummary(ctx context.Context) (privateweb.AdminReconciliationSummary, error) {
	var summary privateweb.AdminReconciliationSummary
	var transactions, unbalanced, pending, failed int64
	var migrationDifference int64
	if err := r.db.QueryRow(ctx, reconciliationSQL).Scan(
		&summary.AsOf, &transactions, &unbalanced, &pending, &failed, &migrationDifference,
	); err != nil {
		return privateweb.AdminReconciliationSummary{}, fmt.Errorf("load reconciliation summary: %w", err)
	}
	counts := []*int{&summary.LedgerTransactions, &summary.UnbalancedTransactions, &summary.PendingOutboxEvents, &summary.FailedOutboxEvents}
	values := []int64{transactions, unbalanced, pending, failed}
	for i, value := range values {
		converted, err := countToInt(value)
		if err != nil {
			return privateweb.AdminReconciliationSummary{}, err
		}
		*counts[i] = converted
	}
	summary.LedgerBalanced = summary.UnbalancedTransactions == 0
	summary.MigrationDifference = ledger.Money{Cents: migrationDifference, Currency: ledger.CAD}
	return summary, nil
}

func (r *Readers) balanceRows(ctx context.Context, userID string) ([]privateweb.BalanceRow, error) {
	rows, err := r.db.Query(ctx, balancesSQL, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]privateweb.BalanceRow, 0)
	for rows.Next() {
		var accountType, accountName, currencyCode string
		var cents int64
		if err = rows.Scan(&accountType, &accountName, &currencyCode, &cents); err != nil {
			return nil, err
		}
		currency, err := ledger.ParseCurrency(strings.TrimSpace(currencyCode))
		if err != nil {
			return nil, err
		}
		amount, err := ledger.NewMoney(cents, currency)
		if err != nil {
			return nil, err
		}
		result = append(result, privateweb.BalanceRow{Label: balanceLabel(accountType), Account: accountName, Amount: amount})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Readers) ledgerRows(ctx context.Context, userID string, limit int) ([]privateweb.LedgerRow, error) {
	rows, err := r.db.Query(ctx, ledgerRowsSQL, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]privateweb.LedgerRow, 0)
	for rows.Next() {
		var row privateweb.LedgerRow
		var amountCents, balanceCents int64
		var currencyCode string
		if err = rows.Scan(
			&row.OccurredAt, &row.Description, &row.TransactionType, &row.Reference, &row.Account,
			&amountCents, &currencyCode, &balanceCents, &row.HasRunningBalance,
		); err != nil {
			return nil, err
		}
		currency, err := ledger.ParseCurrency(strings.TrimSpace(currencyCode))
		if err != nil {
			return nil, err
		}
		row.Amount, err = ledger.NewMoney(amountCents, currency)
		if err != nil {
			return nil, err
		}
		row.RunningBalance, err = ledger.NewMoney(balanceCents, currency)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func requireUserID(userID string) error {
	if strings.TrimSpace(userID) == "" {
		return errors.New("user ID is required")
	}
	return nil
}

func countToInt(value int64) (int, error) {
	if value < 0 || uint64(value) > uint64(math.MaxInt) {
		return 0, fmt.Errorf("database count %d is outside int range", value)
	}
	return int(value), nil
}

func balanceLabel(accountType string) string {
	switch accountType {
	case "user_cash":
		return "Available cash"
	case "user_free_play":
		return "Free play"
	default:
		return "Account balance"
	}
}

const balancesSQL = `
SELECT b.account_type, a.name, b.currency::text, b.balance_cents
FROM ledger_account_balances b
JOIN ledger_accounts a ON a.id = b.account_id
WHERE b.owner_user_id = $1::uuid
ORDER BY b.account_type, b.currency, b.account_id`

const dashboardCountsSQL = `
SELECT count(*) FILTER (WHERE state = 'accepted'),
       count(*) FILTER (WHERE state = 'pending'),
       count(*) FILTER (WHERE state = 'settled') +
           (SELECT count(*) FROM legacy_book_wagers WHERE user_id = $1::uuid AND approved)
FROM wagers
WHERE user_id = $1::uuid`

const ledgerRowsSQL = `
SELECT occurred_at, description, transaction_type, reference, account,
       amount_cents, currency::text, running_balance_cents, has_running_balance
FROM (
    SELECT t.occurred_at,
           coalesce(nullif(t.reason, ''), initcap(replace(t.transaction_type, '_', ' '))) AS description,
           t.transaction_type,
           concat(t.source_type, ':', coalesce(t.source_id::text, t.id::text)) AS reference,
           a.name AS account,
           p.amount_cents,
           t.currency,
           sum(p.amount_cents) OVER (
               PARTITION BY p.account_id
               ORDER BY t.occurred_at, t.id, p.id
               ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
           )::bigint AS running_balance_cents,
           true AS has_running_balance,
           t.id,
           p.id::text AS row_id,
           0 AS source_rank
    FROM ledger_postings p
    JOIN ledger_accounts a ON a.id = p.account_id
    JOIN ledger_transactions t ON t.id = p.transaction_id
    WHERE a.owner_user_id = $1::uuid
	UNION ALL
	SELECT lt.occurred_at, lt.description, 'legacy_' || lt.transaction_type,
	       'legacy-transaction:' || lt.source_transaction_id::text,
	       'Legacy archive', lt.amount_cents, lt.currency,
	       0::bigint, false, NULL::uuid, lt.source_transaction_id::text, 1
	FROM legacy_book_transactions lt
	WHERE lt.user_id = $1::uuid
) history
ORDER BY occurred_at DESC, source_rank, id DESC NULLS LAST, row_id DESC
LIMIT NULLIF($2, 0)`

const wagersSQL = `
SELECT placed_at, market, selection, accepted_american_odds, stake_cents,
       currency, potential_profit_cents, status, legacy_row
FROM (
    SELECT w.placed_at, m.title AS market, w.accepted_terms AS selection,
           w.accepted_american_odds, w.stake_cents, w.currency::text AS currency,
           w.potential_profit_cents, w.state AS status, false AS legacy_row,
           0 AS source_rank, w.id::text AS source_id
    FROM wagers w
    JOIN markets m ON m.id = w.market_id
    WHERE w.user_id = $1::uuid
    UNION ALL
    SELECT lw.placed_at, 'Legacy archive' AS market, lw.accepted_terms AS selection,
           lw.accepted_american_odds, lw.stake_cents, lw.currency::text AS currency,
           0::bigint AS potential_profit_cents,
           CASE WHEN lw.approved THEN 'legacy_' || lw.result ELSE 'legacy_unapproved' END AS status,
           true AS legacy_row, 1 AS source_rank, lw.source_wager_id::text AS source_id
    FROM legacy_book_wagers lw
    WHERE lw.user_id = $1::uuid
) history
ORDER BY placed_at DESC, source_rank, source_id DESC
`

const reconciliationSQL = `
WITH transaction_checks AS (
    SELECT t.id, t.expected_posting_count, count(p.id) AS posting_count,
           coalesce(sum(p.amount_cents), 0) AS posting_total,
           count(*) FILTER (WHERE a.currency <> t.currency) AS currency_mismatches
    FROM ledger_transactions t
    LEFT JOIN ledger_postings p ON p.transaction_id = t.id
    LEFT JOIN ledger_accounts a ON a.id = p.account_id
    GROUP BY t.id, t.expected_posting_count
)
SELECT statement_timestamp(),
       count(*)::bigint,
       count(*) FILTER (
           WHERE posting_count <> expected_posting_count
              OR posting_total <> 0
              OR currency_mismatches <> 0
       )::bigint,
       (SELECT count(*) FROM outbox_events WHERE state = 'pending')::bigint,
       (SELECT count(*) FROM outbox_events WHERE state = 'failed')::bigint,
       coalesce((
           SELECT sum(reconciliation_difference_cents)
           FROM legacy_book_user_mappings
           WHERE import_state = 'promoted'
       ), 0)::bigint
FROM transaction_checks`

var (
	_ privateweb.DashboardReader      = (*Readers)(nil)
	_ privateweb.LedgerReader         = (*Readers)(nil)
	_ privateweb.WagerReader          = (*Readers)(nil)
	_ privateweb.ReconciliationReader = (*Readers)(nil)
)
