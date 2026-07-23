package competitionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/competition"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/jackc/pgx/v5"
)

var (
	ErrRosterNotFound  = errors.New("team roster membership not found")
	ErrRosterProtected = errors.New("team roster membership has protected match history")
	ErrLastCaptain     = errors.New("a staffed team must retain at least one captain")
)

// SetTeamMemberRequest adds a golfer to an event team or changes their
// captain status. The first golfer on a team is always made captain.
type SetTeamMemberRequest struct {
	EventID     string
	TeamID      string
	PlayerID    string
	IsCaptain   bool
	ActorUserID string
}

// SetTeamMember transactionally adds or updates one roster membership and
// records both immutable audit history and a durable outbox event.
func (s Store) SetTeamMember(ctx context.Context, req SetTeamMemberRequest) error {
	if s.Pool == nil {
		return errors.New("competitionpg: pool is required")
	}
	if !uuidTextPattern.MatchString(req.EventID) || !uuidTextPattern.MatchString(req.TeamID) || !uuidTextPattern.MatchString(req.PlayerID) {
		return fmt.Errorf("%w: event, team, and player are required", competition.ErrInvalid)
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin roster update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := requireStaff(ctx, tx, req.ActorUserID); err != nil {
		return err
	}
	version, err := lockRosterTeam(ctx, tx, req.EventID, req.TeamID)
	if err != nil {
		return err
	}
	var playerName string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM players WHERE id = $1::uuid AND active FOR SHARE`, req.PlayerID).Scan(&playerName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: choose an active player", competition.ErrInvalid)
		}
		return fmt.Errorf("load roster player: %w", err)
	}

	var oldCaptain bool
	existed := true
	if err := tx.QueryRow(ctx, `
		SELECT is_captain FROM event_team_memberships
		WHERE event_id = $1::uuid AND team_id = $2::uuid AND player_id = $3::uuid
		FOR UPDATE`, req.EventID, req.TeamID, req.PlayerID).Scan(&oldCaptain); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load roster membership: %w", err)
		}
		existed = false
	}
	var memberCount, captainCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE is_captain)
		FROM event_team_memberships WHERE event_id = $1::uuid AND team_id = $2::uuid`, req.EventID, req.TeamID).Scan(&memberCount, &captainCount); err != nil {
		return fmt.Errorf("count team roster: %w", err)
	}
	newCaptain := req.IsCaptain || memberCount == 0 || captainCount == 0
	if existed && oldCaptain && !newCaptain && captainCount == 1 {
		return ErrLastCaptain
	}
	if existed && oldCaptain == newCaptain {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit unchanged roster update: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_team_memberships (event_id, team_id, player_id, is_captain)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		ON CONFLICT (event_id, player_id) DO UPDATE SET
			team_id = EXCLUDED.team_id,
			is_captain = EXCLUDED.is_captain
		WHERE event_team_memberships.team_id = EXCLUDED.team_id`, req.EventID, req.TeamID, req.PlayerID, newCaptain); err != nil {
		return fmt.Errorf("set team roster membership: %w", err)
	}
	var belongsToTeam bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM event_team_memberships
		WHERE event_id = $1::uuid AND team_id = $2::uuid AND player_id = $3::uuid
	)`, req.EventID, req.TeamID, req.PlayerID).Scan(&belongsToTeam); err != nil {
		return fmt.Errorf("verify team roster membership: %w", err)
	}
	if !belongsToTeam {
		return fmt.Errorf("%w: that player already belongs to another team in this event", competition.ErrInvalid)
	}
	version++
	if _, err := tx.Exec(ctx, `UPDATE teams SET version = $2 WHERE id = $1::uuid`, req.TeamID, version); err != nil {
		return fmt.Errorf("advance team roster version: %w", err)
	}
	action := "added"
	if existed {
		action = "captain_status_changed"
	}
	before := map[string]any{"present": existed, "is_captain": oldCaptain}
	after := map[string]any{"present": true, "is_captain": newCaptain, "player_name": playerName}
	if err := recordRosterAudit(ctx, tx, req.ActorUserID, req.TeamID, req.PlayerID, action, before, after, "team roster updated"); err != nil {
		return err
	}
	if err := publishRosterChanged(ctx, tx, req, version, action, newCaptain); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit roster update: %w", err)
	}
	return nil
}

