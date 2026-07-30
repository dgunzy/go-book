package bettingpg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setTotalCap puts a book-wide ceiling on one side. There is no admin store
// method for this yet, so the tests set the column directly.
func setTotalCap(t *testing.T, ctx context.Context, pool *pgxpool.Pool, selectionID string, cents int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE selections SET total_stake_cap_cents = $2 WHERE id = $1::uuid`, selectionID, cents); err != nil {
		t.Fatalf("set total cap: %v", err)
	}
}

// TestTotalSideCapBoundsEveryMemberTogether is the whole point of the cap: the
// per-member limit never bounded the book, so several members each inside their
// own limit could still take the book far past what it agreed to carry.
func TestTotalSideCapBoundsEveryMemberTogether(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	// A generous per-member cap, so nothing here is stopped by a member limit.
	marketID, selections := cappedMarket(t, ctx, store, 90_000, f.UserB)
	setTotalCap(t, ctx, pool, selections[1], 10_000)

	place := func(user string, cents int64, tag string) error {
		wagerID := mustNewUUID(t, ctx, store)
		_, err := store.PlaceWager(ctx, PlaceWagerRequest{
			WagerID: wagerID, UserID: user, MarketID: marketID, SelectionID: selections[1],
			FundingAccountType: betting.FundingUserCash, StakeCents: cents, Currency: ledger.CAD,
			IdempotencyKey: "total-cap:" + tag,
		})
		return err
	}

	if err := place(f.UserA, 6_000, "a1"); err != nil {
		t.Fatalf("first wager refused: %v", err)
	}
	// A different member, well inside their own limit, but the side only has
	// $40 of room left.
	if err := place(f.UserB, 5_000, "b1"); !errors.Is(err, betting.ErrSideFull) {
		t.Fatalf("error = %v, want ErrSideFull", err)
	}
	// The member's own limit must not be blamed for the book's ceiling.
	if err := place(f.UserB, 5_000, "b2"); errors.Is(err, betting.ErrStakeAboveLimit) {
		t.Fatal("a full side was reported as the member's own stake limit")
	}
	// Exactly filling it is fine.
	if err := place(f.UserB, 4_000, "b3"); err != nil {
		t.Fatalf("filling the cap exactly was refused: %v", err)
	}
	if err := place(f.UserA, 1, "a2"); !errors.Is(err, betting.ErrSideFull) {
		t.Fatalf("one cent past a full side was allowed: %v", err)
	}

	var total int64
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(sum(stake_cents), 0) FROM wagers WHERE selection_id = $1::uuid AND state IN ('pending','accepted')`,
		selections[1]).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 10_000 {
		t.Fatalf("side holds %d cents, want the 10000 cap", total)
	}
}

// TestTotalSideCapHoldsUnderConcurrentPlacement is the test the feature exists
// for. Reading the running total outside a lock passes every sequential test
// and still lets two placements arriving together both find room and both
// commit, which is precisely how a cap silently fails to be a cap.
func TestTotalSideCapHoldsUnderConcurrentPlacement(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 500_000)
	marketID, selections := cappedMarket(t, ctx, store, 900_000, f.UserB)
	const cap = 10_000
	setTotalCap(t, ctx, pool, selections[0], cap)

	// Sixteen simultaneous attempts at $60 each against a $100 cap: at most one
	// can win if the cap holds, since two would be $120. Every wager ID is
	// minted before the barrier so the only work inside the race is the
	// placement itself, and a second WaitGroup makes every goroutine wait until
	// all of them are parked on the barrier — without that they trickle in and
	// never actually overlap.
	const attempts = 16
	users := [2]string{f.UserA, f.UserB}
	wagerIDs := make([]string, attempts)
	for i := range wagerIDs {
		wagerIDs[i] = mustNewUUID(t, ctx, store)
	}

	var ready, wg sync.WaitGroup
	ready.Add(attempts)
	wg.Add(attempts)
	results := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		go func(index int) {
			defer wg.Done()
			ready.Done()
			<-start
			_, err := store.PlaceWager(ctx, PlaceWagerRequest{
				WagerID: wagerIDs[index], UserID: users[index%2], MarketID: marketID, SelectionID: selections[0],
				FundingAccountType: betting.FundingUserCash, StakeCents: 6_000, Currency: ledger.CAD,
				IdempotencyKey: "race:" + wagerIDs[index],
			})
			results[index] = err
		}(i)
	}
	ready.Wait()
	close(start)
	wg.Wait()

	accepted := 0
	for i, err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, betting.ErrSideFull):
		default:
			t.Fatalf("attempt %d failed unexpectedly: %v", i, err)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d of %d concurrent wagers were accepted, want exactly 1", accepted, attempts)
	}

	var total int64
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(sum(stake_cents), 0) FROM wagers WHERE selection_id = $1::uuid AND state IN ('pending','accepted')`,
		selections[0]).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total > cap {
		t.Fatalf("side holds %d cents, past its %d cap", total, cap)
	}
}

// TestNoTotalCapLeavesPlacementUnbounded guards against the lock or the new
// column quietly capping sides that were never given one.
func TestNoTotalCapLeavesPlacementUnbounded(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	marketID, selections := cappedMarket(t, ctx, store, 0, f.UserB)

	for i, cents := range []int64{9_000, 9_000, 9_000} {
		wagerID := mustNewUUID(t, ctx, store)
		if _, err := store.PlaceWager(ctx, PlaceWagerRequest{
			WagerID: wagerID, UserID: f.UserA, MarketID: marketID, SelectionID: selections[0],
			FundingAccountType: betting.FundingUserCash, StakeCents: cents, Currency: ledger.CAD,
			IdempotencyKey: "uncapped:" + wagerID,
		}); err != nil {
			t.Fatalf("uncapped wager %d refused: %v", i, err)
		}
	}
}

// TestTotalSideCapTakesTheLockBeforeReadingTheTotal proves the enforcement is
// real rather than advisory, which a timing race cannot: placements are short
// enough that they mostly serialise by luck, so a cap with no lock still looks
// correct under concurrent load. Here the two transactions are interleaved
// deliberately. The second must block on the first, because the moment two
// placements can read the same running total they can both find room and both
// commit past the cap.
func TestTotalSideCapTakesTheLockBeforeReadingTheTotal(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 100_000)
	marketID, selections := cappedMarket(t, ctx, store, 90_000, f.UserB)
	setTotalCap(t, ctx, pool, selections[0], 10_000)

	first, err := store.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if _, err := loadStakeLimits(ctx, first, marketID, selections[0], f.UserA); err != nil {
		t.Fatalf("first read: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		tx, err := store.begin(ctx)
		if err != nil {
			second <- err
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = loadStakeLimits(ctx, tx, marketID, selections[0], f.UserB)
		second <- err
	}()

	select {
	case err := <-second:
		t.Fatalf("a second placement read the side's total while another transaction held it (err=%v): "+
			"without the row lock both can see room and both commit, so the cap is advisory", err)
	case <-time.After(500 * time.Millisecond):
		// Correctly blocked behind the first transaction.
	}

	if err := first.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second read after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second placement never unblocked after the first released the side")
	}
}
