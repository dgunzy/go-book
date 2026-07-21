package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dgunzy/go-book/internal/authweb"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/bettingweb"
	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/competitionweb"
	"github.com/dgunzy/go-book/internal/config"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/dgunzy/go-book/internal/identity"
	"github.com/dgunzy/go-book/internal/identitypg"
	"github.com/dgunzy/go-book/internal/membersweb"
	"github.com/dgunzy/go-book/internal/migration/publicseed"
	"github.com/dgunzy/go-book/internal/migration/schema"
	"github.com/dgunzy/go-book/internal/oidcclient"
	"github.com/dgunzy/go-book/internal/platform/httpmiddleware"
	"github.com/dgunzy/go-book/internal/platform/httpserver"
	"github.com/dgunzy/go-book/internal/platform/postgresdb"
	"github.com/dgunzy/go-book/internal/privatepg"
	"github.com/dgunzy/go-book/internal/privateweb"
	publicweb "github.com/dgunzy/go-book/internal/web"
	"github.com/dgunzy/go-book/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	case len(arguments) == 1 && arguments[0] == "legacy-book-report":
		return runLegacyBook(ctx, logger, lookup, false, os.Stdout)
	case len(arguments) == 1 && arguments[0] == "legacy-book-promote":
		return runLegacyBook(ctx, logger, lookup, true, os.Stdout)
	case len(arguments) == 1 && arguments[0] == "bootstrap-owner":
		return runBootstrapOwner(ctx, logger, lookup, os.Stdout)
	case len(arguments) == 1 && arguments[0] == "bootstrap-role":
		return runBootstrapRole(ctx, logger, lookup, os.Stdout)
	case len(arguments) == 1 && arguments[0] == "mock-seed":
		return runMockSeed(ctx, logger, lookup, os.Stdout)
	default:
		return fmt.Errorf("usage: cabot-cup [migrate|legacy-book-report|legacy-book-promote|bootstrap-owner|bootstrap-role|mock-seed]")
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
	applicationHandler := http.NewServeMux()
	var readiness func(context.Context) error
	var dispatcher *events.Dispatcher
	if applicationConfig.PrivateAppEnabled {
		pool, err := postgresdb.Open(ctx, applicationConfig.DatabaseURL)
		if err != nil {
			return fmt.Errorf("open private application database: %w", err)
		}
		defer pool.Close()
		readiness = databaseReadiness(pool)

		provider, err := oidcclient.New(ctx, oidcclient.Config{
			IssuerURL: applicationConfig.OIDCIssuerURL, ClientID: applicationConfig.OIDCClientID,
			ClientSecret: applicationConfig.OIDCClientSecret, RedirectURL: applicationConfig.OIDCRedirectURL,
		})
		if err != nil {
			return fmt.Errorf("initialize OIDC provider: %w", err)
		}
		sessions, err := identity.NewService(identitypg.Store{Pool: pool}, applicationConfig.SessionTTL)
		if err != nil {
			return fmt.Errorf("initialize identity sessions: %w", err)
		}
		attempts, err := authweb.NewPostgresAttemptStore(pool)
		if err != nil {
			return fmt.Errorf("initialize OIDC login attempts: %w", err)
		}
		authHandler, err := authweb.New(authweb.Config{
			Deployed: secureDeployment, LoginAttemptTTL: applicationConfig.LoginAttemptTTL,
		}, authweb.Dependencies{
			Attempts: attempts, OIDC: provider, Sessions: sessions,
		})
		if err != nil {
			return fmt.Errorf("build authentication handler: %w", err)
		}
		readers, err := privatepg.New(pool)
		if err != nil {
			return fmt.Errorf("build private read models: %w", err)
		}
		privateHandler, err := privateweb.New(privateweb.Dependencies{
			Sessions: authHandler.SessionReader(), Dashboard: readers, Ledger: readers,
			Wagers: readers, Reconciliation: readers,
		})
		if err != nil {
			return fmt.Errorf("build private web handler: %w", err)
		}
		bettingStore := bettingpg.Store{DB: pool}
		bettingHandler, err := bettingweb.New(bettingweb.Dependencies{
			Sessions: authHandler.SessionReader(), Markets: bettingStore, Wagers: bettingStore,
			AutoApproveMaxCents: applicationConfig.WagerAutoApproveMaxCents,
		})
		if err != nil {
			return fmt.Errorf("build betting web handler: %w", err)
		}
		membersHandler, err := membersweb.New(membersweb.Dependencies{
			Sessions: authHandler.SessionReader(), Members: identitypg.Store{Pool: pool},
			Players: competitionpg.Store{Pool: pool}, PublicBaseURL: applicationConfig.PublicBaseURL.String(),
		})
		if err != nil {
			return fmt.Errorf("build members web handler: %w", err)
		}
		competitionHandler, err := competitionweb.New(competitionweb.Dependencies{
			Sessions: authHandler.SessionReader(), Competition: competitionpg.Store{Pool: pool},
		})
		if err != nil {
			return fmt.Errorf("build competition web handler: %w", err)
		}
		// /dev/login is served only by binaries built with the `dev` build
		// tag; the production image returns 404 for it because authweb does
		// not register the route there.
		for _, path := range []string{"/login", "/auth/google", "/auth/callback", "/logout", "/dev/login"} {
			applicationHandler.Handle(path, authHandler)
		}
		applicationHandler.Handle("/book", privateHandler)
		applicationHandler.Handle("/book/", privateHandler)
		applicationHandler.Handle("/admin", privateHandler)
		// The betting UI owns its specific routes; these more-specific
		// patterns take precedence over privateweb's /book/ subtree.
		for _, path := range []string{
			"/book/markets", "/book/wagers",
			"/admin/markets", "/admin/markets/",
			"/admin/wagers", "/admin/wagers/",
			"/admin/help",
		} {
			applicationHandler.Handle(path, bettingHandler)
		}
		applicationHandler.Handle("/admin/members", membersHandler)
		applicationHandler.Handle("/admin/members/", membersHandler)
		applicationHandler.Handle("/admin/matches", competitionHandler)
		applicationHandler.Handle("/admin/matches/", competitionHandler)
		applicationHandler.Handle("/admin/events", competitionHandler)
		applicationHandler.Handle("/admin/events/", competitionHandler)
		applicationHandler.Handle("/invite/", authHandler)

		dispatcher, err = newOutboxDispatcher(pool, logger)
		if err != nil {
			return fmt.Errorf("build outbox dispatcher: %w", err)
		}
	}
	applicationHandler.Handle("/", publicHandler)

	handler := buildHandler(applicationHandler, logger, secureDeployment, readiness)
	server := httpserver.New(httpserver.Config{
		Address:         applicationConfig.Address,
		ShutdownTimeout: applicationConfig.ShutdownTimeout,
	}, handler, logger)

	if applicationConfig.PrivateAppEnabled && applicationConfig.DatabaseMode == config.DatabaseModeTest {
		logger.Warn("DATABASE_MODE is test: all reads and writes target TEST_DATABASE_URL, not the real ledger")
	}
	logger.Info("starting Cabot Cup",
		"environment", applicationConfig.Environment,
		"public_base_url", applicationConfig.PublicBaseURL.String(),
		"private_app_enabled", applicationConfig.PrivateAppEnabled,
		"database_mode", applicationConfig.DatabaseMode,
	)

	// The outbox dispatcher runs beside the HTTP server and is stopped only
	// after the server has drained, so events published by in-flight requests
	// still get dispatched. Derived from Background deliberately: the signal
	// context cancelling must drain the server first, then stop the worker.
	dispatcherCtx, stopDispatcher := context.WithCancel(context.Background())
	defer stopDispatcher()
	var dispatcherDone sync.WaitGroup
	if dispatcher != nil {
		dispatcherDone.Go(func() {
			if err := dispatcher.Run(dispatcherCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("outbox dispatcher stopped", "error", err)
			}
		})
		logger.Info("outbox dispatcher started", "poll_interval", outboxPollInterval.String())
	}

	serverErr := server.Run(ctx)
	stopDispatcher()
	dispatcherDone.Wait()
	return serverErr
}

