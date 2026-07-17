// Package eventspg implements the durable outbox contracts defined by
// internal/events against PostgreSQL using pgx v5.
package eventspg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// maxLastErrorLength truncates last_error before it reaches Postgres so an
// unusually large error message cannot bloat the outbox table.
const maxLastErrorLength = 2000

// Exec is the minimal write surface Publish needs. Both *pgxpool.Pool and
// pgx.Tx satisfy it, so a domain change and its outbox event can be
// published in the same transaction or, for fire-and-forget callers,
// directly against the pool.
type Exec interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Publish inserts envelope into outbox_events. The insert is idempotent:
// republishing the same (aggregate_type, aggregate_id, aggregate_version,
// event_type) tuple within a retried domain transaction is a no-op, so
// callers may safely publish before or after retrying the surrounding
// transaction.
func Publish(ctx context.Context, db Exec, envelope events.Envelope, maxAttempts int) error {
	if db == nil {
		return errors.New("eventspg: exec is required")
	}
	if err := envelope.Validate(); err != nil {
		return err
	}
	if maxAttempts < 1 || maxAttempts > 100 {
		return fmt.Errorf("eventspg: max attempts must be between 1 and 100, got %d", maxAttempts)
	}
	_, err := db.Exec(ctx, `
INSERT INTO outbox_events (id, aggregate_type, aggregate_id, aggregate_version, event_type, payload,
                            occurred_at, max_attempts, correlation_id, causation_id)
VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::jsonb, $7, $8, nullif($9, '')::uuid, nullif($10, '')::uuid)
ON CONFLICT (aggregate_type, aggregate_id, aggregate_version, event_type) DO NOTHING`,
		envelope.ID, envelope.AggregateType, envelope.AggregateID, envelope.AggregateVersion,
		string(envelope.Type), []byte(envelope.Payload), envelope.OccurredAt, maxAttempts,
		envelope.CorrelationID, envelope.CausationID)
	if err != nil {
		return fmt.Errorf("publish outbox event: %w", err)
	}
	return nil
}

