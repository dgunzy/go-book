package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeOutboxEvent is the in-memory row backing fakeStore.
type fakeOutboxEvent struct {
	envelope    Envelope
	state       OutboxState
	attempt     int
	maxAttempts int
	availableAt time.Time
	lastError   string
}

// fakeStore is a minimal, non-concurrent-safe-beyond-a-mutex in-memory
// implementation of OutboxStore for unit testing the dispatcher without a
// database.
type fakeStore struct {
	mu           sync.Mutex
	events       map[string]*fakeOutboxEvent
	order        []string
	receipts     map[string]map[string]bool // eventID -> consumer -> recorded
	releaseCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		events:   make(map[string]*fakeOutboxEvent),
		receipts: make(map[string]map[string]bool),
	}
}

func (f *fakeStore) addPending(t *testing.T, envelope Envelope, maxAttempts int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[envelope.ID] = &fakeOutboxEvent{
		envelope:    envelope,
		state:       OutboxPending,
		maxAttempts: maxAttempts,
		availableAt: time.Unix(0, 0).UTC(),
	}
	f.order = append(f.order, envelope.ID)
}

func (f *fakeStore) ClaimBatch(_ context.Context, _ string, limit int) ([]ClaimedEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claimed := make([]ClaimedEvent, 0, limit)
	now := time.Now().UTC()
	for _, id := range f.order {
		if len(claimed) >= limit {
			break
		}
		event := f.events[id]
		if event.state != OutboxPending || event.availableAt.After(now) {
			continue
		}
		event.state = OutboxProcessing
		event.attempt++
		claimed = append(claimed, ClaimedEvent{
			Envelope:     event.envelope,
			OutboxID:     event.envelope.ID,
			AttemptCount: event.attempt,
			MaxAttempts:  event.maxAttempts,
		})
	}
	return claimed, nil
}

func (f *fakeStore) MarkCompleted(_ context.Context, outboxID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	event, ok := f.events[outboxID]
	if !ok || event.state != OutboxProcessing {
		return fmt.Errorf("event %s not processing", outboxID)
	}
	event.state = OutboxCompleted
	return nil
}

func (f *fakeStore) MarkRetry(_ context.Context, outboxID string, availableAt time.Time, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	event, ok := f.events[outboxID]
	if !ok || event.state != OutboxProcessing {
		return fmt.Errorf("event %s not processing", outboxID)
	}
	event.state = OutboxPending
	event.availableAt = availableAt
	event.lastError = lastError
	return nil
}

func (f *fakeStore) MarkFailed(_ context.Context, outboxID string, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	event, ok := f.events[outboxID]
	if !ok || event.state != OutboxProcessing {
		return fmt.Errorf("event %s not processing", outboxID)
	}
	event.state = OutboxFailed
	event.lastError = lastError
	return nil
}

func (f *fakeStore) ReleaseStale(_ context.Context, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	return 0, nil
}

func (f *fakeStore) HasReceipt(_ context.Context, consumer, eventID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.receipts[eventID][consumer], nil
}

func (f *fakeStore) RecordReceipt(_ context.Context, consumer, eventID string, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.receipts[eventID] == nil {
		f.receipts[eventID] = make(map[string]bool)
	}
	f.receipts[eventID][consumer] = true
	return nil
}

func (f *fakeStore) stateOf(id string) OutboxState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[id].state
}

func (f *fakeStore) attemptOf(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[id].attempt
}

func (f *fakeStore) availableAtOf(id string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[id].availableAt
}

func (f *fakeStore) lastErrorOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[id].lastError
}

// fakeConsumer is a Consumer implementation callers configure per test.
type fakeConsumer struct {
	name      string
	handles   func(Type) bool
	handle    func(ctx context.Context, envelope Envelope) error
	callCount int
}

func (c *fakeConsumer) Name() string { return c.name }

func (c *fakeConsumer) Handles(t Type) bool { return c.handles(t) }

func (c *fakeConsumer) Handle(ctx context.Context, envelope Envelope) error {
	c.callCount++
	return c.handle(ctx, envelope)
}

func alwaysHandles(Type) bool { return true }

