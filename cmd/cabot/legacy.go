package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/dgunzy/go-book/internal/config"
	"github.com/dgunzy/go-book/internal/migration/legacybook"
	"github.com/jackc/pgx/v5"
)

type legacyPromotionOutput struct {
	BatchID                  string                     `json:"batch_id"`
	SourceVersion            string                     `json:"source_version"`
	ClosingCashTotalCents    int64                      `json:"closing_cash_total_cents"`
	TransactionNetTotalCents int64                      `json:"transaction_net_total_cents"`
	DifferenceTotalCents     int64                      `json:"difference_total_cents"`
	Promotion                legacybook.PromotionResult `json:"promotion"`
}

type legacyReportOutput struct {
	SourceVersion            string             `json:"source_version"`
	SourceSystem             string             `json:"source_system"`
	UserCount                int                `json:"user_count"`
	TransactionCount         int                `json:"transaction_count"`
	WagerCount               int                `json:"wager_count"`
	ClosingCashTotalCents    int64              `json:"closing_cash_total_cents"`
	TransactionNetTotalCents int64              `json:"transaction_net_total_cents"`
	DifferenceTotalCents     int64              `json:"difference_total_cents"`
	Issues                   []legacybook.Issue `json:"issues"`
	Promotable               bool               `json:"promotable"`
}

const legacyMigrationActorEmail = "legacy-public-import@cabotcup.invalid"

func runLegacyBook(ctx context.Context, logger *slog.Logger, lookup lookupFunc, promote bool, output io.Writer) error {
	databaseMode, databaseURL, err := config.DatabaseSelection(lookup)
	if err != nil {
		return err
	}
	if strings.TrimSpace(databaseURL) == "" {
		return errors.New("DATABASE_URL is required for legacy book reconciliation")
	}
	logger.Info("legacy book command starting", "database_mode", databaseMode, "promote", promote)
	connection, err := pgx.Connect(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return fmt.Errorf("connect for legacy book reconciliation: %w", err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	sourceTx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin legacy book source snapshot: %w", err)
	}
	defer func() { _ = sourceTx.Rollback(context.Background()) }()
	report, err := legacybook.Reconcile(ctx, legacybook.PostgresSource{DB: sourceTx})
	if err != nil {
		return fmt.Errorf("reconcile legacy book: %w", err)
	}
	if err := sourceTx.Commit(ctx); err != nil {
		return fmt.Errorf("commit legacy book source snapshot: %w", err)
	}
	encodedReport, err := report.JSON()
	if err != nil {
		return fmt.Errorf("encode legacy book report: %w", err)
	}
	sum := sha256.Sum256(encodedReport)
	sourceVersion := hex.EncodeToString(sum[:])
	if !promote {
		return json.NewEncoder(output).Encode(legacyReportOutput{
			SourceVersion: sourceVersion, SourceSystem: report.SourceSystem,
			UserCount: report.UserCount, TransactionCount: report.TransactionCount, WagerCount: report.WagerCount,
			ClosingCashTotalCents:    report.ClosingCashTotalCents,
			TransactionNetTotalCents: report.TransactionNetTotalCents,
			DifferenceTotalCents:     report.DifferenceTotalCents,
			Issues:                   report.Issues, Promotable: report.Promotable(),
		})
	}
	if !report.Promotable() {
		return errors.New("legacy book report contains blocking issues; run legacy-book-report for details")
	}
	expectedVersion, ok := lookup("LEGACY_BOOK_EXPECTED_SOURCE_VERSION")
	if !ok || strings.TrimSpace(expectedVersion) == "" {
		return errors.New("LEGACY_BOOK_EXPECTED_SOURCE_VERSION is required to approve the reconciled source and warnings")
	}
	if strings.TrimSpace(expectedVersion) != sourceVersion {
		return fmt.Errorf("legacy book source version does not match explicit approval: got %s", sourceVersion)
	}
	var actorUserID string
	if err := connection.QueryRow(ctx, `
		SELECT id::text FROM users WHERE email = $1 AND status = 'disabled'`, legacyMigrationActorEmail).Scan(&actorUserID); err != nil {
		return fmt.Errorf("resolve legacy migration actor: %w", err)
	}
	batchID, err := ensureLegacyMigrationBatch(ctx, connection, sourceVersion, report)
	if err != nil {
		return fmt.Errorf("prepare legacy migration batch: %w", err)
	}
	result, err := legacybook.Promote(ctx, legacybook.PostgresStore{DB: connection}, report, legacybook.PromoteOptions{
		BatchID: batchID, Currency: legacybook.SourceCurrency, ActorUserID: actorUserID, SourceVersion: sourceVersion,
	})
	if err != nil {
		return fmt.Errorf("promote legacy book: %w", err)
	}
	logger.Info("legacy book promotion complete",
		"batch_id", batchID,
		"users", result.Users,
		"settled_balances", result.SettledBalances,
		"transactions", result.Transactions,
		"wagers", result.Wagers,
	)
	return json.NewEncoder(output).Encode(legacyPromotionOutput{
		BatchID:                  batchID,
		SourceVersion:            sourceVersion,
		ClosingCashTotalCents:    report.ClosingCashTotalCents,
		TransactionNetTotalCents: report.TransactionNetTotalCents,
		DifferenceTotalCents:     report.DifferenceTotalCents,
		Promotion:                result,
	})
}

func ensureLegacyMigrationBatch(ctx context.Context, connection *pgx.Conn, sourceVersion string, report legacybook.Report) (string, error) {
	sourceCounts, err := json.Marshal(map[string]int{
		"users": report.UserCount, "transactions": report.TransactionCount, "wagers": report.WagerCount,
	})
	if err != nil {
		return "", err
	}
	reconciliation, err := json.Marshal(map[string]any{
		"closing_cash_total_cents":      report.ClosingCashTotalCents,
		"closing_free_play_total_cents": report.ClosingFreePlayTotalCents,
		"transaction_net_total_cents":   report.TransactionNetTotalCents,
		"difference_total_cents":        report.DifferenceTotalCents,
		"issues":                        report.Issues,
	})
	if err != nil {
		return "", err
	}
	var batchID string
	err = connection.QueryRow(ctx, `
		INSERT INTO migration_batches
		(source_system, source_version, state, source_counts, reconciliation)
		VALUES ($1, $2, 'validated', $3::jsonb, $4::jsonb)
		ON CONFLICT (source_system, source_version) DO UPDATE
		SET source_counts = EXCLUDED.source_counts,
		    reconciliation = EXCLUDED.reconciliation,
		    state = CASE WHEN migration_batches.state = 'promoted' THEN 'promoted' ELSE 'validated' END
		WHERE migration_batches.state <> 'rolled_back'
		RETURNING id::text`, legacybook.SourceSystem, sourceVersion, string(sourceCounts), string(reconciliation)).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("matching migration batch was rolled back")
	}
	return batchID, err
}
