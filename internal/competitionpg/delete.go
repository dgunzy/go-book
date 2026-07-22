package competitionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrDeleteNotFound means the setup record no longer exists or is outside
	// the parent event named in the request.
	ErrDeleteNotFound = errors.New("competition setup record not found")
	// ErrDeleteProtected means a record has history that must be retained.
	ErrDeleteProtected = errors.New("competition setup record has protected history")
)

const maxDeleteReasonLength = 500

type deletedSetupRecord struct {
	recordType string
	recordID   string
	name       string
	reason     string
	beforeData []byte
}

// DeleteMatch removes only an unused scheduled/open match. Any betting,
// result, media, statistics, or imported history protects the match.
func (s Store) DeleteMatch(ctx context.Context, matchID, actorUserID, reason string) error {
	return s.deleteSetup(ctx, actorUserID, reason, func(tx pgx.Tx, cleanReason string) (deletedSetupRecord, error) {
		var eventID, format, state, side1, side1Players, side2, side2Players string
		var matchNumber int
		err := tx.QueryRow(ctx, `
			SELECT m.event_id::text, m.match_number, m.format, m.state,
			       coalesce(t1.name, ''), coalesce(p1.names, ''),
			       coalesce(t2.name, ''), coalesce(p2.names, '')
			FROM matches m
			LEFT JOIN match_sides s1 ON s1.match_id = m.id AND s1.side_number = 1
			LEFT JOIN teams t1 ON t1.id = s1.team_id
			LEFT JOIN match_sides s2 ON s2.match_id = m.id AND s2.side_number = 2
			LEFT JOIN teams t2 ON t2.id = s2.team_id
			LEFT JOIN LATERAL (
				SELECT string_agg(p.display_name, ', ' ORDER BY mp.playing_order, p.display_name) AS names
				FROM match_participants mp JOIN players p ON p.id = mp.player_id
				WHERE mp.match_side_id = s1.id
			) p1 ON true
			LEFT JOIN LATERAL (
				SELECT string_agg(p.display_name, ', ' ORDER BY mp.playing_order, p.display_name) AS names
				FROM match_participants mp JOIN players p ON p.id = mp.player_id
				WHERE mp.match_side_id = s2.id
			) p2 ON true
			WHERE m.id = $1::uuid
			FOR UPDATE OF m`, matchID).Scan(
			&eventID, &matchNumber, &format, &state,
			&side1, &side1Players, &side2, &side2Players,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return deletedSetupRecord{}, ErrDeleteNotFound
		}
		if err != nil {
			return deletedSetupRecord{}, fmt.Errorf("load match for deletion: %w", err)
		}

		var protected bool
		if err := tx.QueryRow(ctx, `
			SELECT $2 NOT IN ('scheduled', 'open')
			    OR EXISTS (SELECT 1 FROM markets WHERE match_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM result_submissions WHERE match_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM verified_results WHERE match_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM media_match_links WHERE match_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM match_stat_applications WHERE match_id = $1::uuid)
			    OR EXISTS (
			        SELECT 1 FROM legacy_import_records
			        WHERE target_table = 'matches' AND target_id = $1::uuid
			    )`, matchID, state).Scan(&protected); err != nil {
			return deletedSetupRecord{}, fmt.Errorf("check match deletion history: %w", err)
		}
		if protected {
			return deletedSetupRecord{}, ErrDeleteProtected
		}

		if _, err := tx.Exec(ctx, `DELETE FROM matches WHERE id = $1::uuid`, matchID); err != nil {
			return deletedSetupRecord{}, fmt.Errorf("delete match: %w", err)
		}
		before, err := json.Marshal(map[string]any{
			"event_id": eventID, "match_number": matchNumber, "format": format,
			"state":       state,
			"side_1_team": side1, "side_1_players": side1Players,
			"side_2_team": side2, "side_2_players": side2Players,
		})
		if err != nil {
			return deletedSetupRecord{}, fmt.Errorf("marshal match audit: %w", err)
		}
		name := fmt.Sprintf("Match %d · %s vs %s", matchNumber, side1, side2)
		return deletedSetupRecord{"match", matchID, name, cleanReason, before}, nil
	})
}

