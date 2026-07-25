package postgresdb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfig(t *testing.T) {
	t.Parallel()

	config, err := Config("postgres://member:secret@db.example/cabot_cup?sslmode=require")
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	if config.MaxConns != 8 || config.MinConns != 1 {
		t.Fatalf("connections = %d/%d, want 8/1", config.MaxConns, config.MinConns)
	}
	if got := config.ConnConfig.RuntimeParams["application_name"]; got != "cabot-cup-web" {
		t.Fatalf("application_name = %q", got)
	}
	if got := config.ConnConfig.RuntimeParams["statement_timeout"]; got != "10s" {
		t.Fatalf("statement_timeout = %q", got)
	}
}

func TestConfigRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	if _, err := Config("://not-a-url"); err == nil {
		t.Fatal("Config() error = nil, want invalid URL")
	}
}

// The retry policy is exercised without a database: the attempt function stands
// in for the connect, so these assert the policy itself rather than pgx.
func TestOpenWithRetryRidesOutAnUnreachableStart(t *testing.T) {
	t.Parallel()

	refused := errors.New("connect: connection refused")
	attempts := 0
	var retries []int
	pool, err := openWithRetry(context.Background(), time.Second, time.Millisecond, 2*time.Millisecond,
		func(attempt int, _ time.Duration, _ error) { retries = append(retries, attempt) },
		func(context.Context) (*pgxpool.Pool, error) {
			attempts++
			if attempts < 3 {
				return nil, refused
			}
			return nil, nil // a real pool is not needed to prove the policy
		})
	if err != nil {
		t.Fatalf("openWithRetry() error = %v, want success on the third attempt", err)
	}
	if pool != nil {
		t.Fatal("stub returned a nil pool; got something else")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if len(retries) != 2 {
		t.Errorf("retry callbacks = %v, want one per failed attempt", retries)
	}
}

func TestOpenWithRetryGivesUpAtTheBudget(t *testing.T) {
	t.Parallel()

	refused := errors.New("connect: connection refused")
	attempts := 0
	start := time.Now()
	_, err := openWithRetry(context.Background(), 30*time.Millisecond, time.Millisecond, 5*time.Millisecond, nil,
		func(context.Context) (*pgxpool.Pool, error) {
			attempts++
			return nil, refused
		})
	if err == nil {
		t.Fatal("openWithRetry() error = nil, wanted the budget to run out")
	}
	// A permanently unreachable database must still surface loudly, and the
	// underlying cause must survive so the log says why.
	if !errors.Is(err, refused) {
		t.Errorf("error = %v, want it to wrap the connect failure", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gave up after %s; the budget should bound it", elapsed)
	}
	if attempts < 2 {
		t.Errorf("attempts = %d, want more than one before giving up", attempts)
	}
}

func TestOpenWithRetryStopsWhenTheProcessIsShuttingDown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := openWithRetry(ctx, time.Minute, 10*time.Millisecond, time.Second, nil,
		func(context.Context) (*pgxpool.Pool, error) {
			attempts++
			cancel() // a SIGTERM arriving mid-wait
			return nil, errors.New("connect: connection refused")
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled so shutdown is not delayed", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want the retry loop to stop immediately", attempts)
	}
}
