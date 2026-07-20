package bettingpg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// makeUser inserts a bare user and returns its ID. Used as created_by for
// markets in the create/open/read tests. The email is lowercased to satisfy
// the users_email_check constraint.
func makeUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) string {
	t.Helper()
	suffix := uniqueSuffix(t, ctx, pool)
	email := strings.ToLower(strings.ReplaceAll(label, " ", "-") + "-" + suffix + "@example.test")
	return mustScanID(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ($1, $2) RETURNING id::text`,
		label+" "+suffix, email)
}

func futureCreateRequest(actor, marketID string) CreateMarketRequest {
	return CreateMarketRequest{
		MarketID: marketID,
		Type:     betting.MarketFuture,
		Title:    "Tournament winner",
		Currency: ledger.CAD,
		// Truncated to microseconds so the value round-trips through Postgres
		// timestamptz unchanged; the idempotency verify compares ClosesAt
		// exactly, and real callers supply minute-precision form times.
		ClosesAt: time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond),
		Selections: []CreateMarketSelection{
			{Key: "team-a", DisplayTerms: "Team A wins the cup", OfferedAmericanOdds: -110},
			{Key: "team-b", DisplayTerms: "Team B wins the cup", OfferedAmericanOdds: 150},
		},
		ActorUserID: actor,
	}
}

func TestCreateMarketPersistsSelectionsAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	admin := makeUser(t, ctx, pool, "Create Admin")
	marketID := mustNewUUID(t, ctx, store)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, marketID)
	})

	// One request value, reused verbatim, so the idempotent re-run below is
	// genuinely identical (regenerating it would give a fresh ClosesAt).
	request := futureCreateRequest(admin, marketID)
	created, err := store.CreateMarket(ctx, request)
	if err != nil {
		t.Fatalf("CreateMarket() error = %v", err)
	}
	if created.State != betting.MarketDraft {
		t.Fatalf("created market state = %v, want draft", created.State)
	}

	var selectionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selections WHERE market_id = $1::uuid AND active`, marketID).Scan(&selectionCount); err != nil {
		t.Fatal(err)
	}
	if selectionCount != 2 {
		t.Fatalf("selection count = %d, want 2", selectionCount)
	}
	assertMarketState(t, ctx, pool, marketID, "draft")
	assertOutboxContains(t, ctx, pool, marketID, events.MarketCreated)

	// Re-running with the same MarketID and identical terms is a no-op that
	// returns the stored market without inserting duplicate selections.
	again, err := store.CreateMarket(ctx, request)
	if err != nil {
		t.Fatalf("idempotent CreateMarket() error = %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("idempotent create returned id %s, want %s", again.ID, created.ID)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selections WHERE market_id = $1::uuid`, marketID).Scan(&selectionCount); err != nil {
		t.Fatal(err)
	}
	if selectionCount != 2 {
		t.Fatalf("selection count after idempotent create = %d, want 2 (no duplicates)", selectionCount)
	}

	// The same MarketID describing different terms is a conflict, not a
	// silent overwrite.
	conflicting := futureCreateRequest(admin, marketID)
	conflicting.Title = "A completely different market"
	if _, err := store.CreateMarket(ctx, conflicting); err == nil {
		t.Fatal("CreateMarket() with reused id and different terms = nil error, want conflict")
	}
}

func TestCreateMarketRejectsUnauthorizedActor(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := Store{DB: pool}

	marketID := mustNewUUID(t, ctx, store)
	if _, err := store.CreateMarket(ctx, futureCreateRequest("not-a-uuid", marketID)); err == nil {
		t.Fatal("CreateMarket() with non-UUID actor = nil error, want unauthorized")
	}
}

func TestOpenMarketTransitionsAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	admin := makeUser(t, ctx, pool, "Open Admin")
	marketID := mustNewUUID(t, ctx, store)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, marketID)
	})
	if _, err := store.CreateMarket(ctx, futureCreateRequest(admin, marketID)); err != nil {
		t.Fatalf("CreateMarket() error = %v", err)
	}

	if err := store.OpenMarket(ctx, marketID, admin); err != nil {
		t.Fatalf("OpenMarket() error = %v", err)
	}
	assertMarketState(t, ctx, pool, marketID, "open")
	assertOutboxContains(t, ctx, pool, marketID, events.MarketOpened)

	// Opening an already-open market is a no-op, not an error.
	if err := store.OpenMarket(ctx, marketID, admin); err != nil {
		t.Fatalf("repeat OpenMarket() error = %v", err)
	}
	assertMarketState(t, ctx, pool, marketID, "open")

	// Closing then trying to re-open must be refused.
	if err := store.CloseMarket(ctx, marketID, admin); err != nil {
		t.Fatalf("CloseMarket() error = %v", err)
	}
	if err := store.OpenMarket(ctx, marketID, admin); err == nil {
		t.Fatal("OpenMarket() on closed market = nil error, want ErrMarketNotOpenable")
	}
}

