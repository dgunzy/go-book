package bettingpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// buildPricedMarket creates a dynamically-priced two-way open market with even
// -110 lines and two funded users, returning the market and selection IDs.
func buildPricedMarket(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store Store, liquidityCents, openingBalanceCents int64) (marketID string, users [2]string, selections [2]string) {
	t.Helper()
	admin := makeUser(t, ctx, pool, "Priced Admin")
	users[0] = makeUser(t, ctx, pool, "Priced Member A")
	users[1] = makeUser(t, ctx, pool, "Priced Member B")
	fundUser(t, ctx, pool, users[0], ledger.CAD, openingBalanceCents, "priced-fund-a:"+users[0])
	fundUser(t, ctx, pool, users[1], ledger.CAD, openingBalanceCents, "priced-fund-b:"+users[1])

	marketID = mustNewUUID(t, ctx, store)
	req := CreateMarketRequest{
		MarketID: marketID, Type: betting.MarketFuture, Title: "Priced " + marketID,
		Currency: ledger.CAD, ClosesAt: time.Now().UTC().Add(72 * time.Hour).Truncate(time.Microsecond),
		DynamicPricing: true, PricingLiquidityCents: liquidityCents,
		Selections: []CreateMarketSelection{
			{Key: "north", DisplayTerms: "Team North wins", OfferedAmericanOdds: -110},
			{Key: "south", DisplayTerms: "Team South wins", OfferedAmericanOdds: -110},
		},
		ActorUserID: admin,
	}
	if _, err := store.CreateMarket(ctx, req); err != nil {
		t.Fatalf("CreateMarket() error = %v", err)
	}
	if err := store.OpenMarket(ctx, marketID, admin); err != nil {
		t.Fatalf("OpenMarket() error = %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM selection_price_changes WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid OR aggregate_id IN (SELECT id FROM wagers WHERE market_id = $1::uuid)`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM wagers WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, marketID)
	})

	rows, err := pool.Query(ctx, `SELECT id::text FROM selections WHERE market_id = $1::uuid ORDER BY selection_key`, marketID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Fatalf("selections = %d, want 2", len(ids))
	}
	// selection_key order: "north" < "south".
	selections[0], selections[1] = ids[0], ids[1]
	return marketID, users, selections
}

func offeredOdds(t *testing.T, ctx context.Context, pool *pgxpool.Pool, selectionID string) int32 {
	t.Helper()
	var odds int32
	if err := pool.QueryRow(ctx, `SELECT offered_american_odds FROM selections WHERE id = $1::uuid`, selectionID).Scan(&odds); err != nil {
		t.Fatal(err)
	}
	return odds
}

func TestRepriceSkewsLineOnAcceptedAction(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 200_000)
	north, south := selections[0], selections[1]

	// A member places and accepts a $1,000 wager on North.
	wager := placeAndAcceptSelection(t, ctx, store, marketID, users[0], north, 100_000, "priced-a")

	// The accepted wager keeps the line it was quoted (-110), immutably.
	if wager.AcceptedOdds != -110 {
		t.Fatalf("accepted odds = %d, want the -110 it was quoted", wager.AcceptedOdds)
	}

	changed, err := store.RepriceMarketAfterWager(ctx, marketID, string(wager.ID))
	if err != nil {
		t.Fatalf("RepriceMarketAfterWager() error = %v", err)
	}
	if !changed {
		t.Fatal("line did not move after $1,000 of action")
	}

	northOdds := offeredOdds(t, ctx, pool, north)
	southOdds := offeredOdds(t, ctx, pool, south)
	// Backed side (North) shortens (more negative); light side (South) lengthens.
	if northOdds >= -110 {
		t.Fatalf("North line did not shorten: -110 -> %d", northOdds)
	}
	if southOdds <= -110 {
		t.Fatalf("South line did not lengthen: -110 -> %d", southOdds)
	}

	// The already-accepted wager's snapshot is unchanged by repricing.
	var snapshot int32
	if err := pool.QueryRow(ctx, `SELECT accepted_american_odds FROM wagers WHERE id = $1::uuid`, string(wager.ID)).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != -110 {
		t.Fatalf("accepted wager odds mutated to %d; snapshots must be immutable", snapshot)
	}

	// One price-change row per moved selection was recorded.
	var changeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selection_price_changes WHERE market_id = $1::uuid`, marketID).Scan(&changeCount); err != nil {
		t.Fatal(err)
	}
	if changeCount != 2 {
		t.Fatalf("price change rows = %d, want 2", changeCount)
	}

	// Re-running the reprice for the same state is a no-op: no further change,
	// no duplicate audit rows.
	changedAgain, err := store.RepriceMarketAfterWager(ctx, marketID, string(wager.ID))
	if err != nil {
		t.Fatalf("idempotent reprice error = %v", err)
	}
	if changedAgain {
		t.Fatal("reprice moved the line a second time with no new action")
	}
	if got := offeredOdds(t, ctx, pool, north); got != northOdds {
		t.Fatalf("North line drifted on idempotent reprice: %d -> %d", northOdds, got)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selection_price_changes WHERE market_id = $1::uuid`, marketID).Scan(&changeCount); err != nil {
		t.Fatal(err)
	}
	if changeCount != 2 {
		t.Fatalf("price change rows after idempotent reprice = %d, want 2", changeCount)
	}
}

