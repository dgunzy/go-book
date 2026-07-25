// Package postgresdb configures the application's PostgreSQL connection pool.
package postgresdb

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxConnections = int32(8)
	defaultMinConnections = int32(1)
)

// Config builds a bounded pool configuration with defensive server-side timeouts.
func Config(databaseURL string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = defaultMaxConnections
	config.MinConns = defaultMinConnections
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	config.ConnConfig.RuntimeParams["application_name"] = "cabot-cup-web"
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
	config.ConnConfig.RuntimeParams["lock_timeout"] = "5s"
	config.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15s"
	return config, nil
}

// Backoff bounds for the startup connect. Plain capped exponential rather than
// the dispatcher's jittered backoff: one process racing one database at startup
// has no herd to spread out, and predictable delays keep the budget easy to
// reason about against the liveness probe.
const (
	connectRetryInitialDelay = 250 * time.Millisecond
	connectRetryMaxDelay     = 3 * time.Second
)

// OpenWithRetry is Open with a bounded retry, for use at process startup.
//
// A pod usually reaches its database a moment before the network policy admits
// it, so the first connect is refused through no fault of the database. Exiting
// there turns every rollout into a crash-and-restart. Retrying inside the
// budget rides out that window, and a transient database blip with it.
//
// The budget must stay comfortably below the liveness probe's kill deadline: a
// process that retries past it is killed mid-wait, which is worse than failing
// fast. It gives up rather than blocking forever, so a genuinely unreachable
// database still surfaces loudly, and it abandons the wait as soon as ctx is
// cancelled so shutdown signals are honoured.
func OpenWithRetry(ctx context.Context, databaseURL string, budget time.Duration, onRetry func(attempt int, wait time.Duration, err error)) (*pgxpool.Pool, error) {
	return openWithRetry(ctx, budget, connectRetryInitialDelay, connectRetryMaxDelay, onRetry,
		func(ctx context.Context) (*pgxpool.Pool, error) { return Open(ctx, databaseURL) })
}

// openWithRetry carries the retry policy. The attempt function and delay bounds
// are parameters so tests exercise the real timing path without a database.
func openWithRetry(
	ctx context.Context,
	budget, initialDelay, maxDelay time.Duration,
	onRetry func(attempt int, wait time.Duration, err error),
	attempt func(context.Context) (*pgxpool.Pool, error),
) (*pgxpool.Pool, error) {
	deadline := time.Now().Add(budget)
	delay := initialDelay
	for number := 1; ; number++ {
		pool, err := attempt(ctx)
		if err == nil {
			return pool, nil
		}
		// A cancelled context means we are shutting down, not that the database
		// is unhealthy; report that instead of burning the rest of the budget.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("connect to database within %s: %w", budget, err)
		}
		wait := min(delay, remaining)
		if onRetry != nil {
			onRetry(number, wait, err)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, maxDelay)
	}
}

// Open creates and verifies the application pool. Callers own Close.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := Config(databaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