// Outbox dispatcher tuning. Polling is the correctness mechanism (no
// LISTEN/NOTIFY dependency); these values trade settlement latency against
// idle database load for a single-instance deployment.
const (
	outboxPollInterval = 2 * time.Second
	outboxBatchSize    = 25
	outboxLockLease    = 2 * time.Minute
	outboxBackoffBase  = 5 * time.Second
	outboxBackoffCap   = 5 * time.Minute
)

func newOutboxDispatcher(pool *pgxpool.Pool, logger *slog.Logger) (*events.Dispatcher, error) {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "cabot"
	}
	return events.NewDispatcher(events.DispatcherConfig{
		Store: eventspg.Store{Pool: pool},
		Consumers: []events.Consumer{
			&bettingpg.MatchSettlementConsumer{Store: &bettingpg.Store{DB: pool}, Logger: logger},
			&bettingpg.PricingConsumer{Store: &bettingpg.Store{DB: pool}, Logger: logger},
			&competitionpg.StatsProjectionConsumer{Pool: pool, Logger: logger},
		},
		PollInterval: outboxPollInterval,
		BatchSize:    outboxBatchSize,
		LockLease:    outboxLockLease,
		WorkerID:     fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		BackoffBase:  outboxBackoffBase,
		BackoffCap:   outboxBackoffCap,
		Logger:       logger,
	})
}

