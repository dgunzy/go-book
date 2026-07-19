// Package bettingpg implements PostgreSQL persistence and event-consumer
// wiring for internal/betting. It mirrors the internal/migration/legacybook
// repository style: every command opens one pgx.Tx, performs an
// insert-with-ON-CONFLICT-DO-NOTHING followed by a verify query for
// idempotency, and publishes its outbox event on the same transaction via
// internal/eventspg so the domain change and the event commit atomically.
//
// internal/betting itself performs no I/O; this package is where market,
// selection, wager, and ledger rows are read, locked, and written, and where
// the pure domain commands in internal/betting are invoked with that data.
package bettingpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
)

// maxOutboxAttempts bounds retries for every event this package publishes.
const maxOutboxAttempts = 20

// PostgresDB is the minimal PostgreSQL surface Store needs, mirroring
// legacybook.PostgresDB.
type PostgresDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Store implements PostgreSQL persistence for the betting domain.
type Store struct{ DB PostgresDB }

func (s Store) begin(ctx context.Context) (pgx.Tx, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	return s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(value string) bool { return uuidPattern.MatchString(value) }

// loadMarket reads a market row. It does not lock; callers that need to
// serialize concurrent settlement or closure use loadMarketForUpdate.
func loadMarket(ctx context.Context, tx pgx.Tx, marketID string) (betting.Market, error) {
	return scanMarket(tx.QueryRow(ctx, `
		SELECT market_type, coalesce(match_id::text, ''), title, state, currency, opens_at, closes_at
		FROM markets WHERE id = $1::uuid`, marketID), marketID)
}

func loadMarketForUpdate(ctx context.Context, tx pgx.Tx, marketID string) (betting.Market, error) {
	return scanMarket(tx.QueryRow(ctx, `
		SELECT market_type, coalesce(match_id::text, ''), title, state, currency, opens_at, closes_at
		FROM markets WHERE id = $1::uuid FOR UPDATE`, marketID), marketID)
}

func scanMarket(row pgx.Row, marketID string) (betting.Market, error) {
	var marketType, matchID, title, state, currency string
	var opensAt sql.NullTime
	var closesAt time.Time
	if err := row.Scan(&marketType, &matchID, &title, &state, &currency, &opensAt, &closesAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return betting.Market{}, fmt.Errorf("%w: market %s", betting.ErrNotFound, marketID)
		}
		return betting.Market{}, fmt.Errorf("load market %s: %w", marketID, err)
	}
	market := betting.Market{
		ID:       betting.ID(marketID),
		Type:     betting.MarketType(marketType),
		MatchID:  betting.ID(matchID),
		Title:    title,
		State:    betting.MarketState(state),
		Currency: ledger.Currency(currency),
		ClosesAt: closesAt.UTC(),
	}
	if opensAt.Valid {
		market.OpensAt = opensAt.Time.UTC()
	}
	return market, nil
}