// Pool is the PostgreSQL surface Store needs. *pgxpool.Pool satisfies it;
// callers inject the interface rather than the concrete pool, mirroring
// legacybook.PostgresDB.
type Pool interface {
	Exec
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store implements events.OutboxStore against PostgreSQL.
type Store struct{ Pool Pool }

func (s Store) ClaimBatch(ctx context.Context, workerID string, limit int) ([]events.ClaimedEvent, error) {
	if s.Pool == nil {
		return nil, errors.New("eventspg: pool is required")
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("eventspg: worker id is required")
	}
	if limit <= 0 {
		return nil, errors.New("eventspg: limit must be positive")
	}
	rows, err := s.Pool.Query(ctx, `
UPDATE outbox_events
SET state = 'processing', locked_at = now(), locked_by = $2, attempt_count = attempt_count + 1
WHERE id IN (
    SELECT id FROM outbox_events
    WHERE state = 'pending' AND available_at <= now()
    ORDER BY available_at, occurred_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING id::text, aggregate_type, aggregate_id::text, aggregate_version, event_type, payload, occurred_at,
          coalesce(correlation_id::text, ''), coalesce(causation_id::text, ''), attempt_count, max_attempts`,
		limit, workerID)
	if err != nil {
		return nil, fmt.Errorf("claim outbox batch: %w", err)
	}
	defer rows.Close()

	claimed := make([]events.ClaimedEvent, 0, limit)
	for rows.Next() {
		var event events.ClaimedEvent
		var eventType string
		var payload []byte
		if err := rows.Scan(&event.OutboxID, &event.AggregateType, &event.AggregateID, &event.AggregateVersion,
			&eventType, &payload, &event.OccurredAt, &event.CorrelationID, &event.CausationID,
			&event.AttemptCount, &event.MaxAttempts); err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		event.ID = event.OutboxID
		event.Type = events.Type(eventType)
		event.Payload = payload
		event.OccurredAt = event.OccurredAt.UTC()
		claimed = append(claimed, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim outbox batch: %w", err)
	}
	return claimed, nil
}

func (s Store) MarkCompleted(ctx context.Context, outboxID string) error {
	if s.Pool == nil {
		return errors.New("eventspg: pool is required")
	}
	tag, err := s.Pool.Exec(ctx, `
UPDATE outbox_events
SET state = 'completed', completed_at = now(), locked_at = NULL, locked_by = NULL,
    failed_at = NULL, last_error = NULL
WHERE id = $1::uuid AND state = 'processing'`, outboxID)
	if err != nil {
		return fmt.Errorf("mark outbox event completed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark outbox event %s completed: not in processing state", outboxID)
	}
	return nil
}

func (s Store) MarkRetry(ctx context.Context, outboxID string, availableAt time.Time, lastError string) error {
	if s.Pool == nil {
		return errors.New("eventspg: pool is required")
	}
	tag, err := s.Pool.Exec(ctx, `
UPDATE outbox_events
SET state = 'pending', available_at = $2, locked_at = NULL, locked_by = NULL,
    completed_at = NULL, failed_at = NULL, last_error = $3
WHERE id = $1::uuid AND state = 'processing'`, outboxID, availableAt, truncateError(lastError))
	if err != nil {
		return fmt.Errorf("mark outbox event retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark outbox event %s for retry: not in processing state", outboxID)
	}
	return nil
}

func (s Store) MarkFailed(ctx context.Context, outboxID string, lastError string) error {
	if s.Pool == nil {
		return errors.New("eventspg: pool is required")
	}
	truncated := truncateError(lastError)
	if strings.TrimSpace(truncated) == "" {
		truncated = "unknown error"
	}
	tag, err := s.Pool.Exec(ctx, `
UPDATE outbox_events
SET state = 'failed', failed_at = now(), locked_at = NULL, locked_by = NULL,
    completed_at = NULL, last_error = $2
WHERE id = $1::uuid AND state = 'processing'`, outboxID, truncated)
	if err != nil {
		return fmt.Errorf("mark outbox event failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark outbox event %s failed: not in processing state", outboxID)
	}
	return nil
}

// ReleaseStale returns processing rows whose lock predates the lease back
// to pending. It does not reset attempt_count because ClaimBatch already
// counted the attempt for the worker that abandoned the row.
func (s Store) ReleaseStale(ctx context.Context, olderThan time.Duration) (int, error) {
	if s.Pool == nil {
		return 0, errors.New("eventspg: pool is required")
	}
	if olderThan <= 0 {
		return 0, errors.New("eventspg: lease duration must be positive")
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	tag, err := s.Pool.Exec(ctx, `
UPDATE outbox_events
SET state = 'pending', locked_at = NULL, locked_by = NULL
WHERE state = 'processing' AND locked_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("release stale outbox locks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s Store) HasReceipt(ctx context.Context, consumer, eventID string) (bool, error) {
	if s.Pool == nil {
		return false, errors.New("eventspg: pool is required")
	}
	var exists bool
	err := s.Pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM event_receipts WHERE consumer = $1 AND event_id = $2::uuid)`,
		consumer, eventID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check event receipt: %w", err)
	}
	return exists, nil
}

func (s Store) RecordReceipt(ctx context.Context, consumer, eventID string, result map[string]any) error {
	if s.Pool == nil {
		return errors.New("eventspg: pool is required")
	}
	payload := result
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode receipt result: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
INSERT INTO event_receipts (consumer, event_id, result)
VALUES ($1, $2::uuid, $3::jsonb)
ON CONFLICT (consumer, event_id) DO NOTHING`, consumer, eventID, string(encoded))
	if err != nil {
		return fmt.Errorf("record event receipt: %w", err)
	}
	return nil
}

func truncateError(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) <= maxLastErrorLength {
		return msg
	}
	runes := []rune(msg)
	if len(runes) <= maxLastErrorLength {
		return msg
	}
	return string(runes[:maxLastErrorLength])
}
