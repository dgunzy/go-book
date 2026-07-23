package competitionpg

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PublicCompetitionSnapshot is the authoritative, event-derived public read
// model. It intentionally remains separate from the labelled legacy snapshot.
type PublicCompetitionSnapshot struct {
	Seasons []PublicSeasonRow
	Career  []PublicPlayerStatRow
}

type PublicSeasonRow struct {
	EventID       string
	Name          string
	Year          int
	Venue         string
	VerifiedCount int
	Matches       []PublicMatchRow
	Teams         []PublicTeamStandingRow
	Players       []PublicPlayerStatRow
}

type PublicMatchRow struct {
	Number             int
	Format             string
	Side1Team          string
	Side1Players       string
	Side2Team          string
	Side2Players       string
	Outcome            string
	Score              string
	VerificationMethod string
	VerifiedAt         time.Time
}

func (m PublicMatchRow) WinnerLabel() string {
	left := m.Side1Players
	if left == "" {
		left = m.Side1Team
	}
	right := m.Side2Players
	if right == "" {
		right = m.Side2Team
	}
	switch m.Outcome {
	case "side_1":
		return left
	case "side_2":
		return right
	default:
		return "Tied"
	}
}

func (m PublicMatchRow) VerificationLabel() string {
	switch m.VerificationMethod {
	case "opponent":
		return "Opponent confirmed"
	case "captain":
		return "Captain confirmed"
	case "admin_override":
		return "Admin verified"
	case "migration":
		return "Imported verification"
	default:
		return "Verified"
	}
}

type PublicTeamStandingRow struct {
	TeamName string
	Played   int
	Wins     int
	Losses   int
	Ties     int
	Points   string
}

type PublicPlayerStatRow struct {
	PlayerName    string
	Played        int
	Wins          int
	Losses        int
	Ties          int
	Points        string
	SinglesPlayed int
	SinglesWins   int
	SinglesLosses int
	SinglesTies   int
	SinglesPoints string
	TeamPlayed    int
	TeamWins      int
	TeamLosses    int
	TeamTies      int
	TeamPoints    string
}

// PublicCompetition returns verified match history plus event and career
// projections. No pending or disputed result is exposed.
func (s Store) PublicCompetition(ctx context.Context) (PublicCompetitionSnapshot, error) {
	if s.Pool == nil {
		return PublicCompetitionSnapshot{}, errors.New("competitionpg: pool is required")
	}
	result := PublicCompetitionSnapshot{}
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id::text, e.name, e.season_year, coalesce(e.venue, ''), count(vr.id)::integer
		FROM events e
		JOIN matches m ON m.event_id = e.id
		JOIN LATERAL (
			SELECT id FROM verified_results WHERE match_id = m.id ORDER BY version DESC LIMIT 1
		) vr ON true
		GROUP BY e.id, e.name, e.season_year, e.venue
		ORDER BY e.season_year DESC, e.name`)
	if err != nil {
		return result, fmt.Errorf("list public competition seasons: %w", err)
	}
	seasonIndex := make(map[string]int)
	for rows.Next() {
		var season PublicSeasonRow
		if err := rows.Scan(&season.EventID, &season.Name, &season.Year, &season.Venue, &season.VerifiedCount); err != nil {
			rows.Close()
			return result, err
		}
		seasonIndex[season.EventID] = len(result.Seasons)
		result.Seasons = append(result.Seasons, season)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Seasons) == 0 {
		return result, nil
	}

	if err := s.loadPublicMatches(ctx, &result, seasonIndex); err != nil {
		return PublicCompetitionSnapshot{}, err
	}
	if err := s.loadPublicTeams(ctx, &result, seasonIndex); err != nil {
		return PublicCompetitionSnapshot{}, err
	}
	career, seasons, err := s.loadPublicPlayerStats(ctx)
	if err != nil {
		return PublicCompetitionSnapshot{}, err
	}
	result.Career = career
	for eventID, players := range seasons {
		if index, ok := seasonIndex[eventID]; ok {
			result.Seasons[index].Players = players
		}
	}
	return result, nil
}

func (s Store) loadPublicMatches(ctx context.Context, snapshot *PublicCompetitionSnapshot, seasonIndex map[string]int) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT m.event_id::text, m.match_number, m.format,
		       t1.name, coalesce(p1.names, ''), t2.name, coalesce(p2.names, ''),
		       vr.outcome, coalesce(vr.display_score, ''), vr.verification_method, vr.verified_at
		FROM matches m
		JOIN match_sides s1 ON s1.match_id = m.id AND s1.side_number = 1
		JOIN teams t1 ON t1.id = s1.team_id
		JOIN match_sides s2 ON s2.match_id = m.id AND s2.side_number = 2
		JOIN teams t2 ON t2.id = s2.team_id
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
		JOIN LATERAL (
			SELECT outcome, display_score, verification_method, verified_at
			FROM verified_results WHERE match_id = m.id ORDER BY version DESC LIMIT 1
		) vr ON true
		ORDER BY m.event_id, m.match_number`)
	if err != nil {
		return fmt.Errorf("list public verified matches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID string
		var match PublicMatchRow
		if err := rows.Scan(&eventID, &match.Number, &match.Format,
			&match.Side1Team, &match.Side1Players, &match.Side2Team, &match.Side2Players,
			&match.Outcome, &match.Score, &match.VerificationMethod, &match.VerifiedAt); err != nil {
			return err
		}
		if index, ok := seasonIndex[eventID]; ok {
			snapshot.Seasons[index].Matches = append(snapshot.Seasons[index].Matches, match)
		}
	}
	return rows.Err()
}