func runMigrations(ctx context.Context, logger *slog.Logger, lookup lookupFunc) error {
	databaseMode, databaseURL, err := config.DatabaseSelection(lookup)
	if err != nil {
		return err
	}
	if strings.TrimSpace(databaseURL) == "" {
		return fmt.Errorf("DATABASE_URL is required for migrations")
	}
	logger.Info("running migrations", "database_mode", databaseMode)

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

func buildHandler(applicationHandler http.Handler, logger *slog.Logger, production bool, readiness ...func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", health)
	mux.HandleFunc("GET /readyz", readinessHandler(firstReadiness(readiness)))
	mux.Handle("/", applicationHandler)

	return httpmiddleware.Chain(
		mux,
		httpmiddleware.RequestID,
		httpmiddleware.AccessLog(logger),
		httpmiddleware.Recover(logger),
		httpmiddleware.SecurityHeaders(production),
	)
}

func firstReadiness(checks []func(context.Context) error) func(context.Context) error {
	for _, check := range checks {
		if check != nil {
			return check
		}
	}
	return nil
}

func readinessHandler(check func(context.Context) error) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if check == nil {
			health(response, request)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 1500*time.Millisecond)
		defer cancel()
		if err := check(ctx); err != nil {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		health(response, request)
	}
}

func databaseReadiness(pool *pgxpool.Pool) func(context.Context) error {
	definitions := migrations.All()
	expectedVersion := definitions[len(definitions)-1].Version
	return func(ctx context.Context) error {
		var version int64
		var identityReady, legacyReady bool
		err := pool.QueryRow(ctx, `
			SELECT coalesce(max(version), 0),
			       to_regclass('public.oidc_login_attempts') IS NOT NULL,
			       to_regclass('public.legacy_book_user_mappings') IS NOT NULL
			FROM schema_migrations`).Scan(&version, &identityReady, &legacyReady)
		if err != nil {
			return err
		}
		if version != expectedVersion || !identityReady || !legacyReady {
			return fmt.Errorf("database schema is not ready")
		}
		return nil
	}
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
