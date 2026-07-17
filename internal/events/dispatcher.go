package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"
)

// Consumer processes envelopes for the event types it declares interest in.
//
// Handle must be idempotent on its own. Receipts recorded by the dispatcher
// are a dedupe aid, not a substitute for consumer-side idempotency: a crash
// between a successful Handle call and the dispatcher recording the receipt
// redelivers the same event on the next claim.
type Consumer interface {
	// Name identifies the consumer for receipts and logs. It must be stable
	// across process restarts and 1-120 characters, matching the
	// event_receipts.consumer column constraint.
	Name() string
	// Handles reports whether this consumer processes events of type t.
	Handles(t Type) bool
	// Handle processes envelope. A returned error is retried with backoff
	// until the claimed event's attempt count reaches its max attempts,
	// after which the event is marked terminally failed.
	Handle(ctx context.Context, envelope Envelope) error
}

// ClaimedEvent is an outbox row claimed for processing by a dispatcher
// worker.
type ClaimedEvent struct {
	Envelope
	OutboxID     string
	AttemptCount int
	MaxAttempts  int
}

// OutboxStore is the durable outbox persistence contract the dispatcher
// drives. internal/eventspg implements this against PostgreSQL.
type OutboxStore interface {
	// ClaimBatch atomically transitions up to limit pending, due events to
	// processing for workerID and returns them.
	ClaimBatch(ctx context.Context, workerID string, limit int) ([]ClaimedEvent, error)
	// MarkCompleted transitions a processing event to completed.
	MarkCompleted(ctx context.Context, outboxID string) error
	// MarkRetry returns a processing event to pending, available again at
	// availableAt, recording lastError for operator visibility.
	MarkRetry(ctx context.Context, outboxID string, availableAt time.Time, lastError string) error
	// MarkFailed terminally fails a processing event, recording lastError.
	MarkFailed(ctx context.Context, outboxID string, lastError string) error
	// ReleaseStale returns processing events whose lock was acquired more
	// than olderThan ago back to pending, without resetting their attempt
	// count, and reports how many rows were released.
	ReleaseStale(ctx context.Context, olderThan time.Duration) (int, error)
	// HasReceipt reports whether consumer already processed eventID.
	HasReceipt(ctx context.Context, consumer, eventID string) (bool, error)
	// RecordReceipt durably records that consumer processed eventID.
	RecordReceipt(ctx context.Context, consumer, eventID string, result map[string]any) error
}

// DispatcherConfig configures a Dispatcher. All fields except Logger are
// required; NewDispatcher validates them.
type DispatcherConfig struct {
	Store        OutboxStore
	Consumers    []Consumer
	PollInterval time.Duration
	BatchSize    int
	LockLease    time.Duration
	WorkerID     string
	BackoffBase  time.Duration
	BackoffCap   time.Duration
	Logger       *slog.Logger
}

// Dispatcher polls the durable outbox and drives registered consumers.
// Correctness never depends on LISTEN/NOTIFY delivery; Run is a plain
// polling loop and any wake-up notification is only an optimization a
// caller may add around it.
type Dispatcher struct {
	store        OutboxStore
	consumers    []Consumer
	pollInterval time.Duration
	batchSize    int
	lockLease    time.Duration
	workerID     string
	backoffBase  time.Duration
	backoffCap   time.Duration
	logger       *slog.Logger

	// now and randInt63n are overridden in tests for deterministic backoff
	// scheduling; production callers always get the zero-value defaults
	// assigned by NewDispatcher.
	now        func() time.Time
	randInt63n func(int64) int64
}

