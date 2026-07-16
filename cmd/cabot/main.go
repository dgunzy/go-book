package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dgunzy/go-book/internal/config"
	"github.com/dgunzy/go-book/internal/platform/httpmiddleware"
	"github.com/dgunzy/go-book/internal/platform/httpserver"
	publicweb "github.com/dgunzy/go-book/internal/web"
)

func main() {
	logger := newLogger(os.Getenv("APP_ENV"))
	if err := run(logger); err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	applicationConfig, err := config.Load(os.LookupEnv)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting Cabot Cup",
		"environment", applicationConfig.Environment,
		"public_base_url", applicationConfig.PublicBaseURL.String(),
	)
	return server.Run(ctx)
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
