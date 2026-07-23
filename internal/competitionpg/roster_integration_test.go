package competitionpg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/competition"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTeamRosterLifecycleAndMatchEnforcement(t *testing.T) {
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

	actor := scanText(t, ctx, pool, `INSERT INTO users (display_name, email) VALUES ('Roster Admin', $1) RETURNING id::text`, fmt.Sprintf("roster-%d@example.test", suffix))
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, 'admin')`, actor); err != nil {
		t.Fatal(err)
	}
	eventID, err := store.CreateEvent(ctx, CreateEventRequest{Name: fmt.Sprintf("Roster Cup %d", suffix), Venue: "Test Links", SeasonYear: 2198, CreatedBy: actor})
	if err != nil {
		t.Fatal(err)
	}
	teamA, _ := store.CreateTeam(ctx, eventID, fmt.Sprintf("A %d", suffix), actor)
	teamB, _ := store.CreateTeam(ctx, eventID, fmt.Sprintf("B %d", suffix), actor)
	playerA, _ := store.CreatePlayer(ctx, fmt.Sprintf("Player A %d", suffix), "")
	playerReserve, _ := store.CreatePlayer(ctx, fmt.Sprintf("Reserve %d", suffix), "")
	playerB, _ := store.CreatePlayer(ctx, fmt.Sprintf("Player B %d", suffix), "")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM outbox_events WHERE aggregate_id = ANY($1::uuid[])`, []string{teamA, teamB})
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM match_participants WHERE match_id IN (SELECT id FROM matches WHERE event_id = $1::uuid)`, eventID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM match_sides WHERE match_id IN (SELECT id FROM matches WHERE event_id = $1::uuid)`, eventID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM matches WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM teams WHERE event_id = $1::uuid`, eventID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM events WHERE id = $1::uuid`, eventID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id = ANY($1::uuid[])`, []string{playerA, playerReserve, playerB})
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1::uuid`, actor)
	})

	// The first member is made captain even when the caller omits the flag.
	if err := store.SetTeamMember(ctx, SetTeamMemberRequest{EventID: eventID, TeamID: teamA, PlayerID: playerA, ActorUserID: actor}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTeamMember(ctx, SetTeamMemberRequest{EventID: eventID, TeamID: teamA, PlayerID: playerReserve, ActorUserID: actor}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTeamMember(ctx, SetTeamMemberRequest{EventID: eventID, TeamID: teamB, PlayerID: playerB, ActorUserID: actor}); err != nil {
		t.Fatal(err)
	}
	var firstCaptain bool
	if err := pool.QueryRow(ctx, `SELECT is_captain FROM event_team_memberships WHERE event_id = $1::uuid AND player_id = $2::uuid`, eventID, playerA).Scan(&firstCaptain); err != nil || !firstCaptain {
		t.Fatalf("first member captain=%v error=%v", firstCaptain, err)
	}
	if err := store.SetTeamMember(ctx, SetTeamMemberRequest{EventID: eventID, TeamID: teamA, PlayerID: playerA, IsCaptain: false, ActorUserID: actor}); !errors.Is(err, ErrLastCaptain) {
		t.Fatalf("demote final captain error=%v, want ErrLastCaptain", err)
	}
	if err := store.SetTeamMember(ctx, SetTeamMemberRequest{EventID: eventID, TeamID: teamB, PlayerID: playerB, IsCaptain: false, ActorUserID: actor}); !errors.Is(err, ErrLastCaptain) {
		t.Fatalf("demote sole member captain error=%v, want ErrLastCaptain", err)
	}
	if err := store.SetTeamMember(ctx, SetTeamMemberRequest{EventID: eventID, TeamID: teamB, PlayerID: playerReserve, ActorUserID: actor}); !errors.Is(err, competition.ErrInvalid) {
		t.Fatalf("cross-team assignment error=%v, want ErrInvalid", err)
	}

	unrostered, _ := store.CreatePlayer(ctx, fmt.Sprintf("Unrostered %d", suffix), "")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id = $1::uuid`, unrostered)
	})
	if _, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "singles", Side1TeamID: teamA, Side2TeamID: teamB,
		Side1PlayerIDs: []string{unrostered}, Side2PlayerIDs: []string{playerB}, CreatedBy: actor,
	}); !errors.Is(err, competition.ErrInvalid) {
		t.Fatalf("unrostered match error=%v, want ErrInvalid", err)
	}
	match, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "singles", Side1TeamID: teamA, Side2TeamID: teamB,
		Side1PlayerIDs: []string{playerA}, Side2PlayerIDs: []string{playerB}, CreatedBy: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTeamMember(ctx, eventID, teamA, playerA, actor, "attempt to erase history"); !errors.Is(err, ErrRosterProtected) {
		t.Fatalf("remove participant error=%v, want ErrRosterProtected", err)
	}

	eventsList, err := store.ListEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundRoster := false
	for _, event := range eventsList {
		if event.ID == eventID && len(event.Teams) == 2 && event.Teams[0].HasCaptain() && event.Teams[1].HasCaptain() {
			foundRoster = true
		}
	}
	if !foundRoster {
		t.Fatal("ListEvents did not return both staffed team rosters")
	}

	var auditCount, outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE action LIKE 'competition.roster_%' AND target_id = ANY($1::uuid[])`, []string{teamA, teamB}).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type = 'TeamRosterChanged.v1' AND aggregate_id = ANY($1::uuid[])`, []string{teamA, teamB}).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 || outboxCount != 3 {
		t.Fatalf("roster audit/outbox=%d/%d, want 3/3", auditCount, outboxCount)
	}
	_ = match
}