func loadSelection(ctx context.Context, tx pgx.Tx, marketID, selectionID string) (betting.Selection, error) {
	var key, terms string
	var odds int32
	var semantic sql.NullString
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT selection_key, display_terms, offered_american_odds, semantic_result_key, active
		FROM selections WHERE market_id = $1::uuid AND id = $2::uuid`, marketID, selectionID).
		Scan(&key, &terms, &odds, &semantic, &active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return betting.Selection{}, fmt.Errorf("%w: selection %s", betting.ErrNotFound, selectionID)
		}
		return betting.Selection{}, fmt.Errorf("load selection %s: %w", selectionID, err)
	}
	return betting.Selection{
		ID:                  betting.ID(selectionID),
		MarketID:            betting.ID(marketID),
		Key:                 key,
		DisplayTerms:        terms,
		OfferedAmericanOdds: ledger.AmericanOdds(odds),
		SemanticResultKey:   semantic.String,
		Active:              active,
	}, nil
}

// loadSelections returns every selection on a market along with its raw
// semantic_result_key (used by the match-settlement mapping, which must
// treat NULL/empty distinctly from a recognized key).
func loadSelections(ctx context.Context, tx pgx.Tx, marketID string) ([]betting.Selection, map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, selection_key, display_terms, offered_american_odds, semantic_result_key, active
		FROM selections WHERE market_id = $1::uuid ORDER BY id`, marketID)
	if err != nil {
		return nil, nil, fmt.Errorf("load selections for market %s: %w", marketID, err)
	}
	defer rows.Close()

	var selections []betting.Selection
	semanticKeys := make(map[string]string)
	for rows.Next() {
		var id, key, terms string
		var odds int32
		var semantic sql.NullString
		var active bool
		if err := rows.Scan(&id, &key, &terms, &odds, &semantic, &active); err != nil {
			return nil, nil, fmt.Errorf("scan selection: %w", err)
		}
		selections = append(selections, betting.Selection{
			ID:                  betting.ID(id),
			MarketID:            betting.ID(marketID),
			Key:                 key,
			DisplayTerms:        terms,
			OfferedAmericanOdds: ledger.AmericanOdds(odds),
			SemanticResultKey:   semantic.String,
			Active:              active,
		})
		semanticKeys[id] = semantic.String
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("load selections for market %s: %w", marketID, err)
	}
	return selections, semanticKeys, nil
}

func loadRestrictedUsers(ctx context.Context, tx pgx.Tx, marketID string) ([]betting.ID, error) {
	rows, err := tx.Query(ctx, `SELECT user_id::text FROM market_restrictions WHERE market_id = $1::uuid`, marketID)
	if err != nil {
		return nil, fmt.Errorf("load market restrictions for %s: %w", marketID, err)
	}
	defer rows.Close()
	var restricted []betting.ID
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan market restriction: %w", err)
		}
		restricted = append(restricted, betting.ID(userID))
	}
	return restricted, rows.Err()
}

// wagerRow scans one wagers table row into a betting.Wager.
func wagerRow(row pgx.Row) (betting.Wager, error) {
	var id, userID, marketID, selectionID, fundingType, currency, terms, state, idempotencyKey string
	var stakeCents, profitCents int64
	var odds int32
	var placedAt time.Time
	if err := row.Scan(&id, &userID, &marketID, &selectionID, &fundingType, &stakeCents, &currency,
		&odds, &terms, &profitCents, &state, &idempotencyKey, &placedAt); err != nil {
		return betting.Wager{}, err
	}
	return betting.Wager{
		ID:                 betting.ID(id),
		UserID:             betting.ID(userID),
		MarketID:           betting.ID(marketID),
		SelectionID:        betting.ID(selectionID),
		FundingAccountType: betting.FundingAccountType(fundingType),
		Stake:              ledger.Money{Cents: stakeCents, Currency: ledger.Currency(currency)},
		AcceptedOdds:       ledger.AmericanOdds(odds),
		AcceptedTerms:      terms,
		PotentialProfit:    ledger.Money{Cents: profitCents, Currency: ledger.Currency(currency)},
		State:              betting.WagerState(state),
		IdempotencyKey:     idempotencyKey,
		PlacedAt:           placedAt.UTC(),
	}, nil
}

const wagerColumns = `id::text, user_id::text, market_id::text, selection_id::text, funding_account_type,
	stake_cents, currency, accepted_american_odds, accepted_terms, potential_profit_cents, state,
	idempotency_key, placed_at`