func testEnvelope(id string) Envelope {
	return Envelope{
		ID:               id,
		AggregateType:    "match",
		AggregateID:      aggregateID,
		AggregateVersion: 1,
		Type:             MatchResultVerified,
		Payload:          []byte(`{}`),
		OccurredAt:       time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
}

func newTestDispatcher(t *testing.T, store OutboxStore, consumers ...Consumer) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(DispatcherConfig{
		Store:        store,
		Consumers:    consumers,
		PollInterval: time.Millisecond,
		BatchSize:    10,
		LockLease:    time.Minute,
		WorkerID:     "test-worker",
		BackoffBase:  time.Second,
		BackoffCap:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	return d
}

func TestDispatcherSuccessfulDispatchRecordsReceipt(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	envelope := testEnvelope(eventID)
	store.addPending(t, envelope, 5)

	consumer := &fakeConsumer{
		name:    "projector",
		handles: alwaysHandles,
		handle:  func(context.Context, Envelope) error { return nil },
	}
	d := newTestDispatcher(t, store, consumer)
	d.tick(context.Background())

	if got := store.stateOf(eventID); got != OutboxCompleted {
		t.Fatalf("event state = %v, want completed", got)
	}
	if consumer.callCount != 1 {
		t.Fatalf("consumer called %d times, want 1", consumer.callCount)
	}
	has, err := store.HasReceipt(context.Background(), "projector", eventID)
	if err != nil || !has {
		t.Fatalf("HasReceipt() = %v, %v, want true, nil", has, err)
	}
}

func TestDispatcherSkipsConsumerWithExistingReceipt(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	envelope := testEnvelope(eventID)
	store.addPending(t, envelope, 5)
	// Simulate a prior crash between Handle succeeding and the receipt
	// being observable on redelivery: the receipt already exists before
	// this claim.
	if err := store.RecordReceipt(context.Background(), "projector", eventID, nil); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	consumer := &fakeConsumer{
		name:    "projector",
		handles: alwaysHandles,
		handle:  func(context.Context, Envelope) error { return errors.New("must not be called") },
	}
	d := newTestDispatcher(t, store, consumer)
	d.tick(context.Background())

	if consumer.callCount != 0 {
		t.Fatalf("consumer called %d times, want 0 (receipt already existed)", consumer.callCount)
	}
	if got := store.stateOf(eventID); got != OutboxCompleted {
		t.Fatalf("event state = %v, want completed", got)
	}
}

func TestDispatcherRetriesThenTerminatesAtMaxAttempts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	envelope := testEnvelope(eventID)
	store.addPending(t, envelope, 2)

	consumer := &fakeConsumer{
		name:    "projector",
		handles: alwaysHandles,
		handle:  func(context.Context, Envelope) error { return errors.New("boom") },
	}
	d := newTestDispatcher(t, store, consumer)
	fixed := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return fixed }
	d.randInt63n = func(int64) int64 { return 0 } // deterministic: availableAt = now + delay/2

	// Attempt 1 of 2: retry, not terminal.
	d.tick(context.Background())
	if got := store.stateOf(eventID); got != OutboxPending {
		t.Fatalf("after attempt 1, state = %v, want pending", got)
	}
	if got := store.attemptOf(eventID); got != 1 {
		t.Fatalf("after attempt 1, attempt count = %d, want 1", got)
	}
	wantAvailable := fixed.Add(d.backoffDelay(1))
	if got := store.availableAtOf(eventID); !got.Equal(wantAvailable) {
		t.Fatalf("availableAt = %v, want %v", got, wantAvailable)
	}
	if consumer.callCount != 1 {
		t.Fatalf("consumer called %d times, want 1", consumer.callCount)
	}

	// Advance the clock past availableAt so the retried event is claimable.
	fixed = wantAvailable.Add(time.Millisecond)

	// Attempt 2 of 2: terminal failure.
	d.tick(context.Background())
	if got := store.stateOf(eventID); got != OutboxFailed {
		t.Fatalf("after attempt 2, state = %v, want failed", got)
	}
	if got := store.lastErrorOf(eventID); got != "boom" {
		t.Fatalf("lastError = %q, want %q", got, "boom")
	}
	if consumer.callCount != 2 {
		t.Fatalf("consumer called %d times, want 2", consumer.callCount)
	}
}

