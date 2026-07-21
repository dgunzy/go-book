package competitionpg

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStatsProjectionFromVerifiedResult proves statistics are event-driven off
// verified match results: recording a result publishes MatchResultVerified,
// and the stats consumer projects each participant's win/loss into
// player_stat_projections — idempotently.
func TestStatsProjectionFromVerifiedResult(t *testing.T) {
	databaseURL := os.Getenv("IDENTITYPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("BETTINGPG_TEST_DATABASE_URL")
	}
	if databaseURL == "" {
		t.Skip("no test database URL set")
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

	admin := scanText(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ('Stats Admin', $1) RETURNING id::text`,
		fmt.Sprintf("stats-admin-%d@example.test", suffix))
	eventID, err := store.CreateEvent(ctx, CreateEventRequest{Name: fmt.Sprintf("Stats Cup %d", suffix), Venue: "Links", SeasonYear: 2026, CreatedBy: admin})
	if err != nil {
		t.Fatal(err)
	}
	teamA, _ := store.CreateTeam(ctx, eventID, fmt.Sprintf("North %d", suffix), admin)
	teamB, _ := store.CreateTeam(ctx, eventID, fmt.Sprintf("South %d", suffix), admin)
	winner, err := store.CreatePlayer(ctx, fmt.Sprintf("Winner %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	loser, _ := store.CreatePlayer(ctx, fmt.Sprintf("Loser %d", suffix), "")

	match, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "singles", Side1TeamID: teamA, Side2TeamID: teamB,
		Side1PlayerIDs: []string{winner}, Side2PlayerIDs: []string{loser}, CreatedBy: admin,
	})
	if err != nil {
		t.Fatalf("CreateMatch() error = %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM match_stat_applications WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM player_stat_projections WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM match_participants WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM verified_results WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM match_sides WHERE match_id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM matches WHERE id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(c, `DELETE FROM players WHERE id IN ($1::uuid, $2::uuid)`, winner, loser)
		_, _ = pool.Exec(c, `DELETE FROM teams WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1::uuid`, eventID)
	})

	if _, err := store.RecordAdminResult(ctx, RecordResultRequest{
		MatchID: match.MatchID, Winner: "side_1", Score: "2 up", ActorUserID: admin, Reason: "card",
	}); err != nil {
		t.Fatalf("RecordAdminResult() error = %v", err)
	}

	envelope := loadEnvelope(t, ctx, pool, match.MatchID)
	consumer := &StatsProjectionConsumer{Pool: pool}
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("stats consumer Handle() error = %v", err)
	}

	assertStat(t, ctx, pool, winner, eventID, 1, 1, 0, 0)
	assertStat(t, ctx, pool, loser, eventID, 1, 0, 1, 0)

	// Redelivery must not double-count.
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("redelivery Handle() error = %v", err)
	}
	assertStat(t, ctx, pool, winner, eventID, 1, 1, 0, 0)
}

func assertStat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, playerID, eventID string, played, wins, losses, ties int) {
	t.Helper()
	var gp, gw, gl, gt int
	if err := pool.QueryRow(ctx, `SELECT matches_played, wins, losses, ties FROM player_stat_projections WHERE player_id = $1::uuid AND event_id = $2::uuid`,
		playerID, eventID).Scan(&gp, &gw, &gl, &gt); err != nil {
		t.Fatalf("load projection: %v", err)
	}
	if gp != played || gw != wins || gl != losses || gt != ties {
		t.Fatalf("stats = played %d w %d l %d t %d, want %d/%d/%d/%d", gp, gw, gl, gt, played, wins, losses, ties)
	}
}

func scanText(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func loadEnvelope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, matchID string) events.Envelope {
	t.Helper()
	var env events.Envelope
	var payload []byte
	if err := pool.QueryRow(ctx, `
		SELECT id::text, aggregate_type, aggregate_id::text, aggregate_version, event_type, payload, occurred_at
		FROM outbox_events WHERE aggregate_id = $1::uuid AND event_type = $2 ORDER BY occurred_at DESC LIMIT 1`,
		matchID, string(events.MatchResultVerified)).Scan(
		&env.ID, &env.AggregateType, &env.AggregateID, &env.AggregateVersion, &env.Type, &payload, &env.OccurredAt); err != nil {
		t.Fatalf("load envelope: %v", err)
	}
	env.Payload = payload
	return env
}