func (s Store) loadPublicTeams(ctx context.Context, snapshot *PublicCompetitionSnapshot, seasonIndex map[string]int) error {
	rows, err := s.Pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (match_id) match_id, outcome
			FROM verified_results ORDER BY match_id, version DESC
		)
		SELECT m.event_id::text, t.name, count(*)::integer,
		       count(*) FILTER (WHERE (l.outcome = 'side_1' AND ms.side_number = 1) OR (l.outcome = 'side_2' AND ms.side_number = 2))::integer,
		       count(*) FILTER (WHERE (l.outcome = 'side_1' AND ms.side_number = 2) OR (l.outcome = 'side_2' AND ms.side_number = 1))::integer,
		       count(*) FILTER (WHERE l.outcome = 'tie')::integer,
		       sum(CASE WHEN l.outcome = 'tie' THEN 0.5
		                WHEN (l.outcome = 'side_1' AND ms.side_number = 1) OR (l.outcome = 'side_2' AND ms.side_number = 2) THEN 1
		                ELSE 0 END)::numeric(10,2)::text
		FROM latest l
		JOIN matches m ON m.id = l.match_id
		JOIN match_sides ms ON ms.match_id = m.id
		JOIN teams t ON t.id = ms.team_id
		WHERE l.outcome IN ('side_1', 'side_2', 'tie')
		GROUP BY m.event_id, t.id, t.name
		ORDER BY m.event_id, sum(CASE WHEN l.outcome = 'tie' THEN 0.5
		                WHEN (l.outcome = 'side_1' AND ms.side_number = 1) OR (l.outcome = 'side_2' AND ms.side_number = 2) THEN 1
		                ELSE 0 END) DESC, t.name`)
	if err != nil {
		return fmt.Errorf("list public team standings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID string
		var team PublicTeamStandingRow
		if err := rows.Scan(&eventID, &team.TeamName, &team.Played, &team.Wins, &team.Losses, &team.Ties, &team.Points); err != nil {
			return err
		}
		if index, ok := seasonIndex[eventID]; ok {
			snapshot.Seasons[index].Teams = append(snapshot.Seasons[index].Teams, team)
		}
	}
	return rows.Err()
}

func (s Store) loadPublicPlayerStats(ctx context.Context) ([]PublicPlayerStatRow, map[string][]PublicPlayerStatRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT sp.event_id::text, p.display_name,
		       sp.matches_played, sp.wins, sp.losses, sp.ties, sp.points_won::text,
		       sp.singles_played, sp.singles_wins, sp.singles_losses, sp.singles_ties, sp.singles_points::text,
		       sp.team_played, sp.team_wins, sp.team_losses, sp.team_ties, sp.team_points::text
		FROM player_stat_projections sp
		JOIN players p ON p.id = sp.player_id
		ORDER BY sp.event_id, sp.points_won DESC, sp.wins DESC, p.display_name`)
	if err != nil {
		return nil, nil, fmt.Errorf("list public season player statistics: %w", err)
	}
	bySeason := make(map[string][]PublicPlayerStatRow)
	for rows.Next() {
		var eventID string
		var player PublicPlayerStatRow
		if err := scanPublicPlayer(rows, &eventID, &player); err != nil {
			rows.Close()
			return nil, nil, err
		}
		bySeason[eventID] = append(bySeason[eventID], player)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT ''::text, p.display_name,
		       sum(sp.matches_played)::integer, sum(sp.wins)::integer, sum(sp.losses)::integer, sum(sp.ties)::integer, sum(sp.points_won)::numeric(10,2)::text,
		       sum(sp.singles_played)::integer, sum(sp.singles_wins)::integer, sum(sp.singles_losses)::integer, sum(sp.singles_ties)::integer, sum(sp.singles_points)::numeric(10,2)::text,
		       sum(sp.team_played)::integer, sum(sp.team_wins)::integer, sum(sp.team_losses)::integer, sum(sp.team_ties)::integer, sum(sp.team_points)::numeric(10,2)::text
		FROM player_stat_projections sp
		JOIN players p ON p.id = sp.player_id
		GROUP BY p.id, p.display_name
		ORDER BY sum(sp.points_won) DESC, sum(sp.wins) DESC, p.display_name`)
	if err != nil {
		return nil, nil, fmt.Errorf("list public career player statistics: %w", err)
	}
	defer rows.Close()
	var career []PublicPlayerStatRow
	for rows.Next() {
		var ignored string
		var player PublicPlayerStatRow
		if err := scanPublicPlayer(rows, &ignored, &player); err != nil {
			return nil, nil, err
		}
		career = append(career, player)
	}
	return career, bySeason, rows.Err()
}

type publicPlayerScanner interface{ Scan(...any) error }

func scanPublicPlayer(row publicPlayerScanner, eventID *string, player *PublicPlayerStatRow) error {
	return row.Scan(eventID, &player.PlayerName,
		&player.Played, &player.Wins, &player.Losses, &player.Ties, &player.Points,
		&player.SinglesPlayed, &player.SinglesWins, &player.SinglesLosses, &player.SinglesTies, &player.SinglesPoints,
		&player.TeamPlayed, &player.TeamWins, &player.TeamLosses, &player.TeamTies, &player.TeamPoints)
}