func loadWagerForUpdate(ctx context.Context, tx pgx.Tx, wagerID string) (betting.Wager, error) {
	wager, err := wagerRow(tx.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wagers WHERE id = $1::uuid FOR UPDATE`, wagerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return betting.Wager{}, fmt.Errorf("%w: wager %s", betting.ErrNotFound, wagerID)
	}
	if err != nil {
		return betting.Wager{}, fmt.Errorf("load wager %s: %w", wagerID, err)
	}
	return wager, nil
}

func loadWagersForMarketForUpdate(ctx context.Context, tx pgx.Tx, marketID string) ([]betting.Wager, error) {
	rows, err := tx.Query(ctx, `SELECT `+wagerColumns+` FROM wagers WHERE market_id = $1::uuid ORDER BY id FOR UPDATE`, marketID)
	if err != nil {
		return nil, fmt.Errorf("load wagers for market %s: %w", marketID, err)
	}
	defer rows.Close()
	var wagers []betting.Wager
	for rows.Next() {
		wager, err := wagerRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan wager: %w", err)
		}
		wagers = append(wagers, wager)
	}
	return wagers, rows.Err()
}

// ensureUserAccount upserts and returns the ledger account for a user's cash
// or free-play funding balance. The upsert shape mirrors
// legacybook.EnsureOpeningBalance.
func ensureUserAccount(ctx context.Context, tx pgx.Tx, userID string, accountType betting.FundingAccountType, currency ledger.Currency) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (owner_user_id, account_type, currency, name)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (owner_user_id, account_type, currency) WHERE owner_user_id IS NOT NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, userID, string(accountType), string(currency),
		fmt.Sprintf("User %s %s", userID, accountType)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ensure user ledger account: %w", err)
	}
	return id, nil
}

// ensureSystemAccount upserts and returns a shared system ledger account
// (wager_escrow or house_clearing) for a currency.
func ensureSystemAccount(ctx context.Context, tx pgx.Tx, accountType string, currency ledger.Currency) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, name)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_type, currency) WHERE owner_user_id IS NULL
		DO UPDATE SET name = ledger_accounts.name
		RETURNING id::text`, accountType, string(currency),
		fmt.Sprintf("System %s %s", accountType, currency)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ensure system ledger account: %w", err)
	}
	return id, nil
}

// insertLedgerTransaction writes an already-validated, balanced
// ledger.Transaction and its postings. Callers are responsible for the
// idempotency guarantee (a row lock or an outer ON CONFLICT check) that
// makes sure this is only ever called once per real-world transaction.
func insertLedgerTransaction(ctx context.Context, tx pgx.Tx, txn ledger.Transaction) (string, error) {
	actorUUID := ""
	if isUUID(txn.Actor) {
		actorUUID = txn.Actor
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
		(transaction_type, currency, idempotency_key, source_type, source_id, actor_user_id, reason,
		 expected_posting_count, occurred_at)
		VALUES ($1, $2, $3, $4, nullif($5, '')::uuid, nullif($6, '')::uuid, nullif($7, ''), $8, now())
		RETURNING id::text`,
		string(txn.Type), string(txn.Currency), txn.IdempotencyKey, txn.SourceType, txn.SourceID,
		actorUUID, txn.Reason, txn.ExpectedPostingCount()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert ledger transaction %s: %w", txn.IdempotencyKey, err)
	}
	for _, posting := range txn.Postings {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger_postings (transaction_id, account_id, amount_cents)
			VALUES ($1::uuid, $2::uuid, $3)`, id, posting.AccountID, posting.Amount.Cents); err != nil {
			return "", fmt.Errorf("insert ledger posting for transaction %s: %w", id, err)
		}
	}
	return id, nil
}

// accountBalance reads the current balance of a ledger account from
// ledger_postings directly (not the ledger_account_balances view, which
// callers use for read-only reporting outside a transaction).
func accountBalance(ctx context.Context, tx pgx.Tx, accountID string) (int64, error) {
	var balance int64
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount_cents), 0) FROM ledger_postings WHERE account_id = $1::uuid`, accountID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("read account %s balance: %w", accountID, err)
	}
	return balance, nil
}

// lockAccount takes a row lock on a ledger account so a concurrent
// transaction attempting to spend the same account's balance must wait for
// this transaction to commit or roll back before it can compute its own
// balance. This is what makes concurrent wager acceptance unable to
// overspend a shared funding account.
func lockAccount(ctx context.Context, tx pgx.Tx, accountID string) error {
	var discard string
	err := tx.QueryRow(ctx, `SELECT id::text FROM ledger_accounts WHERE id = $1::uuid FOR UPDATE`, accountID).Scan(&discard)
	if err != nil {
		return fmt.Errorf("lock account %s: %w", accountID, err)
	}
	return nil
}
