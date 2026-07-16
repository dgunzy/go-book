package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type Config struct {
	Address           string
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

func New(config Config, handler http.Handler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	httpServer := &http.Server{
		Addr:              config.Address,
		Handler:           handler,
		ReadHeaderTimeout: valueOrDefault(config.ReadHeaderTimeout, 5*time.Second),
		ReadTimeout:       valueOrDefault(config.ReadTimeout, 15*time.Second),
		WriteTimeout:      valueOrDefault(config.WriteTimeout, 30*time.Second),
		IdleTimeout:       valueOrDefault(config.IdleTimeout, 60*time.Second),
	}

	return &Server{
		server:          httpServer,
		shutdownTimeout: valueOrDefault(config.ShutdownTimeout, 10*time.Second),
		logger:          logger,
		serve:           httpServer.ListenAndServe,
		shutdown:        httpServer.Shutdown,
		close:           httpServer.Close,
	}
}

type Server struct {
	server          *http.Server
	shutdownTimeout time.Duration
	logger          *slog.Logger
	serve           func() error
	shutdown        func(context.Context) error
	close           func() error
}

// Run serves until the context is cancelled or the HTTP server fails. A cancelled
// context triggers a bounded graceful shutdown.
func (server *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		server.logger.Info("http server listening", "address", server.server.Addr)
		errCh <- server.serve()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), server.shutdownTimeout)
		defer cancel()

		if err := server.shutdown(shutdownCtx); err != nil {
			_ = server.close()
			return fmt.Errorf("shut down HTTP server: %w", err)
		}

		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", err)
		}
		return nil
	}
}

func valueOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
