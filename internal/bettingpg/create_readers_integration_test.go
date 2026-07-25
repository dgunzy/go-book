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

// A board should read like a price board: shortest price at the top, longest
// shot at the bottom. Selections used to come back ordered by their UUID, which
// on a sixteen-name prop such as "Leading points getter" is no order at all —
// the favourite could sit anywhere in the list.
func TestMarketSelectionsComeBackShortestPriceFirst(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	admin := makeUser(t, ctx, pool, "Order Admin")
	marketID := mustNewUUID(t, ctx, store)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, marketID)
		_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, marketID)
	})

	// Deliberately created out of order, and spanning the negative/positive
	// boundary where "shorter" is easy to get backwards: -250 is a shorter
	// price than -110, which is shorter than +100, which is shorter than +3000.
	request := futureCreateRequest(admin, marketID)
	request.Title = "Leading points getter"
	request.Selections = []CreateMarketSelection{
		{Key: "rushton", DisplayTerms: "Rushton", OfferedAmericanOdds: 3000},
		{Key: "clarke", DisplayTerms: "Clarke", OfferedAmericanOdds: 600},
		{Key: "wright", DisplayTerms: "Wright", OfferedAmericanOdds: -110},
		{Key: "guns", DisplayTerms: "Guns", OfferedAmericanOdds: 2500},
		{Key: "marshall", DisplayTerms: "Marshall", OfferedAmericanOdds: -250},
		{Key: "kiley", DisplayTerms: "Kiley", OfferedAmericanOdds: 100},
	}
	if _, err := store.CreateMarket(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenMarket(ctx, marketID, admin); err != nil {
		t.Fatal(err)
	}

	board, err := store.ListOpenMarkets(ctx)
	if err != nil {
		t.Fatalf("ListOpenMarkets() error = %v", err)
	}
	market, ok := findMarketRow(board, marketID)
	if !ok {
		t.Fatal("the open market is missing from the board")
	}

	want := []ledger.AmericanOdds{-250, -110, 100, 600, 2500, 3000}
	got := make([]ledger.AmericanOdds, 0, len(market.Selections))
	for _, selection := range market.Selections {
		got = append(got, selection.OfferedAmericanOdds)
	}
	if len(got) != len(want) {
		t.Fatalf("selections = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selections are not shortest price first:\n got %v\nwant %v", got, want)
		}
	}
}

// A whole board posted for one event closes at the same moment, so closing time
// alone decides nothing and the order falls to the tiebreak. That used to be the
// market's UUID, which is no order at all; it is now creation order, so the list
// reads the way the markets were put up.
func TestMarketsSharingACloseTimeAreOrderedByCreation(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	admin := makeUser(t, ctx, pool, "Creation Order Admin")
	firstID := mustNewUUID(t, ctx, store)
	secondID := mustNewUUID(t, ctx, store)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		for _, id := range []string{firstID, secondID} {
			_, _ = pool.Exec(cctx, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, id)
			_, _ = pool.Exec(cctx, `DELETE FROM selections WHERE market_id = $1::uuid`, id)
			_, _ = pool.Exec(cctx, `DELETE FROM markets WHERE id = $1::uuid`, id)
		}
	})

	// Both markets close at the very same instant, as a board posted for one
	// event does, so only creation order can separate them.
	first := futureCreateRequest(admin, firstID)
	first.Title = "Posted first"
	second := futureCreateRequest(admin, secondID)
	second.Title = "Posted second"
	second.ClosesAt = first.ClosesAt

	if _, err := store.CreateMarket(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMarket(ctx, second); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{firstID, secondID} {
		if err := store.OpenMarket(ctx, id, admin); err != nil {
			t.Fatal(err)
		}
	}

	// The member board runs soonest-closing first, so within one closing time
	// the market posted first is the one listed first.
	board, err := store.ListOpenMarkets(ctx)
	if err != nil {
		t.Fatalf("ListOpenMarkets() error = %v", err)
	}
	if got := relativeOrder(board, firstID, secondID); got != firstID {
		t.Errorf("member board lists %s first; the market posted first should lead an ascending list", marketLabel(got, firstID, secondID))
	}

	// The admin list runs newest-closing first, and its tiebreak follows the
	// same direction, so the most recently posted market leads.
	all, err := store.ListMarkets(ctx)
	if err != nil {
		t.Fatalf("ListMarkets() error = %v", err)
	}
	if got := relativeOrder(all, firstID, secondID); got != secondID {
		t.Errorf("admin list leads with %s; a descending list should lead with the most recently posted market", marketLabel(got, firstID, secondID))
	}
}

// relativeOrder reports which of two market IDs appears first in a listing.
func relativeOrder(markets []MarketRow, a, b string) string {
	for _, market := range markets {
		if market.ID == a || market.ID == b {
			return market.ID
		}
	}
	return ""
}

func marketLabel(got, first, second string) string {
	switch got {
	case first:
		return "the market posted first"
	case second:
		return "the market posted second"
	default:
		return "neither market"
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
