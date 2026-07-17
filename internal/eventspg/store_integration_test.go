package eventspg

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("EVENTSPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EVENTSPG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newTestUUID asks PostgreSQL for a fresh random UUID so tests do not need
// their own UUID generator dependency.
func newTestUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("generate uuid: %v", err)
	}
	return id
}

func newTestEnvelope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aggregateVersion int64) events.Envelope {
	t.Helper()
	return events.Envelope{
		ID:               newTestUUID(t, ctx, pool),
		AggregateType:    "match",
		AggregateID:      newTestUUID(t, ctx, pool),
		AggregateVersion: aggregateVersion,
		Type:             events.MatchResultVerified,
		Payload:          []byte(`{"integration":true}`),
		OccurredAt:       time.Now().UTC(),
	}
}

func cleanupOutboxEvent(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM event_receipts WHERE event_id = $1::uuid`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE id = $1::uuid`, id)
	})
}

func TestPublishIsIdempotentUnderRepublish(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	envelope := newTestEnvelope(t, ctx, pool, 1)
	cleanupOutboxEvent(t, pool, envelope.ID)

	if err := Publish(ctx, pool, envelope, 5); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	// A retried domain transaction republishes the identical envelope; the
	// unique constraint on (aggregate_type, aggregate_id, aggregate_version,
	// event_type) must make this a no-op rather than an error.
	if err := Publish(ctx, pool, envelope, 5); err != nil {
		t.Fatalf("republish Publish() error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE id = $1::uuid`, envelope.ID).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("outbox row count = %d, want 1", count)
	}
}

func TestPublishRejectsInvalidEnvelope(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	invalid := newTestEnvelope(t, ctx, pool, 0) // zero version is invalid
	if err := Publish(ctx, pool, invalid, 5); !errors.Is(err, events.ErrInvalidEnvelope) {
		t.Fatalf("Publish() error = %v, want ErrInvalidEnvelope", err)
	}
}

func TestStoreFullPendingProcessingCompletedFlow(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	envelope := newTestEnvelope(t, ctx, pool, 1)
	cleanupOutboxEvent(t, pool, envelope.ID)
	if err := Publish(ctx, pool, envelope, 5); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	store := Store{Pool: pool}
	claimed, err := store.ClaimBatch(ctx, "worker-a", 10)
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	found := findClaimed(claimed, envelope.ID)
	if found == nil {
		t.Fatalf("published event was not claimed")
	}
	if found.AttemptCount != 1 {
		t.Fatalf("attempt count = %d, want 1", found.AttemptCount)
	}
	if found.MaxAttempts != 5 {
		t.Fatalf("max attempts = %d, want 5", found.MaxAttempts)
	}
	if found.Type != events.MatchResultVerified {
		t.Fatalf("type = %q, want %q", found.Type, events.MatchResultVerified)
	}

	assertState(t, ctx, pool, envelope.ID, "processing")

	has, err := store.HasReceipt(ctx, "projector", envelope.ID)
	if err != nil || has {
		t.Fatalf("HasReceipt() = %v, %v, want false, nil", has, err)
	}
	if err := store.RecordReceipt(ctx, "projector", envelope.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("RecordReceipt() error = %v", err)
	}
	has, err = store.HasReceipt(ctx, "projector", envelope.ID)
	if err != nil || !has {
		t.Fatalf("HasReceipt() = %v, %v, want true, nil", has, err)
	}
	// Duplicate receipt insert must stay a no-op.
	if err := store.RecordReceipt(ctx, "projector", envelope.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("duplicate RecordReceipt() error = %v", err)
	}
	var receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_receipts WHERE consumer = $1 AND event_id = $2::uuid`,
		"projector", envelope.ID).Scan(&receiptCount); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receiptCount != 1 {
		t.Fatalf("receipt count = %d, want 1 (unique consumer, event_id)", receiptCount)
	}

	if err := store.MarkCompleted(ctx, envelope.ID); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	assertState(t, ctx, pool, envelope.ID, "completed")
}

func TestStoreRetryThenTerminalFailure(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	envelope := newTestEnvelope(t, ctx, pool, 1)
	cleanupOutboxEvent(t, pool, envelope.ID)
	if err := Publish(ctx, pool, envelope, 2); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	store := Store{Pool: pool}

	claimed, err := store.ClaimBatch(ctx, "worker-a", 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v, want 1 event, nil", claimed, err)
	}
	if err := store.MarkRetry(ctx, envelope.ID, time.Now().UTC().Add(-time.Second), "temporary failure"); err != nil {
		t.Fatalf("MarkRetry() error = %v", err)
	}
	assertState(t, ctx, pool, envelope.ID, "pending")

	claimed, err = store.ClaimBatch(ctx, "worker-a", 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch() after retry = %v, %v, want 1 event, nil", claimed, err)
	}
	if claimed[0].AttemptCount != 2 {
		t.Fatalf("attempt count after retry claim = %d, want 2", claimed[0].AttemptCount)
	}

	if err := store.MarkFailed(ctx, envelope.ID, "terminal failure"); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	assertState(t, ctx, pool, envelope.ID, "failed")

	var lastError string
	if err := pool.QueryRow(ctx, `SELECT last_error FROM outbox_events WHERE id = $1::uuid`, envelope.ID).Scan(&lastError); err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	if lastError != "terminal failure" {
		t.Fatalf("last_error = %q, want %q", lastError, "terminal failure")
	}
}

func TestStoreReleasesStaleLocks(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	envelope := newTestEnvelope(t, ctx, pool, 1)
	cleanupOutboxEvent(t, pool, envelope.ID)
	if err := Publish(ctx, pool, envelope, 5); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	store := Store{Pool: pool}
	if _, err := store.ClaimBatch(ctx, "worker-a", 10); err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	assertState(t, ctx, pool, envelope.ID, "processing")

	// Back-date the lock so it looks abandoned by a crashed worker.
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET locked_at = now() - interval '1 hour' WHERE id = $1::uuid`, envelope.ID); err != nil {
		t.Fatalf("back-date lock: %v", err)
	}

	released, err := store.ReleaseStale(ctx, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReleaseStale() error = %v", err)
	}
	if released < 1 {
		t.Fatalf("released = %d, want at least 1", released)
	}
	assertState(t, ctx, pool, envelope.ID, "pending")

	var attempt int
	if err := pool.QueryRow(ctx, `SELECT attempt_count FROM outbox_events WHERE id = $1::uuid`, envelope.ID).Scan(&attempt); err != nil {
		t.Fatalf("read attempt_count: %v", err)
	}
	if attempt != 1 {
		t.Fatalf("attempt_count after stale release = %d, want 1 (unchanged)", attempt)
	}
}

