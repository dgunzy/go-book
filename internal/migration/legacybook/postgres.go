package legacybook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type PostgresQueryDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type PostgresDB interface {
	PostgresQueryDB
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// PostgresSource reads only the normalized archived tables. The fixed schema
// prevents a caller-controlled SQL identifier from entering the query.
type PostgresSource struct{ DB PostgresQueryDB }

func (s PostgresSource) Users(ctx context.Context) ([]LegacyUser, error) {
	if s.DB == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT user_id, coalesce(username, ''), coalesce(email, ''),
		       coalesce(role, ''), coalesce(balance::text, '0'),
		       coalesce(free_play_balance::text, '0')
		FROM legacy_cabot_book.users
		ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]LegacyUser, 0)
	for rows.Next() {
		var user LegacyUser
		if err := rows.Scan(&user.ID, &user.DisplayName, &user.Email, &user.Role, &user.Balance, &user.FreePlayBalance); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s PostgresSource) Transactions(ctx context.Context) ([]LegacyTransaction, error) {
	if s.DB == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT transaction_id, user_id, coalesce(amount::text, ''), coalesce(transaction_type, ''),
		       coalesce(description, ''), transaction_date::timestamptz
		FROM legacy_cabot_book.transactions
		ORDER BY transaction_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	transactions := make([]LegacyTransaction, 0)
	for rows.Next() {
		var transaction LegacyTransaction
		if err := rows.Scan(&transaction.ID, &transaction.UserID, &transaction.Amount, &transaction.Type, &transaction.Description, &transaction.OccurredAt); err != nil {
			return nil, err
		}
		transactions = append(transactions, transaction)
	}
	return transactions, rows.Err()
}

func (s PostgresSource) Wagers(ctx context.Context) ([]LegacyWager, error) {
	if s.DB == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT user_bet_id, user_id, coalesce(amount::text, ''), coalesce(bet_description, ''),
		       coalesce(odds::text, ''), placed_at::timestamptz, coalesce(result, ''), approved
		FROM legacy_cabot_book.user_bets
		ORDER BY user_bet_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wagers := make([]LegacyWager, 0)
	for rows.Next() {
		var wager LegacyWager
		if err := rows.Scan(&wager.ID, &wager.UserID, &wager.Amount, &wager.Description, &wager.Odds, &wager.PlacedAt, &wager.Result, &wager.Approved); err != nil {
			return nil, err
		}
		wagers = append(wagers, wager)
	}
	return wagers, rows.Err()
}

type PostgresStore struct{ DB PostgresDB }