// RemoveTeamMember removes only a golfer who has no match history for that
// team. A staffed team cannot lose its final captain.
func (s Store) RemoveTeamMember(ctx context.Context, eventID, teamID, playerID, actorUserID, reason string) error {
	if s.Pool == nil {
		return errors.New("competitionpg: pool is required")
	}
	if !uuidTextPattern.MatchString(eventID) || !uuidTextPattern.MatchString(teamID) || !uuidTextPattern.MatchString(playerID) {
		return fmt.Errorf("%w: event, team, and player are required", competition.ErrInvalid)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > maxDeleteReasonLength {
		return errors.New("roster removal requires a reason of at most 500 characters")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin roster removal: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := requireStaff(ctx, tx, actorUserID); err != nil {
		return err
	}
	version, err := lockRosterTeam(ctx, tx, eventID, teamID)
	if err != nil {
		return err
	}
	var playerName string
	var wasCaptain bool
	if err := tx.QueryRow(ctx, `
		SELECT p.display_name, etm.is_captain
		FROM event_team_memberships etm
		JOIN players p ON p.id = etm.player_id
		WHERE etm.event_id = $1::uuid AND etm.team_id = $2::uuid AND etm.player_id = $3::uuid
		FOR UPDATE OF etm`, eventID, teamID, playerID).Scan(&playerName, &wasCaptain); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRosterNotFound
		}
		return fmt.Errorf("load roster membership: %w", err)
	}
	var protected bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM match_participants mp
		JOIN match_sides ms ON ms.id = mp.match_side_id
		WHERE ms.event_id = $1::uuid AND ms.team_id = $2::uuid AND mp.player_id = $3::uuid
	)`, eventID, teamID, playerID).Scan(&protected); err != nil {
		return fmt.Errorf("check roster history: %w", err)
	}
	if protected {
		return ErrRosterProtected
	}
	var otherMembers, otherCaptains int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE player_id <> $3::uuid),
		       count(*) FILTER (WHERE player_id <> $3::uuid AND is_captain)
		FROM event_team_memberships
		WHERE event_id = $1::uuid AND team_id = $2::uuid`, eventID, teamID, playerID).Scan(&otherMembers, &otherCaptains); err != nil {
		return fmt.Errorf("count remaining roster: %w", err)
	}
	if wasCaptain && otherMembers > 0 && otherCaptains == 0 {
		return ErrLastCaptain
	}
	if _, err := tx.Exec(ctx, `DELETE FROM event_team_memberships WHERE event_id = $1::uuid AND team_id = $2::uuid AND player_id = $3::uuid`, eventID, teamID, playerID); err != nil {
		return fmt.Errorf("remove roster membership: %w", err)
	}
	version++
	if _, err := tx.Exec(ctx, `UPDATE teams SET version = $2 WHERE id = $1::uuid`, teamID, version); err != nil {
		return fmt.Errorf("advance team roster version: %w", err)
	}
	before := map[string]any{"present": true, "is_captain": wasCaptain, "player_name": playerName}
	if err := recordRosterAudit(ctx, tx, actorUserID, teamID, playerID, "removed", before, map[string]any{"present": false}, reason); err != nil {
		return err
	}
	req := SetTeamMemberRequest{EventID: eventID, TeamID: teamID, PlayerID: playerID, ActorUserID: actorUserID}
	if err := publishRosterChanged(ctx, tx, req, version, "removed", false); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit roster removal: %w", err)
	}
	return nil
}

func lockRosterTeam(ctx context.Context, tx pgx.Tx, eventID, teamID string) (int64, error) {
	var version int64
	if err := tx.QueryRow(ctx, `SELECT version FROM teams WHERE id = $1::uuid AND event_id = $2::uuid FOR UPDATE`, teamID, eventID).Scan(&version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrDeleteNotFound
		}
		return 0, fmt.Errorf("load roster team: %w", err)
	}
	return version, nil
}

func recordRosterAudit(ctx context.Context, tx pgx.Tx, actorUserID, teamID, playerID, action string, before, after map[string]any, reason string) error {
	before["player_id"] = playerID
	after["player_id"] = playerID
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return fmt.Errorf("marshal roster audit before data: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return fmt.Errorf("marshal roster audit after data: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, $2, 'team', $3::uuid, $4, $5::jsonb, $6::jsonb)`,
		actorUserID, "competition.roster_"+action, teamID, reason, beforeJSON, afterJSON); err != nil {
		return fmt.Errorf("record roster audit for player %s: %w", playerID, err)
	}
	return nil
}

func publishRosterChanged(ctx context.Context, tx pgx.Tx, req SetTeamMemberRequest, version int64, action string, isCaptain bool) error {
	eventID, err := newUUID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(events.TeamRosterChangedPayload{
		EventID: req.EventID, TeamID: req.TeamID, PlayerID: req.PlayerID, Action: action, IsCaptain: isCaptain,
	})
	if err != nil {
		return fmt.Errorf("marshal roster event: %w", err)
	}
	if err := eventspg.Publish(ctx, tx, events.Envelope{
		ID: eventID, AggregateType: "team", AggregateID: req.TeamID, AggregateVersion: version,
		Type: events.TeamRosterChanged, Payload: payload, OccurredAt: time.Now().UTC(),
	}, maxOutboxAttempts); err != nil {
		return fmt.Errorf("publish roster event: %w", err)
	}
	return nil
}
