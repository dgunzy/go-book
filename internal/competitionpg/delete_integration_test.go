package competitionpg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/competition"
	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompetitionSetupDeletionLifecycle(t *testing.T) {
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
	actor := scanText(t, ctx, pool, `
		INSERT INTO users (display_name, email)
		VALUES ('Setup Delete Admin', $1)
		RETURNING id::text`, fmt.Sprintf("setup-delete-%d@example.test", suffix))
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, 'admin')`, actor); err != nil {
		t.Fatal(err)
	}

	eventID, err := store.CreateEvent(ctx, CreateEventRequest{
		Name: fmt.Sprintf("Deletion Test %d", suffix), Venue: "Test Links", SeasonYear: 2199, CreatedBy: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	team1, err := store.CreateTeam(ctx, eventID, fmt.Sprintf("North %d", suffix), actor)
	if err != nil {
		t.Fatal(err)
	}
	team2, err := store.CreateTeam(ctx, eventID, fmt.Sprintf("South %d", suffix), actor)
	if err != nil {
		t.Fatal(err)
	}
	player1, err := store.CreatePlayer(ctx, fmt.Sprintf("Alex %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	player2, err := store.CreatePlayer(ctx, fmt.Sprintf("Bill %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE players SET active = false WHERE id = $1::uuid`, player2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "singles", Side1TeamID: team1, Side2TeamID: team2,
		Side1PlayerIDs: []string{player1}, Side2PlayerIDs: []string{player2}, CreatedBy: actor,
	}); !errors.Is(err, competition.ErrInvalid) {
		t.Fatalf("CreateMatch(inactive player) error = %v, want ErrInvalid", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE players SET active = true WHERE id = $1::uuid`, player2); err != nil {
		t.Fatal(err)
	}
	for _, member := range []SetTeamMemberRequest{
		{EventID: eventID, TeamID: team1, PlayerID: player1, IsCaptain: true, ActorUserID: actor},
		{EventID: eventID, TeamID: team2, PlayerID: player2, IsCaptain: true, ActorUserID: actor},
	} {
		if err := store.SetTeamMember(ctx, member); err != nil {
			t.Fatalf("SetTeamMember() error = %v", err)
		}
	}
	match, err := store.CreateMatch(ctx, CreateMatchRequest{
		EventID: eventID, Format: "singles", Side1TeamID: team1, Side2TeamID: team2,
		Side1PlayerIDs: []string{player1}, Side2PlayerIDs: []string{player2}, CreatedBy: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM matches WHERE id = $1::uuid`, match.MatchID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM teams WHERE id IN ($1::uuid, $2::uuid)`, team1, team2)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM events WHERE id = $1::uuid`, eventID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id IN ($1::uuid, $2::uuid)`, player1, player2)
	})

	eventsList, err := store.ListEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, ok := findMatchRow(eventsList, match.MatchID)
	if !ok || listed.Side1Players != fmt.Sprintf("Alex %d", suffix) || listed.Side2Players != fmt.Sprintf("Bill %d", suffix) {
		t.Fatalf("listed match = %+v, found=%v", listed, ok)
	}
	if _, err := pool.Exec(ctx, `UPDATE memberships SET role = 'member' WHERE user_id = $1::uuid`, actor); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMatch(ctx, match.MatchID, actor, "member cannot delete"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("DeleteMatch(member) error = %v, want ErrUnauthorized", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE memberships SET role = 'admin' WHERE user_id = $1::uuid`, actor); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteTeam(ctx, eventID, team1, actor, "team is still used"); !errors.Is(err, ErrDeleteProtected) {
		t.Fatalf("DeleteTeam(used) error = %v, want ErrDeleteProtected", err)
	}
	if err := store.DeleteEvent(ctx, eventID, actor, "event is not empty"); !errors.Is(err, ErrDeleteProtected) {
		t.Fatalf("DeleteEvent(non-empty) error = %v, want ErrDeleteProtected", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE matches SET state = 'pending_verification' WHERE id = $1::uuid`, match.MatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMatch(ctx, match.MatchID, actor, "pending result"); !errors.Is(err, ErrDeleteProtected) {
		t.Fatalf("DeleteMatch(pending) error = %v, want ErrDeleteProtected", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE matches SET state = 'open' WHERE id = $1::uuid`, match.MatchID); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteMatch(ctx, match.MatchID, actor, "duplicate setup match"); err != nil {
		t.Fatalf("DeleteMatch() error = %v", err)
	}
	if err := store.DeleteMatch(ctx, match.MatchID, actor, "repeated request"); !errors.Is(err, ErrDeleteNotFound) {
		t.Fatalf("repeated DeleteMatch() error = %v, want ErrDeleteNotFound", err)
	}
	if err := store.RemoveTeamMember(ctx, eventID, team1, player1, actor, "remove unused setup roster"); err != nil {
		t.Fatalf("RemoveTeamMember(team1) error = %v", err)
	}
	if err := store.RemoveTeamMember(ctx, eventID, team2, player2, actor, "remove unused setup roster"); err != nil {
		t.Fatalf("RemoveTeamMember(team2) error = %v", err)
	}
	if err := store.DeleteTeam(ctx, eventID, team1, actor, "duplicate setup team"); err != nil {
		t.Fatalf("DeleteTeam(team1) error = %v", err)
	}
	if err := store.DeleteTeam(ctx, eventID, team2, actor, "duplicate setup team"); err != nil {
		t.Fatalf("DeleteTeam(team2) error = %v", err)
	}
	if err := store.DeleteEvent(ctx, eventID, actor, "duplicate setup event"); err != nil {
		t.Fatalf("DeleteEvent() error = %v", err)
	}

	var auditCount, outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_entries
		WHERE target_id = ANY($1::uuid[])
		  AND action IN ('competition.match_deleted', 'competition.team_deleted', 'competition.event_deleted')`,
		[]string{match.MatchID, team1, team2, eventID}).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 4 {
		t.Fatalf("deletion audit rows = %d, want 4", auditCount)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM outbox_events
		WHERE aggregate_id = ANY($1::uuid[]) AND event_type = 'CompetitionSetupDeleted.v1'`,
		[]string{match.MatchID, team1, team2, eventID}).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 4 {
		t.Fatalf("deletion outbox rows = %d, want 4", outboxCount)
	}
}

func findMatchRow(eventsList []EventRow, matchID string) (MatchRow, bool) {
	for _, event := range eventsList {
		for _, match := range event.Matches {
			if match.ID == matchID {
				return match, true
			}
		}
	}
	return MatchRow{}, false
}
