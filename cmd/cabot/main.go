package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dgunzy/go-book/internal/config"
	"github.com/dgunzy/go-book/internal/migration/publicseed"
	"github.com/dgunzy/go-book/internal/migration/schema"
	"github.com/dgunzy/go-book/internal/platform/httpmiddleware"
	"github.com/dgunzy/go-book/internal/platform/httpserver"
	publicweb "github.com/dgunzy/go-book/internal/web"
	"github.com/jackc/pgx/v5"
)

func main() {
	logger := newLogger(os.Getenv("APP_ENV"))
	if err := run(logger); err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCommand(ctx, logger, os.Args[1:], os.LookupEnv)
}

type lookupFunc func(string) (string, bool)

func runCommand(ctx context.Context, logger *slog.Logger, arguments []string, lookup lookupFunc) error {
	switch {
	case len(arguments) == 0:
		return runServer(ctx, logger, lookup)
	case len(arguments) == 1 && arguments[0] == "migrate":
		return runMigrations(ctx, logger, lookup)
	default:
		return fmt.Errorf("usage: cabot-cup [migrate]")
	}
}

func runServer(ctx context.Context, logger *slog.Logger, lookup lookupFunc) error {
	applicationConfig, err := config.Load(lookup)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	publicHandler, err := publicweb.New()
	if err != nil {
		return fmt.Errorf("build public web handler: %w", err)
	}

	secureDeployment := applicationConfig.Environment == "staging" || applicationConfig.Environment == "production"
	handler := buildHandler(publicHandler, logger, secureDeployment)
	server := httpserver.New(httpserver.Config{
		Address:         applicationConfig.Address,
		ShutdownTimeout: applicationConfig.ShutdownTimeout,
	}, handler, logger)

	logger.Info("starting Cabot Cup",
		"environment", applicationConfig.Environment,
		"public_base_url", applicationConfig.PublicBaseURL.String(),
	)
	return server.Run(ctx)
}

func runMigrations(ctx context.Context, logger *slog.Logger, lookup lookupFunc) error {
	databaseURL, ok := lookup("DATABASE_URL")
	if !ok || strings.TrimSpace(databaseURL) == "" {
		return fmt.Errorf("DATABASE_URL is required for migrations")
	}

	connection, err := pgx.Connect(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return fmt.Errorf("connect for migrations: %w", err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	schemaReport, err := schema.Apply(ctx, connection)
	if err != nil {
		return fmt.Errorf("apply schema migrations: %w", err)
	}

	tx, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin public seed: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	seedReport, err := publicseed.Apply(ctx, tx)
	if err != nil {
		return fmt.Errorf("seed public snapshot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit public seed: %w", err)
	}

	logger.Info("database migration complete",
		"schema_applied", schemaReport.Applied,
		"schema_skipped", schemaReport.Skipped,
		"players", seedReport.Players,
		"events", seedReport.Events,
		"stat_snapshots", seedReport.StatSnapshots,
		"media_assets", seedReport.MediaAssets,
		"media_player_links", seedReport.MediaPlayerLinks,
		"remote_event_photos_skipped", seedReport.SkippedEventPhotos,
	)
	return nil
}

func buildHandler(publicHandler http.Handler, logger *slog.Logger, production bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", health)
	mux.HandleFunc("GET /readyz", health)
	mux.Handle("/", publicHandler)

	return httpmiddleware.Chain(
		mux,
		httpmiddleware.RequestID,
		httpmiddleware.AccessLog(logger),
		httpmiddleware.Recover(logger),
		httpmiddleware.SecurityHeaders(production),
	)
}

func health(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ok\n"))
}

func newLogger(environment string) *slog.Logger {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	if environment == "production" {
		return slog.New(slog.NewJSONHandler(os.Stdout, options))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, options))
}