func TestListOpenMarketsFiltersDraftAndClosed(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	admin := makeUser(t, ctx, pool, "List Admin")
	openID := mustNewUUID(t, ctx, store)
	draftID := mustNewUUID(t, ctx, store)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		for _, id := range []string{openID, draftID} {
			_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, id)
			_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, id)
			_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, id)
		}
	})
	if _, err := store.CreateMarket(ctx, futureCreateRequest(admin, openID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMarket(ctx, futureCreateRequest(admin, draftID)); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenMarket(ctx, openID, admin); err != nil {
		t.Fatal(err)
	}

	open, err := store.ListOpenMarkets(ctx)
	if err != nil {
		t.Fatalf("ListOpenMarkets() error = %v", err)
	}
	if containsMarket(open, draftID) {
		t.Fatal("ListOpenMarkets() included a draft market")
	}
	target, ok := findMarketRow(open, openID)
	if !ok {
		t.Fatal("ListOpenMarkets() omitted the open market")
	}
	if len(target.Selections) != 2 {
		t.Fatalf("open market selections = %d, want 2", len(target.Selections))
	}

	all, err := store.ListMarkets(ctx)
	if err != nil {
		t.Fatalf("ListMarkets() error = %v", err)
	}
	if !containsMarket(all, draftID) || !containsMarket(all, openID) {
		t.Fatal("ListMarkets() must include both draft and open markets")
	}
}

func TestListWagersScopingByUserAndState(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 10_000)
	pendingWager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 1_000, 1)
	_ = pendingWager

	// UserA's list contains only UserA's wager; UserB has none here.
	userAWagers, err := store.ListWagersForUser(ctx, f.UserA)
	if err != nil {
		t.Fatalf("ListWagersForUser(A) error = %v", err)
	}
	if len(userAWagers) != 1 {
		t.Fatalf("UserA wagers = %d, want 1", len(userAWagers))
	}
	userBWagers, err := store.ListWagersForUser(ctx, f.UserB)
	if err != nil {
		t.Fatalf("ListWagersForUser(B) error = %v", err)
	}
	for _, w := range userBWagers {
		if w.ID == string(pendingWager.ID) {
			t.Fatal("UserB's wager list leaked UserA's wager")
		}
	}

	// placeAndAccept leaves the wager accepted, so it appears under accepted,
	// carrying the wagering user's identity for the admin queue.
	accepted, err := store.ListWagersByState(ctx, betting.WagerAccepted)
	if err != nil {
		t.Fatalf("ListWagersByState(accepted) error = %v", err)
	}
	found := false
	for _, row := range accepted {
		if row.ID == string(pendingWager.ID) {
			found = true
			if row.UserID != f.UserA {
				t.Fatalf("admin wager row user = %s, want %s", row.UserID, f.UserA)
			}
			if row.UserDisplayName == "" {
				t.Fatal("admin wager row missing user display name")
			}
		}
	}
	if !found {
		t.Fatal("ListWagersByState(accepted) omitted the accepted wager")
	}
}

func containsMarket(markets []MarketRow, id string) bool {
	_, ok := findMarketRow(markets, id)
	return ok
}

func findMarketRow(markets []MarketRow, id string) (MarketRow, bool) {
	for _, market := range markets {
		if market.ID == id {
			return market, true
		}
	}
	return MarketRow{}, false
}