func (s PostgresStore) WithinTransaction(ctx context.Context, operation func(Repository) error) error {
	if s.DB == nil {
		return errors.New("PostgreSQL pool is required")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, SourceSystem); err != nil {
		return err
	}
	if err = operation(postgresRepository{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type postgresRepository struct{ tx pgx.Tx }

func (r postgresRepository) EnsureApprovedUser(ctx context.Context, batchID string, input UserInput) (string, error) {
	expectedChecksum, err := checksum(input)
	if err != nil {
		return "", err
	}
	var userID, storedChecksum string
	err = r.tx.QueryRow(ctx, `
		SELECT target_id::text, source_checksum FROM legacy_import_records
		WHERE migration_batch_id = $1::uuid AND source_table = 'Users' AND source_primary_key = $2`,
		batchID, fmt.Sprint(input.LegacyUserID)).Scan(&userID, &storedChecksum)
	if err == nil {
		if storedChecksum != expectedChecksum {
			return "", fmt.Errorf("legacy user %d changed after import", input.LegacyUserID)
		}
		if _, err = r.verifyUser(ctx, userID, input); err != nil {
			return "", err
		}
		if err = r.ensureMembership(ctx, userID, input.Role, input.ActorUserID); err != nil {
			return "", err
		}
		if err = r.ensureUserMapping(ctx, batchID, userID, expectedChecksum, input); err != nil {
			return "", err
		}
		return userID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	if input.Email != "" {
		err = r.tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(email) = lower($1)`, input.Email).Scan(&userID)
	}
	if input.Email == "" || errors.Is(err, pgx.ErrNoRows) {
		err = r.tx.QueryRow(ctx, `
			INSERT INTO users (display_name, email, status)
			VALUES ($1, nullif($2, ''), 'active') RETURNING id::text`,
			input.DisplayName, input.Email).Scan(&userID)
	}
	if err != nil {
		return "", err
	}
	if _, err = r.verifyUser(ctx, userID, input); err != nil {
		return "", err
	}

	if err = r.ensureMembership(ctx, userID, input.Role, input.ActorUserID); err != nil {
		return "", err
	}

	_, err = r.tx.Exec(ctx, `
		INSERT INTO legacy_import_records
		(migration_batch_id, source_table, source_primary_key, target_table, target_id, source_checksum, import_state, imported_at)
		VALUES ($1::uuid, 'Users', $2, 'users', $3::uuid, $4, 'imported', now())
		ON CONFLICT (migration_batch_id, source_table, source_primary_key) DO NOTHING`,
		batchID, fmt.Sprint(input.LegacyUserID), userID, expectedChecksum)
	if err != nil {
		return "", err
	}
	if err = r.ensureUserMapping(ctx, batchID, userID, expectedChecksum, input); err != nil {
		return "", err
	}
	return userID, nil
}

func (r postgresRepository) ensureUserMapping(ctx context.Context, batchID, userID, expectedChecksum string, input UserInput) error {
	_, err := r.tx.Exec(ctx, `
		INSERT INTO legacy_book_user_mappings
		(source_user_id, migration_batch_id, user_id, source_checksum, currency,
		 balance_cents, free_play_balance_cents, transaction_net_cents, reconciliation_difference_cents,
		 import_state, imported_at)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, 'promoted', now())
		ON CONFLICT (source_user_id) DO NOTHING`, input.LegacyUserID, batchID, userID,
		expectedChecksum, input.Currency, input.BalanceCents, input.FreePlayBalanceCents,
		input.TransactionNetCents, input.DifferenceCents)
	if err != nil {
		return err
	}
	return r.verifyUserMapping(ctx, batchID, userID, expectedChecksum, input)
}

func (r postgresRepository) verifyUserMapping(ctx context.Context, batchID, userID, expectedChecksum string, input UserInput) error {
	var storedBatch, storedUser, storedChecksum, storedCurrency, state string
	var cash, freePlay, net, difference int64
	err := r.tx.QueryRow(ctx, `
		SELECT migration_batch_id::text, user_id::text, source_checksum, currency::text, balance_cents,
		       free_play_balance_cents, transaction_net_cents, reconciliation_difference_cents, import_state
		FROM legacy_book_user_mappings WHERE source_user_id = $1`, input.LegacyUserID).
		Scan(&storedBatch, &storedUser, &storedChecksum, &storedCurrency, &cash, &freePlay, &net, &difference, &state)
	if err != nil {
		return err
	}
	if storedBatch != batchID || storedUser != userID || storedChecksum != expectedChecksum || strings.TrimSpace(storedCurrency) != input.Currency || cash != input.BalanceCents || freePlay != input.FreePlayBalanceCents || net != input.TransactionNetCents || difference != input.DifferenceCents || state != "promoted" {
		return fmt.Errorf("legacy user mapping %d does not match requested promotion", input.LegacyUserID)
	}
	return nil
}

func (r postgresRepository) ensureMembership(ctx context.Context, userID, role, actorUserID string) error {
	var activeRole string
	err := r.tx.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID).Scan(&activeRole)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = r.tx.Exec(ctx, `INSERT INTO memberships (user_id, role, granted_by) VALUES ($1::uuid, $2, $3::uuid)`, userID, role, actorUserID)
		return err
	}
	if err != nil {
		return err
	}
	if activeRole != role {
		return fmt.Errorf("existing active membership role %q does not match %q", activeRole, role)
	}
	return nil
}

func (r postgresRepository) verifyUser(ctx context.Context, userID string, input UserInput) (string, error) {
	var displayName, email, status string
	err := r.tx.QueryRow(ctx, `SELECT display_name, coalesce(email, ''), status FROM users WHERE id = $1::uuid`, userID).Scan(&displayName, &email, &status)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(email), input.Email) || status != "active" {
		return "", fmt.Errorf("existing user identity/status does not match legacy user %d", input.LegacyUserID)
	}
	if strings.TrimSpace(displayName) == "" {
		return "", errors.New("existing user has an empty display name")
	}
	return userID, nil
}

func (r postgresRepository) EnsureOpeningBalance(ctx context.Context, batchID string, input OpeningBalanceInput) error {
	userPosting, equityPosting, err := openingPostings(input.AmountCents)
	if err != nil {
		return err
	}
	var userAccountID, equityAccountID string
	err = r.tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (owner_user_id, account_type, currency, name)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (owner_user_id, account_type, currency) WHERE owner_user_id IS NOT NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, input.UserID, input.AccountType, input.Currency, fmt.Sprintf("Legacy user %d %s", input.LegacyUserID, input.AccountType)).Scan(&userAccountID)
	if err != nil {
		return err
	}
	err = r.tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, name)
		VALUES ('migration_equity', $1, 'Legacy migration equity')
		ON CONFLICT (account_type, currency) WHERE owner_user_id IS NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, input.Currency).Scan(&equityAccountID)
	if err != nil {
		return err
	}

	var transactionID string
	err = r.tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
		(transaction_type, currency, idempotency_key, source_type, actor_user_id, reason, expected_posting_count, occurred_at)
		VALUES ('migration_adjustment', $1, $2, $3, nullif($4, '')::uuid, $5, 2, now())
		ON CONFLICT (currency, idempotency_key) DO NOTHING
		RETURNING id::text`, input.Currency, input.IdempotencyKey, SourceSystem, input.ActorUserID, input.Reason).Scan(&transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.verifyOpeningBalance(ctx, batchID, input, userAccountID, equityAccountID)
	}
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `INSERT INTO ledger_postings (transaction_id, account_id, amount_cents) VALUES ($1::uuid, $2::uuid, $3), ($1::uuid, $4::uuid, $5)`, transactionID, userAccountID, userPosting, equityAccountID, equityPosting)
	if err != nil {
		return err
	}

	checksum, err := checksum(input)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
		INSERT INTO legacy_import_records
		(migration_batch_id, source_table, source_primary_key, target_table, target_id, source_checksum, import_state, imported_at)
		VALUES ($1::uuid, 'UserBalances', $2, 'ledger_transactions', $3::uuid, $4, 'imported', now())
		ON CONFLICT (migration_batch_id, source_table, source_primary_key) DO NOTHING`, batchID,
		fmt.Sprintf("%d:%s", input.LegacyUserID, input.AccountType), transactionID, checksum)
	if err != nil {
		return err
	}
	return r.verifyOpeningBalance(ctx, batchID, input, userAccountID, equityAccountID)
}

func openingPostings(amountCents int64) (user int64, equity int64, err error) {
	if amountCents == 0 {
		return 0, 0, errors.New("opening balance must be non-zero")
	}
	if amountCents == -9223372036854775807-1 {
		return 0, 0, errors.New("opening balance cannot be represented by an opposite equity posting")
	}
	return amountCents, -amountCents, nil
}

func (r postgresRepository) EnsureLegacyTransaction(ctx context.Context, batchID, userID string, record TransactionRecord) error {
	sourceChecksum, err := checksum(record)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
		INSERT INTO legacy_book_transactions
		(source_transaction_id, migration_batch_id, user_id, amount_cents, transaction_type,
		 currency, description, occurred_at, source_checksum)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (source_transaction_id) DO NOTHING`, record.SourceTransactionID, batchID,
		userID, record.AmountCents, record.TransactionType, record.Currency, record.Description, record.OccurredAt, sourceChecksum)
	if err != nil {
		return err
	}
	var storedBatch, storedUser, storedType, storedCurrency, storedDescription, storedChecksum string
	var storedAmount int64
	var storedAt time.Time
	err = r.tx.QueryRow(ctx, `
		SELECT migration_batch_id::text, user_id::text, amount_cents, transaction_type,
		       currency::text, description, occurred_at, source_checksum
		FROM legacy_book_transactions WHERE source_transaction_id = $1`, record.SourceTransactionID).
		Scan(&storedBatch, &storedUser, &storedAmount, &storedType, &storedCurrency, &storedDescription, &storedAt, &storedChecksum)
	if err != nil {
		return err
	}
	if storedBatch != batchID || storedUser != userID || storedAmount != record.AmountCents || storedType != record.TransactionType || strings.TrimSpace(storedCurrency) != record.Currency || storedDescription != record.Description || !storedAt.Equal(record.OccurredAt) || storedChecksum != sourceChecksum {
		return fmt.Errorf("legacy transaction %d does not match existing immutable history", record.SourceTransactionID)
	}
	return nil
}

func (r postgresRepository) EnsureLegacyWager(ctx context.Context, batchID, userID string, record WagerRecord) error {
	sourceChecksum, err := checksum(record)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
		INSERT INTO legacy_book_wagers
		(source_wager_id, migration_batch_id, user_id, stake_cents, accepted_terms,
		 currency, accepted_american_odds, placed_at, result, approved, source_checksum)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (source_wager_id) DO NOTHING`, record.SourceWagerID, batchID, userID,
		record.StakeCents, record.AcceptedTerms, record.Currency, record.AcceptedAmericanOdds, record.PlacedAt,
		record.Result, record.Approved, sourceChecksum)
	if err != nil {
		return err
	}
	var storedBatch, storedUser, storedTerms, storedCurrency, storedResult, storedChecksum string
	var storedStake int64
	var storedOdds int
	var storedAt time.Time
	var storedApproved bool
	err = r.tx.QueryRow(ctx, `
		SELECT migration_batch_id::text, user_id::text, stake_cents, accepted_terms,
		       currency::text, accepted_american_odds, placed_at, result, approved, source_checksum
		FROM legacy_book_wagers WHERE source_wager_id = $1`, record.SourceWagerID).
		Scan(&storedBatch, &storedUser, &storedStake, &storedTerms, &storedCurrency, &storedOdds, &storedAt, &storedResult, &storedApproved, &storedChecksum)
	if err != nil {
		return err
	}
	if storedBatch != batchID || storedUser != userID || storedStake != record.StakeCents || storedTerms != record.AcceptedTerms || strings.TrimSpace(storedCurrency) != record.Currency || storedOdds != record.AcceptedAmericanOdds || !storedAt.Equal(record.PlacedAt) || storedResult != record.Result || storedApproved != record.Approved || storedChecksum != sourceChecksum {
		return fmt.Errorf("legacy wager %d does not match existing immutable history", record.SourceWagerID)
	}
	return nil
}

func (r postgresRepository) CompletePromotion(ctx context.Context, batchID string, input CompletionInput) error {
	var storedChecksum, storedTarget string
	err := r.tx.QueryRow(ctx, `
		INSERT INTO legacy_import_records
		(migration_batch_id, source_table, source_primary_key, target_table, target_id,
		 source_checksum, import_state, imported_at)
		VALUES ($1::uuid, 'Promotion', 'complete', 'migration_batches', $1::uuid, $2, 'imported', now())
		ON CONFLICT (migration_batch_id, source_table, source_primary_key) DO UPDATE
		SET source_checksum = legacy_import_records.source_checksum
		RETURNING source_checksum, target_id::text`, batchID, input.SourceChecksum).
		Scan(&storedChecksum, &storedTarget)
	if err != nil {
		return err
	}
	if storedChecksum != input.SourceChecksum || storedTarget != batchID {
		return errors.New("existing promotion completion record does not match reconciliation report")
	}

	var batchState string
	if err := r.tx.QueryRow(ctx, `SELECT state FROM migration_batches WHERE id = $1::uuid FOR UPDATE`, batchID).Scan(&batchState); err != nil {
		return err
	}
	if batchState != "validated" && batchState != "promoted" {
		return fmt.Errorf("migration batch state %q cannot be promoted", batchState)
	}
	alreadyPromoted := batchState == "promoted"
	if !alreadyPromoted {
		afterData, err := json.Marshal(map[string]any{
			"users": input.Users, "transactions": input.Transactions, "wagers": input.Wagers,
			"closing_cash_total_cents":    input.ClosingCashTotalCents,
			"transaction_net_total_cents": input.TransactionNetTotalCents,
			"difference_total_cents":      input.DifferenceTotalCents,
			"currency":                    SourceCurrency, "source_checksum": input.SourceChecksum,
		})
		if err != nil {
			return err
		}
		if _, err := r.tx.Exec(ctx, `
			INSERT INTO audit_entries
			(actor_user_id, action, target_type, target_id, reason, after_data)
			VALUES ($1::uuid, 'legacy_book.promoted', 'migration_batches', $2::uuid,
			        'Approved legacy Cabot Book reconciliation and opening balances', $3::jsonb)`,
			input.ActorUserID, batchID, string(afterData)); err != nil {
			return err
		}
	}
	_, err = r.tx.Exec(ctx, `
		UPDATE migration_batches
		SET state = 'promoted', completed_at = coalesce(completed_at, now())
		WHERE id = $1::uuid AND state IN ('validated', 'promoted')`, batchID)
	return err
}

func (r postgresRepository) verifyOpeningBalance(ctx context.Context, batchID string, input OpeningBalanceInput, userAccountID, equityAccountID string) error {
	var count int
	var userAmount, equityAmount int64
	var transactionID, transactionType, sourceType, actorUserID, reason string
	var expectedPostingCount int
	err := r.tx.QueryRow(ctx, `
		SELECT t.id::text, t.transaction_type, t.source_type, coalesce(t.actor_user_id::text, ''),
		       coalesce(t.reason, ''), t.expected_posting_count, count(*),
		       coalesce(sum(p.amount_cents) FILTER (WHERE p.account_id = $3::uuid), 0),
		       coalesce(sum(p.amount_cents) FILTER (WHERE p.account_id = $4::uuid), 0)
		FROM ledger_transactions t JOIN ledger_postings p ON p.transaction_id = t.id
		WHERE t.currency = $1 AND t.idempotency_key = $2
		  AND p.account_id IN ($3::uuid, $4::uuid)
		GROUP BY t.id`, input.Currency, input.IdempotencyKey, userAccountID, equityAccountID).
		Scan(&transactionID, &transactionType, &sourceType, &actorUserID, &reason,
			&expectedPostingCount, &count, &userAmount, &equityAmount)
	if err != nil {
		return err
	}
	if transactionType != "migration_adjustment" || sourceType != SourceSystem || actorUserID != input.ActorUserID ||
		reason != input.Reason || expectedPostingCount != 2 || count != 2 ||
		userAmount != input.AmountCents || equityAmount != -input.AmountCents {
		return errors.New("existing opening balance does not match requested balanced postings")
	}
	expectedChecksum, err := checksum(input)
	if err != nil {
		return err
	}
	var storedChecksum, targetTable, targetID string
	err = r.tx.QueryRow(ctx, `
		SELECT source_checksum, coalesce(target_table, ''), coalesce(target_id::text, '')
		FROM legacy_import_records
		WHERE migration_batch_id = $1::uuid AND source_table = 'UserBalances' AND source_primary_key = $2`,
		batchID,
		fmt.Sprintf("%d:%s", input.LegacyUserID, input.AccountType)).Scan(&storedChecksum, &targetTable, &targetID)
	if err != nil {
		return err
	}
	if storedChecksum != expectedChecksum || targetTable != "ledger_transactions" || targetID != transactionID {
		return errors.New("existing opening balance import record does not match requested migration")
	}
	return nil
}

func checksum(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