// DeleteTeam removes only an unused team in the named event. A team assigned
// to a match or event roster is protected.
func (s Store) DeleteTeam(ctx context.Context, eventID, teamID, actorUserID, reason string) error {
	return s.deleteSetup(ctx, actorUserID, reason, func(tx pgx.Tx, cleanReason string) (deletedSetupRecord, error) {
		var name, slug string
		err := tx.QueryRow(ctx, `
			SELECT name, slug FROM teams
			WHERE id = $1::uuid AND event_id = $2::uuid
			FOR UPDATE`, teamID, eventID).Scan(&name, &slug)
		if errors.Is(err, pgx.ErrNoRows) {
			return deletedSetupRecord{}, ErrDeleteNotFound
		}
		if err != nil {
			return deletedSetupRecord{}, fmt.Errorf("load team for deletion: %w", err)
		}
		var protected bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM match_sides WHERE team_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM event_team_memberships WHERE team_id = $1::uuid)
			    OR EXISTS (
			        SELECT 1 FROM legacy_import_records
			        WHERE target_table = 'teams' AND target_id = $1::uuid
			    )`, teamID).Scan(&protected); err != nil {
			return deletedSetupRecord{}, fmt.Errorf("check team deletion history: %w", err)
		}
		if protected {
			return deletedSetupRecord{}, ErrDeleteProtected
		}
		if _, err := tx.Exec(ctx, `DELETE FROM teams WHERE id = $1::uuid`, teamID); err != nil {
			return deletedSetupRecord{}, fmt.Errorf("delete team: %w", err)
		}
		before, err := json.Marshal(map[string]any{"event_id": eventID, "name": name, "slug": slug})
		if err != nil {
			return deletedSetupRecord{}, fmt.Errorf("marshal team audit: %w", err)
		}
		return deletedSetupRecord{"team", teamID, name, cleanReason, before}, nil
	})
}

// DeleteEvent removes only an empty, non-historical event. Teams and matches
// must be removed first; completed/cancelled/imported events stay immutable.
func (s Store) DeleteEvent(ctx context.Context, eventID, actorUserID, reason string) error {
	return s.deleteSetup(ctx, actorUserID, reason, func(tx pgx.Tx, cleanReason string) (deletedSetupRecord, error) {
		var name, slug, venue, state string
		var seasonYear int
		err := tx.QueryRow(ctx, `
			SELECT name, slug, season_year, venue, state
			FROM events WHERE id = $1::uuid
			FOR UPDATE`, eventID).Scan(&name, &slug, &seasonYear, &venue, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return deletedSetupRecord{}, ErrDeleteNotFound
		}
		if err != nil {
			return deletedSetupRecord{}, fmt.Errorf("load event for deletion: %w", err)
		}
		var protected bool
		if err := tx.QueryRow(ctx, `
			SELECT $2 NOT IN ('draft', 'scheduled', 'active')
			    OR EXISTS (SELECT 1 FROM teams WHERE event_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM matches WHERE event_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM player_stat_projections WHERE event_id = $1::uuid)
			    OR EXISTS (SELECT 1 FROM media_event_links WHERE event_id = $1::uuid)
			    OR EXISTS (
			        SELECT 1 FROM legacy_import_records
			        WHERE target_table = 'events' AND target_id = $1::uuid
			    )`, eventID, state).Scan(&protected); err != nil {
			return deletedSetupRecord{}, fmt.Errorf("check event deletion history: %w", err)
		}
		if protected {
			return deletedSetupRecord{}, ErrDeleteProtected
		}
		if _, err := tx.Exec(ctx, `DELETE FROM events WHERE id = $1::uuid`, eventID); err != nil {
			return deletedSetupRecord{}, fmt.Errorf("delete event: %w", err)
		}
		before, err := json.Marshal(map[string]any{
			"name": name, "slug": slug, "season_year": seasonYear, "venue": venue, "state": state,
		})
		if err != nil {
			return deletedSetupRecord{}, fmt.Errorf("marshal event audit: %w", err)
		}
		return deletedSetupRecord{"event", eventID, fmt.Sprintf("%s %d", name, seasonYear), cleanReason, before}, nil
	})
}

func (s Store) deleteSetup(
	ctx context.Context,
	actorUserID string,
	reason string,
	deleteRecord func(pgx.Tx, string) (deletedSetupRecord, error),
) error {
	if s.Pool == nil {
		return errors.New("competitionpg: pool is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > maxDeleteReasonLength {
		return errors.New("deletion requires a reason of at most 500 characters")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin setup deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := requireStaff(ctx, tx, actorUserID); err != nil {
		return err
	}
	record, err := deleteRecord(tx, reason)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6::jsonb, jsonb_build_object('deleted', true))`,
		actorUserID, "competition."+record.recordType+"_deleted", record.recordType,
		record.recordID, record.reason, record.beforeData); err != nil {
		return fmt.Errorf("record setup deletion audit: %w", err)
	}
	if err := publishSetupDeleted(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit setup deletion: %w", err)
	}
	return nil
}

func publishSetupDeleted(ctx context.Context, tx pgx.Tx, record deletedSetupRecord) error {
	eventID, err := newUUID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(events.CompetitionSetupDeletedPayload{
		RecordType: record.recordType, RecordID: record.recordID, Name: record.name, Reason: record.reason,
	})
	if err != nil {
		return fmt.Errorf("marshal setup deletion event: %w", err)
	}
	if err := eventspg.Publish(ctx, tx, events.Envelope{
		ID: eventID, AggregateType: record.recordType, AggregateID: record.recordID, AggregateVersion: 1,
		Type: events.CompetitionSetupDeleted, Payload: payload, OccurredAt: time.Now().UTC(),
	}, maxOutboxAttempts); err != nil {
		return fmt.Errorf("publish setup deletion: %w", err)
	}
	return nil
}
