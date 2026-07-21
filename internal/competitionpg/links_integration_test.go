package competitionpg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPlayerLinkLifecycle(t *testing.T) {
	databaseURL := os.Getenv("IDENTITYPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("IDENTITYPG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := Store{Pool: pool}
	suffix := time.Now().UTC().UnixNano()

	admin := insertLinkTestMember(t, ctx, pool, suffix, "Link Admin", "admin")
	member := insertLinkTestMember(t, ctx, pool, suffix+1, "Linked Member", "member")
	otherMember := insertLinkTestMember(t, ctx, pool, suffix+2, "Other Member", "member")
	plainUser := scanText(t, ctx, pool, `
		INSERT INTO users (display_name, email)
		VALUES ('No Membership', $1)
		RETURNING id::text`, fmt.Sprintf("link-no-membership-%d@example.test", suffix))
	player, err := store.CreatePlayer(ctx, fmt.Sprintf("Historical Golfer %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	otherPlayer, err := store.CreatePlayer(ctx, fmt.Sprintf("Other Golfer %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM outbox_events WHERE aggregate_type = 'player' AND aggregate_id IN ($1::uuid, $2::uuid)`, player, otherPlayer)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id IN ($1::uuid, $2::uuid)`, player, otherPlayer)
	})

	if err := store.LinkPlayerToUser(ctx, member, player, otherMember); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("member actor error = %v, want ErrUnauthorized", err)
	}
	if err := store.LinkPlayerToUser(ctx, admin, player, plainUser); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("non-member target error = %v, want ErrMemberNotFound", err)
	}
	if err := store.LinkPlayerToUser(ctx, admin, player, member); err != nil {
		t.Fatalf("LinkPlayerToUser() error = %v", err)
	}
	// A repeated request is idempotent and must not create another audit row.
	if err := store.LinkPlayerToUser(ctx, admin, player, member); err != nil {
		t.Fatalf("repeated LinkPlayerToUser() error = %v", err)
	}
	if err := store.LinkPlayerToUser(ctx, admin, otherPlayer, member); !errors.Is(err, ErrMemberAlreadyLinked) {
		t.Fatalf("second player error = %v, want ErrMemberAlreadyLinked", err)
	}
	if err := store.LinkPlayerToUser(ctx, admin, player, otherMember); !errors.Is(err, ErrPlayerAlreadyLinked) {
		t.Fatalf("second member error = %v, want ErrPlayerAlreadyLinked", err)
	}

	if _, err := store.ListPlayerLinks(ctx, member); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("member list error = %v, want ErrUnauthorized", err)
	}
	links, err := store.ListPlayerLinks(ctx, admin)
	if err != nil {
		t.Fatalf("ListPlayerLinks() error = %v", err)
	}
	if !hasPlayerLink(links, player, member) {
		t.Fatalf("linked player %s -> member %s missing from %+v", player, member, links)
	}

	if err := store.UnlinkPlayer(ctx, admin, player, otherMember); !errors.Is(err, ErrPlayerLinkMismatch) {
		t.Fatalf("mismatched unlink error = %v, want ErrPlayerLinkMismatch", err)
	}
	if err := store.UnlinkPlayer(ctx, admin, player, member); err != nil {
		t.Fatalf("UnlinkPlayer() error = %v", err)
	}
	if err := store.UnlinkPlayer(ctx, admin, player, member); err != nil {
		t.Fatalf("repeated UnlinkPlayer() error = %v", err)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM audit_entries
		WHERE target_id = $1::uuid
		  AND action IN ('player.linked_to_user', 'player.unlinked_from_user')`, player).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("mapping audit rows = %d, want 2", auditCount)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM outbox_events
		WHERE aggregate_type = 'player' AND aggregate_id = $1::uuid
		  AND event_type IN ('PlayerLinkedToUser.v1', 'PlayerUnlinkedFromUser.v1')`, player).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 {
		t.Fatalf("mapping outbox events = %d, want 2", eventCount)
	}
}

func insertLinkTestMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix int64, name, role string) string {
	t.Helper()
	userID := scanText(t, ctx, pool, `
		INSERT INTO users (display_name, email)
		VALUES ($1, $2)
		RETURNING id::text`, name, fmt.Sprintf("player-link-%d@example.test", suffix))
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, $2)`, userID, role); err != nil {
		t.Fatal(err)
	}
	return userID
}

func hasPlayerLink(links []PlayerLink, playerID, userID string) bool {
	for _, link := range links {
		if link.PlayerID == playerID && link.LinkedUserID == userID {
			return true
		}
	}
	return false
}
