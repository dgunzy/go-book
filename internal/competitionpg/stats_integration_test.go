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
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, 'admin')`, admin); err != nil {
		t.Fatal(err)
	}
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
	teammateA, _ := store.CreatePlayer(ctx, fmt.Sprintf("Teammate A %d", suffix), "")
	teammateB, _ := store.CreatePlayer(ctx, fmt.Sprintf("Teammate B %d", suffix), "")
	for _, member := range []SetTeamMemberRequest{
		{EventID: eventID, TeamID: teamA, PlayerID: winner, IsCaptain: true, ActorUserID: admin},
		{EventID: eventID, TeamID: teamA, PlayerID: teammateA, ActorUserID: admin},
		{EventID: eventID, TeamID: teamB, PlayerID: loser, IsCaptain: true, ActorUserID: admin},
		{EventID: eventID, TeamID: teamB, PlayerID: teammateB, ActorUserID: admin},
	} {
		if err := store.SetTeamMember(ctx, member); err != nil {
			t.Fatalf("SetTeamMember() error = %v", err)
		}
	}

	match, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "singles", Side1TeamID: teamA, Side2TeamID: teamB,
		Side1PlayerIDs: []string{winner}, Side2PlayerIDs: []string{loser}, CreatedBy: admin,
	})
	if err != nil {
		t.Fatalf("CreateMatch() error = %v", err)
	}
	teamMatch, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "fourball", Side1TeamID: teamA, Side2TeamID: teamB,
		Side1PlayerIDs: []string{winner, teammateA}, Side2PlayerIDs: []string{loser, teammateB}, CreatedBy: admin,
	})
	if err != nil {
		t.Fatalf("CreateMatch(fourball) error = %v", err)
	}
	matchIDs := []string{match.MatchID, teamMatch.MatchID}
	playerIDs := []string{winner, loser, teammateA, teammateB}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id = ANY($1::uuid[])`, matchIDs)
		_, _ = pool.Exec(c, `DELETE FROM match_stat_applications WHERE match_id = ANY($1::uuid[])`, matchIDs)
		_, _ = pool.Exec(c, `DELETE FROM player_stat_projections WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM match_participants WHERE match_id = ANY($1::uuid[])`, matchIDs)
		_, _ = pool.Exec(c, `DELETE FROM verified_results WHERE match_id = ANY($1::uuid[])`, matchIDs)
		_, _ = pool.Exec(c, `DELETE FROM match_sides WHERE match_id = ANY($1::uuid[])`, matchIDs)
		_, _ = pool.Exec(c, `DELETE FROM matches WHERE id = ANY($1::uuid[])`, matchIDs)
		_, _ = pool.Exec(c, `DELETE FROM teams WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1::uuid`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM players WHERE id = ANY($1::uuid[])`, playerIDs)
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
	assertFormatStat(t, ctx, pool, winner, eventID, 1, 1, 0, 0, 0, 0)

	// Redelivery must not double-count.
	if err := consumer.Handle(ctx, envelope); err != nil {
		t.Fatalf("redelivery Handle() error = %v", err)
	}
	assertStat(t, ctx, pool, winner, eventID, 1, 1, 0, 0)

	if _, err := store.RecordAdminResult(ctx, RecordResultRequest{
		MatchID: teamMatch.MatchID, Winner: "side_2", Score: "1 up", ActorUserID: admin, Reason: "team card",
	}); err != nil {
		t.Fatalf("RecordAdminResult(fourball) error = %v", err)
	}
	if err := consumer.Handle(ctx, loadEnvelope(t, ctx, pool, teamMatch.MatchID)); err != nil {
		t.Fatalf("team stats consumer Handle() error = %v", err)
	}
	assertStat(t, ctx, pool, winner, eventID, 2, 1, 1, 0)
	assertStat(t, ctx, pool, loser, eventID, 2, 1, 1, 0)
	assertStat(t, ctx, pool, teammateA, eventID, 1, 0, 1, 0)
	assertStat(t, ctx, pool, teammateB, eventID, 1, 1, 0, 0)
	assertFormatStat(t, ctx, pool, winner, eventID, 1, 1, 1, 0, 0, 1)

	public, err := store.PublicCompetition(ctx)
	if err != nil {
		t.Fatalf("PublicCompetition() error = %v", err)
	}
	var publicSeason *PublicSeasonRow
	for i := range public.Seasons {
		if public.Seasons[i].EventID == eventID {
			publicSeason = &public.Seasons[i]
			break
		}
	}
	if publicSeason == nil || len(publicSeason.Matches) != 2 || publicSeason.Matches[0].Score != "2 up" || publicSeason.Matches[0].WinnerLabel() != fmt.Sprintf("Winner %d", suffix) {
		t.Fatalf("public season = %+v", publicSeason)
	}
	if len(publicSeason.Players) != 4 || len(publicSeason.Teams) != 2 {
		t.Fatalf("public season projections = players %d teams %d", len(publicSeason.Players), len(publicSeason.Teams))
	}
}

func assertFormatStat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, playerID, eventID string, singlesPlayed, singlesWins, teamPlayed, teamWins int, singlesLosses, teamLosses int) {
	t.Helper()
	var got [6]int
	if err := pool.QueryRow(ctx, `SELECT singles_played, singles_wins, team_played, team_wins, singles_losses, team_losses FROM player_stat_projections WHERE player_id = $1::uuid AND event_id = $2::uuid`,
		playerID, eventID).Scan(&got[0], &got[1], &got[2], &got[3], &got[4], &got[5]); err != nil {
		t.Fatalf("load format projection: %v", err)
	}
	want := [6]int{singlesPlayed, singlesWins, teamPlayed, teamWins, singlesLosses, teamLosses}
	if got != want {
		t.Fatalf("format stats = %v, want %v", got, want)
	}
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