// NewDispatcher validates cfg and constructs a Dispatcher.
func NewDispatcher(cfg DispatcherConfig) (*Dispatcher, error) {
	if cfg.Store == nil {
		return nil, errors.New("dispatcher: outbox store is required")
	}
	if len(cfg.Consumers) == 0 {
		return nil, errors.New("dispatcher: at least one consumer is required")
	}
	seen := make(map[string]struct{}, len(cfg.Consumers))
	for _, consumer := range cfg.Consumers {
		if consumer == nil {
			return nil, errors.New("dispatcher: consumer must not be nil")
		}
		name := consumer.Name()
		trimmed := strings.TrimSpace(name)
		if len(trimmed) == 0 || len(name) > 120 {
			return nil, fmt.Errorf("dispatcher: consumer name %q must be 1-120 characters", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("dispatcher: duplicate consumer name %q", name)
		}
		seen[name] = struct{}{}
	}
	if strings.TrimSpace(cfg.WorkerID) == "" {
		return nil, errors.New("dispatcher: worker id is required")
	}
	if cfg.PollInterval <= 0 {
		return nil, errors.New("dispatcher: poll interval must be positive")
	}
	if cfg.BatchSize <= 0 {
		return nil, errors.New("dispatcher: batch size must be positive")
	}
	if cfg.LockLease <= 0 {
		return nil, errors.New("dispatcher: lock lease must be positive")
	}
	if cfg.BackoffBase <= 0 {
		return nil, errors.New("dispatcher: backoff base must be positive")
	}
	if cfg.BackoffCap < cfg.BackoffBase {
		return nil, errors.New("dispatcher: backoff cap must be greater than or equal to backoff base")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		store:        cfg.Store,
		consumers:    append([]Consumer(nil), cfg.Consumers...),
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		lockLease:    cfg.LockLease,
		workerID:     cfg.WorkerID,
		backoffBase:  cfg.BackoffBase,
		backoffCap:   cfg.BackoffCap,
		logger:       logger,
		now:          time.Now,
		randInt63n:   rand.Int63n,
	}, nil
}

// Run polls until ctx is cancelled. Each tick releases stale processing
// locks, then claims and processes one batch. Run returns ctx.Err() when
// the context is done.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		d.tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	if released, err := d.store.ReleaseStale(ctx, d.lockLease); err != nil {
		d.logger.Error("release stale outbox locks", "error", err)
	} else if released > 0 {
		d.logger.Info("released stale outbox locks", "count", released)
	}

	claimed, err := d.store.ClaimBatch(ctx, d.workerID, d.batchSize)
	if err != nil {
		d.logger.Error("claim outbox batch", "worker", d.workerID, "error", err)
		return
	}
	for _, event := range claimed {
		if ctx.Err() != nil {
			return
		}
		d.processEvent(ctx, event)
	}
}

func (d *Dispatcher) processEvent(ctx context.Context, event ClaimedEvent) {
	var failure error
	for _, consumer := range d.consumers {
		if !consumer.Handles(event.Type) {
			continue
		}
		done, err := d.store.HasReceipt(ctx, consumer.Name(), event.OutboxID)
		if err != nil {
			d.logger.Error("check event receipt", "event_id", event.OutboxID, "event_type", event.Type,
				"consumer", consumer.Name(), "error", err)
			failure = err
			break
		}
		if done {
			continue
		}
		if err := consumer.Handle(ctx, event.Envelope); err != nil {
			d.logger.Error("consumer handle failed", "event_id", event.OutboxID, "event_type", event.Type,
				"consumer", consumer.Name(), "attempt", event.AttemptCount, "error", err)
			failure = err
			break
		}
		if err := d.store.RecordReceipt(ctx, consumer.Name(), event.OutboxID, map[string]any{}); err != nil {
			d.logger.Error("record event receipt", "event_id", event.OutboxID, "event_type", event.Type,
				"consumer", consumer.Name(), "error", err)
			failure = err
			break
		}
	}

	if failure != nil {
		d.handleFailure(ctx, event, failure)
		return
	}
	if err := d.store.MarkCompleted(ctx, event.OutboxID); err != nil {
		d.logger.Error("mark outbox event completed", "event_id", event.OutboxID, "event_type", event.Type, "error", err)
	}
}

func (d *Dispatcher) handleFailure(ctx context.Context, event ClaimedEvent, cause error) {
	if event.AttemptCount >= event.MaxAttempts {
		if err := d.store.MarkFailed(ctx, event.OutboxID, cause.Error()); err != nil {
			d.logger.Error("mark outbox event failed", "event_id", event.OutboxID, "event_type", event.Type, "error", err)
		}
		return
	}
	availableAt := d.now().UTC().Add(d.backoffDelay(event.AttemptCount))
	if err := d.store.MarkRetry(ctx, event.OutboxID, availableAt, cause.Error()); err != nil {
		d.logger.Error("mark outbox event retry", "event_id", event.OutboxID, "event_type", event.Type, "error", err)
	}
}

// backoffDelay computes an exponential backoff with equal jitter: the
// result is uniformly distributed in [delay/2, delay], where
// delay = min(backoffCap, backoffBase*2^(attempt-1)). Equal jitter avoids
// both a thundering herd on retry and a delay of zero.
func (d *Dispatcher) backoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	const maxShift = 62 // avoid overflowing time.Duration when shifting
	if shift > maxShift {
		shift = maxShift
	}
	delay := d.backoffBase * time.Duration(1<<uint(shift))
	if delay <= 0 || delay > d.backoffCap {
		delay = d.backoffCap
	}
	half := delay / 2
	jitter := time.Duration(0)
	if half > 0 {
		jitter = time.Duration(d.randInt63n(int64(half) + 1))
	}
	return half + jitter
}