func TestRepriceSkippedWhenDisabled(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	// buildFixture makes a non-dynamic market.
	f := buildFixture(t, ctx, pool, 200_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 100_000, 1)

	changed, err := store.RepriceMarketAfterWager(ctx, f.MarketID, string(wager.ID))
	if err != nil {
		t.Fatalf("RepriceMarketAfterWager() error = %v", err)
	}
	if changed {
		t.Fatal("a market without dynamic pricing must not move its line")
	}
}

func TestPricingConsumerMovesLine(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 200_000)
	north := selections[0]
	wager := placeAndAcceptSelection(t, ctx, store, marketID, users[0], north, 100_000, "consumer-a")

	consumer := &PricingConsumer{Store: &store}
	if !consumer.Handles(events.WagerAccepted) {
		t.Fatal("PricingConsumer must handle WagerAccepted")
	}
	envelope := events.Envelope{
		ID: mustNewUUID(t, ctx, store), AggregateType: "wager", AggregateID: string(wager.ID),
		AggregateVersion: 1, Type: events.WagerAccepted, OccurredAt: time.Now().UTC(),
		Payload: mustMarshal(t, map[string]any{"wager_id": string(wager.ID), "market_id": marketID}),
	}
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("consumer Handle() error = %v", err)
	}
	if got := offeredOdds(t, ctx, pool, north); got >= -110 {
		t.Fatalf("consumer did not shorten the backed line: -110 -> %d", got)
	}
	// Redelivery is safe.
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("consumer redelivery error = %v", err)
	}
}

func TestAutoApproveAcceptsWithSystemActor(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	wagerID := mustNewUUID(t, ctx, store)
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: f.UserA, MarketID: f.MarketID, SelectionID: f.SelectionAID,
		FundingAccountType: betting.FundingUserCash, StakeCents: 2_500, Currency: f.Currency,
		IdempotencyKey: "auto-approve:" + f.Suffix,
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}

	accepted, err := store.AcceptWager(ctx, string(wager.ID), AutoApproveActor)
	if err != nil {
		t.Fatalf("AcceptWager(system) error = %v", err)
	}
	if accepted.State != betting.WagerAccepted {
		t.Fatalf("state = %v, want accepted", accepted.State)
	}
	var acceptedByNull bool
	if err := pool.QueryRow(ctx, `SELECT accepted_by IS NULL FROM wagers WHERE id = $1::uuid`, string(wager.ID)).Scan(&acceptedByNull); err != nil {
		t.Fatal(err)
	}
	if !acceptedByNull {
		t.Fatal("auto-approved wager should record accepted_by NULL for the system actor")
	}
}

func TestAcceptInvalidatesWagerWhenLineMovedWhilePending(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID, users, selections := buildPricedMarket(t, ctx, pool, store, 500_000, 200_000)
	north := selections[0]
	admin := makeUser(t, ctx, pool, "Invalidate Admin")

	// Bettor A places on north and is left pending (quoted the opening -110).
	wagerAID := mustNewUUID(t, ctx, store)
	wagerA, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerAID, UserID: users[0], MarketID: marketID, SelectionID: north,
		FundingAccountType: betting.FundingUserCash, StakeCents: 1_000, Currency: ledger.CAD,
		IdempotencyKey: "invalidate-a:" + wagerAID,
	})
	if err != nil {
		t.Fatalf("PlaceWager(A) error = %v", err)
	}
	if wagerA.State != betting.WagerPending {
		t.Fatalf("A state = %v, want pending", wagerA.State)
	}

	// Bettor B's accepted action then moves the line while A is still pending.
	wagerB := placeAndAcceptSelection(t, ctx, store, marketID, users[1], north, 100_000, "invalidate-b")
	_ = wagerB
	changed, err := store.RepriceMarketAfterWager(ctx, marketID, wagerAID)
	if err != nil {
		t.Fatalf("RepriceMarketAfterWager() error = %v", err)
	}
	if !changed {
		t.Fatal("line did not move; cannot exercise the stale-wager path")
	}

	// Accepting A's now-stale wager must be refused and the wager invalidated.
	if _, err := store.AcceptWager(ctx, string(wagerA.ID), admin); !errors.Is(err, betting.ErrOddsMoved) {
		t.Fatalf("AcceptWager(stale A) error = %v, want ErrOddsMoved", err)
	}
	assertWagerState(t, ctx, pool, string(wagerA.ID), "rejected")

	// No escrow moved for the rejected wager: A keeps their full balance.
	balanceA := accountBalanceFor(t, ctx, pool, users[0], "user_cash", ledger.CAD)
	if balanceA != 200_000 {
		t.Fatalf("A balance after invalidation = %d, want 200000 (untouched)", balanceA)
	}
}

// placeAndAcceptSelection places and accepts a wager on a specific market and
// selection (buildPricedMarket markets are not the buildFixture shape).
func placeAndAcceptSelection(t *testing.T, ctx context.Context, store Store, marketID, userID, selectionID string, stakeCents int64, tag string) betting.Wager {
	t.Helper()
	wagerID := mustNewUUID(t, ctx, store)
	wager, err := store.PlaceWager(ctx, PlaceWagerRequest{
		WagerID: wagerID, UserID: userID, MarketID: marketID, SelectionID: selectionID,
		FundingAccountType: betting.FundingUserCash, StakeCents: stakeCents, Currency: ledger.CAD,
		IdempotencyKey: "priced-place:" + tag + ":" + wagerID,
	})
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	accepted, err := store.AcceptWager(ctx, string(wager.ID), userID)
	if err != nil {
		t.Fatalf("AcceptWager() error = %v", err)
	}
	return accepted
}
