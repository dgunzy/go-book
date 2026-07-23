package competitionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StatsProjectionConsumer maintains event-derived player statistics from
// verified match results. It implements events.Consumer for
// MatchResultVerified: for each participant on the match it records a win,
// loss, or tie in player_stat_projections. It is idempotent at the database
// level via the match_stat_applications guard, so redelivery or replay of the
// same match never double-counts.
type StatsProjectionConsumer struct {
	Pool   *pgxpool.Pool
	Logger *slog.Logger
}

func (c *StatsProjectionConsumer) Name() string { return "competition-stats-projection" }

func (c *StatsProjectionConsumer) Handles(t events.Type) bool {
	return t == events.MatchResultVerified
}

func (c *StatsProjectionConsumer) Handle(ctx context.Context, envelope events.Envelope) error {
	if c.Pool == nil {
		return errors.New("competition-stats-projection: pool is required")
	}
	var payload events.MatchResultVerifiedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("parse MatchResultVerified payload: %w", err)
	}
	if payload.MatchID == "" || payload.CompetitionEventID == "" {
		return errors.New("competition-stats-projection: payload missing match or event ID")
	}

	tx, err := c.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin stats projection: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Apply this match's result exactly once, ever.
	tag, err := tx.Exec(ctx, `INSERT INTO match_stat_applications (match_id) VALUES ($1::uuid) ON CONFLICT DO NOTHING`, payload.MatchID)
	if err != nil {
		return fmt.Errorf("guard stats application: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx) // already projected
	}

	rows, err := tx.Query(ctx, `
		SELECT mp.player_id::text, mp.match_side_id::text, m.format
		FROM match_participants mp
		JOIN matches m ON m.id = mp.match_id
		WHERE mp.match_id = $1::uuid`, payload.MatchID)
	if err != nil {
		return fmt.Errorf("load participants: %w", err)
	}
	type participant struct{ playerID, sideID, format string }
	var participants []participant
	for rows.Next() {
		var p participant
		if err := rows.Scan(&p.playerID, &p.sideID, &p.format); err != nil {
			rows.Close()
			return fmt.Errorf("scan participant: %w", err)
		}
		participants = append(participants, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range participants {
		wins, losses, ties, points := outcomeForSide(payload.Outcome, payload.WinningSideID, p.sideID)
		singlesPlayed, singlesWins, singlesLosses, singlesTies, singlesPoints := 0, 0, 0, 0, 0.0
		teamPlayed, teamWins, teamLosses, teamTies, teamPoints := 0, 0, 0, 0, 0.0
		if p.format == "singles" {
			singlesPlayed, singlesWins, singlesLosses, singlesTies, singlesPoints = 1, wins, losses, ties, points
		} else {
			teamPlayed, teamWins, teamLosses, teamTies, teamPoints = 1, wins, losses, ties, points
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO player_stat_projections
			    (player_id, event_id, matches_played, wins, losses, ties, points_won,
			     singles_played, singles_wins, singles_losses, singles_ties, singles_points,
			     team_played, team_wins, team_losses, team_ties, team_points,
			     projection_version, updated_at)
			VALUES ($1::uuid, $2::uuid, 1, $3, $4, $5, $6,
			        $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 1, now())
			ON CONFLICT (player_id, event_id) DO UPDATE SET
				matches_played = player_stat_projections.matches_played + 1,
				wins = player_stat_projections.wins + $3,
				losses = player_stat_projections.losses + $4,
				ties = player_stat_projections.ties + $5,
				points_won = player_stat_projections.points_won + $6,
				singles_played = player_stat_projections.singles_played + $7,
				singles_wins = player_stat_projections.singles_wins + $8,
				singles_losses = player_stat_projections.singles_losses + $9,
				singles_ties = player_stat_projections.singles_ties + $10,
				singles_points = player_stat_projections.singles_points + $11,
				team_played = player_stat_projections.team_played + $12,
				team_wins = player_stat_projections.team_wins + $13,
				team_losses = player_stat_projections.team_losses + $14,
				team_ties = player_stat_projections.team_ties + $15,
				team_points = player_stat_projections.team_points + $16,
				projection_version = player_stat_projections.projection_version + 1,
				updated_at = now()`,
			p.playerID, payload.CompetitionEventID, wins, losses, ties, points,
			singlesPlayed, singlesWins, singlesLosses, singlesTies, singlesPoints,
			teamPlayed, teamWins, teamLosses, teamTies, teamPoints); err != nil {
			return fmt.Errorf("project stats for player %s: %w", p.playerID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit stats projection: %w", err)
	}
	if len(participants) > 0 {
		c.logger().Info("projected match statistics", "match_id", payload.MatchID, "participants", len(participants))
	}
	return nil
}

// outcomeForSide returns the win/loss/tie flags and points for a participant on
// sideID given the match outcome. Points: a win is 1, a tie 0.5, a loss 0.
func outcomeForSide(outcome, winningSideID, sideID string) (wins, losses, ties int, points float64) {
	switch outcome {
	case "tie":
		return 0, 0, 1, 0.5
	case "side_win":
		if sideID == winningSideID {
			return 1, 0, 0, 1
		}
		return 0, 1, 0, 0
	default:
		return 0, 0, 0, 0
	}
}

func (c *StatsProjectionConsumer) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
