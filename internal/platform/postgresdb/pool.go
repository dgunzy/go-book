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