func TestDispatcherMultipleConsumersOneFailsThenRecovers(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	envelope := testEnvelope(eventID)
	store.addPending(t, envelope, 5)

	succeeding := &fakeConsumer{
		name:    "always-ok",
		handles: alwaysHandles,
		handle:  func(context.Context, Envelope) error { return nil },
	}
	var failingCalls int
	failing := &fakeConsumer{
		name:    "flaky",
		handles: alwaysHandles,
		handle: func(context.Context, Envelope) error {
			failingCalls++
			if failingCalls == 1 {
				return errors.New("temporary failure")
			}
			return nil
		},
	}
	d := newTestDispatcher(t, store, succeeding, failing)
	fixed := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return fixed }
	d.randInt63n = func(int64) int64 { return 0 }

	// First delivery: succeeding consumer records a receipt, flaky fails
	// and the whole event is retried.
	d.tick(context.Background())
	if got := store.stateOf(eventID); got != OutboxPending {
		t.Fatalf("after first delivery, state = %v, want pending", got)
	}
	has, _ := store.HasReceipt(context.Background(), "always-ok", eventID)
	if !has {
		t.Fatalf("expected receipt for always-ok consumer after first delivery")
	}
	has, _ = store.HasReceipt(context.Background(), "flaky", eventID)
	if has {
		t.Fatalf("did not expect receipt for flaky consumer after it failed")
	}
	if succeeding.callCount != 1 {
		t.Fatalf("always-ok called %d times, want 1", succeeding.callCount)
	}

	// Advance past the retry delay and redeliver.
	fixed = store.availableAtOf(eventID).Add(time.Millisecond)
	d.tick(context.Background())

	if got := store.stateOf(eventID); got != OutboxCompleted {
		t.Fatalf("after second delivery, state = %v, want completed", got)
	}
	if succeeding.callCount != 1 {
		t.Fatalf("always-ok called %d times on redelivery, want 1 (receipt should skip it)", succeeding.callCount)
	}
	if failingCalls != 2 {
		t.Fatalf("flaky called %d times, want 2", failingCalls)
	}
	has, _ = store.HasReceipt(context.Background(), "flaky", eventID)
	if !has {
		t.Fatalf("expected receipt for flaky consumer after it succeeded")
	}
}

func TestDispatcherRunStopsOnContextCancellation(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	consumer := &fakeConsumer{
		name:    "projector",
		handles: alwaysHandles,
		handle:  func(context.Context, Envelope) error { return nil },
	}
	d := newTestDispatcher(t, store, consumer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}

func TestNewDispatcherValidatesConfig(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	consumer := &fakeConsumer{name: "projector", handles: alwaysHandles, handle: func(context.Context, Envelope) error { return nil }}
	base := DispatcherConfig{
		Store:        store,
		Consumers:    []Consumer{consumer},
		PollInterval: time.Second,
		BatchSize:    10,
		LockLease:    time.Minute,
		WorkerID:     "worker",
		BackoffBase:  time.Second,
		BackoffCap:   time.Minute,
	}
	if _, err := NewDispatcher(base); err != nil {
		t.Fatalf("valid config: NewDispatcher() error = %v", err)
	}

	tests := []struct {
		name   string
		change func(*DispatcherConfig)
	}{
		{"no store", func(c *DispatcherConfig) { c.Store = nil }},
		{"no consumers", func(c *DispatcherConfig) { c.Consumers = nil }},
		{"nil consumer", func(c *DispatcherConfig) { c.Consumers = []Consumer{nil} }},
		{"blank worker id", func(c *DispatcherConfig) { c.WorkerID = "  " }},
		{"non-positive poll interval", func(c *DispatcherConfig) { c.PollInterval = 0 }},
		{"non-positive batch size", func(c *DispatcherConfig) { c.BatchSize = 0 }},
		{"non-positive lock lease", func(c *DispatcherConfig) { c.LockLease = 0 }},
		{"non-positive backoff base", func(c *DispatcherConfig) { c.BackoffBase = 0 }},
		{"backoff cap below base", func(c *DispatcherConfig) { c.BackoffCap = c.BackoffBase / 2 }},
		{"duplicate consumer names", func(c *DispatcherConfig) {
			c.Consumers = []Consumer{consumer, &fakeConsumer{name: "projector", handles: alwaysHandles}}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			test.change(&cfg)
			if _, err := NewDispatcher(cfg); err == nil {
				t.Fatalf("NewDispatcher() error = nil, want error")
			}
		})
	}
}