func TestStoreConcurrentClaimersNeverShareAnEvent(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const eventCount = 20
	ids := make([]string, 0, eventCount)
	for i := 0; i < eventCount; i++ {
		envelope := newTestEnvelope(t, ctx, pool, 1)
		cleanupOutboxEvent(t, pool, envelope.ID)
		if err := Publish(ctx, pool, envelope, 5); err != nil {
			t.Fatalf("Publish() error = %v", err)
		}
		ids = append(ids, envelope.ID)
	}

	store := Store{Pool: pool}
	const claimers = 4
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]string) // event id -> worker id
	errCh := make(chan error, claimers)

	for i := 0; i < claimers; i++ {
		wg.Add(1)
		workerID := "worker-" + string(rune('a'+i))
		go func(workerID string) {
			defer wg.Done()
			claimed, err := store.ClaimBatch(ctx, workerID, eventCount)
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, event := range claimed {
				if owner, dup := seen[event.OutboxID]; dup {
					errCh <- errors.New("event " + event.OutboxID + " claimed by both " + owner + " and " + workerID)
					continue
				}
				seen[event.OutboxID] = workerID
			}
		}(workerID)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if len(seen) != eventCount {
		t.Fatalf("claimed %d distinct events across workers, want %d", len(seen), eventCount)
	}
}

func findClaimed(claimed []events.ClaimedEvent, id string) *events.ClaimedEvent {
	for i := range claimed {
		if claimed[i].OutboxID == id {
			return &claimed[i]
		}
	}
	return nil
}

func assertState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT state FROM outbox_events WHERE id = $1::uuid`, id).Scan(&got); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got != want {
		t.Fatalf("state = %q, want %q", got, want)
	}
}
