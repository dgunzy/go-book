// Package competitionpg implements PostgreSQL persistence for the competition
// domain: events, teams, matches, and admin-verified match results. Recording
// a verified result writes the verified_results row and publishes a
// MatchResultVerified outbox event in one transaction, which the betting
// settlement consumer (internal/bettingpg) then acts on to settle linked
// match markets. This is the producer side of the match-driven, event-based
// settlement model.
package competitionpg

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxOutboxAttempts = 20

// Store persists the competition domain.
type Store struct{ Pool *pgxpool.Pool }

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(value string) string {
	s := slugStrip.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	return strings.Trim(s, "-")
}

func newUUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// CreateEventRequest describes a new competition event.
type CreateEventRequest struct {
	Name       string
	Venue      string
	SeasonYear int
	CreatedBy  string
}

// CreateEvent inserts a competition event and returns its ID.
func (s Store) CreateEvent(ctx context.Context, req CreateEventRequest) (string, error) {
	if s.Pool == nil {
		return "", errors.New("competitionpg: pool is required")
	}
	name := strings.TrimSpace(req.Name)
	venue := strings.TrimSpace(req.Venue)
	if name == "" || venue == "" {
		return "", errors.New("event requires a name and venue")
	}
	if req.SeasonYear < 2010 || req.SeasonYear > 2200 {
		return "", errors.New("event season year is out of range")
	}
	slug := slugify(fmt.Sprintf("%s-%d", name, req.SeasonYear))
	if slug == "" {
		return "", errors.New("event name must contain letters or digits")
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO events (slug, name, season_year, venue, state, created_by)
		VALUES ($1, $2, $3, $4, 'active', $5::uuid) RETURNING id::text`,
		slug, name, req.SeasonYear, venue, req.CreatedBy).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert event: %w", err)
	}
	return id, nil
}

// CreateTeam inserts a team into an event and returns its ID.
func (s Store) CreateTeam(ctx context.Context, eventID, name, createdBy string) (string, error) {
	if s.Pool == nil {
		return "", errors.New("competitionpg: pool is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("team requires a name")
	}
	slug := slugify(name)
	if slug == "" {
		return "", errors.New("team name must contain letters or digits")
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO teams (event_id, slug, name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		eventID, slug, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert team: %w", err)
	}
	return id, nil
}

// MatchCreated reports the new match and its two side IDs. The side IDs are the
// values a match-type betting market must key its selections to as
// "side:<id>" so the market settles automatically from the verified result.
type MatchCreated struct {
	MatchID string
	Side1ID string
	Side2ID string
}

// CreateMatchRequest describes a new match: two team sides and, optionally, the
// players on each side. Participants are what event-derived statistics are
// projected onto when the result is verified.
type CreateMatchRequest struct {
	EventID        string
	Format         string
	Side1TeamID    string
	Side2TeamID    string
	Side1PlayerIDs []string
	Side2PlayerIDs []string
	CreatedBy      string
}

// CreateMatch inserts a match with two sides and their participants, returning
// the match and side IDs. The match is created open so betting markets can be
// attached to it.
func (s Store) CreateMatch(ctx context.Context, req CreateMatchRequest) (MatchCreated, error) {
	if s.Pool == nil {
		return MatchCreated{}, errors.New("competitionpg: pool is required")
	}
	if req.Side1TeamID == req.Side2TeamID {
		return MatchCreated{}, errors.New("a match needs two different teams")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return MatchCreated{}, fmt.Errorf("begin create match: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var matchNumber int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(match_number), 0) + 1 FROM matches WHERE event_id = $1::uuid`, req.EventID).Scan(&matchNumber); err != nil {
		return MatchCreated{}, fmt.Errorf("next match number: %w", err)
	}
	var matchID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO matches (event_id, match_number, format, state, created_by)
		VALUES ($1::uuid, $2, $3, 'open', $4::uuid) RETURNING id::text`,
		req.EventID, matchNumber, req.Format, req.CreatedBy).Scan(&matchID); err != nil {
		return MatchCreated{}, fmt.Errorf("insert match: %w", err)
	}
	side1ID, err := insertSide(ctx, tx, req.EventID, matchID, 1, req.Side1TeamID)
	if err != nil {
		return MatchCreated{}, err
	}
	side2ID, err := insertSide(ctx, tx, req.EventID, matchID, 2, req.Side2TeamID)
	if err != nil {
		return MatchCreated{}, err
	}
	if err := insertParticipants(ctx, tx, matchID, side1ID, req.Side1PlayerIDs); err != nil {
		return MatchCreated{}, err
	}
	if err := insertParticipants(ctx, tx, matchID, side2ID, req.Side2PlayerIDs); err != nil {
		return MatchCreated{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MatchCreated{}, fmt.Errorf("commit create match: %w", err)
	}
	return MatchCreated{MatchID: matchID, Side1ID: side1ID, Side2ID: side2ID}, nil
}

func insertParticipants(ctx context.Context, tx pgx.Tx, matchID, sideID string, playerIDs []string) error {
	for i, playerID := range playerIDs {
		if strings.TrimSpace(playerID) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO match_participants (match_id, match_side_id, player_id, playing_order)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`, matchID, sideID, playerID, i+1); err != nil {
			return fmt.Errorf("insert participant %s: %w", playerID, err)
		}
	}
	return nil
}

// CreatePlayer inserts a competition player, optionally linked to a login user,
// and returns its ID.
func (s Store) CreatePlayer(ctx context.Context, displayName, linkedUserID string) (string, error) {
	if s.Pool == nil {
		return "", errors.New("competitionpg: pool is required")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return "", errors.New("player requires a display name")
	}
	slug := slugify(displayName)
	if slug == "" {
		return "", errors.New("player name must contain letters or digits")
	}
	// Disambiguate the slug so two players with the same name do not collide.
	unique, err := newUUID()
	if err != nil {
		return "", err
	}
	slug = slug + "-" + strings.Split(unique, "-")[0]
	var id string
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO players (slug, display_name, linked_user_id)
		VALUES ($1, $2, nullif($3, '')::uuid) RETURNING id::text`,
		slug, displayName, linkedUserID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert player: %w", err)
	}
	return id, nil
}

func insertSide(ctx context.Context, tx pgx.Tx, eventID, matchID string, number int, teamID string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO match_sides (event_id, match_id, side_number, team_id)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid) RETURNING id::text`,
		eventID, matchID, number, teamID).Scan(&id); err != nil {
		return "", fmt.Errorf("insert match side %d: %w", number, err)
	}
	return id, nil
}

// RecordResultRequest declares a match's verified result. Winner is "side_1",
// "side_2", or "tie". Reason is recorded on the admin verification.
type RecordResultRequest struct {
	MatchID     string
	Winner      string
	Score       string
	ActorUserID string
	Reason      string
}

// RecordAdminResult writes an admin-authoritative verified result for a match
// and publishes MatchResultVerified in the same transaction. The betting
// settlement consumer then settles any linked match markets. It is idempotent
// per match: a match already verified returns its existing verification.
func (s Store) RecordAdminResult(ctx context.Context, req RecordResultRequest) (string, error) {
	if s.Pool == nil {
		return "", errors.New("competitionpg: pool is required")
	}
	if req.Winner != "side_1" && req.Winner != "side_2" && req.Winner != "tie" {
		return "", errors.New("winner must be side_1, side_2, or tie")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return "", errors.New("a reason is required to record a result")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return "", fmt.Errorf("begin record result: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var eventID, state string
	if err := tx.QueryRow(ctx, `SELECT event_id::text, state FROM matches WHERE id = $1::uuid FOR UPDATE`, req.MatchID).Scan(&eventID, &state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("match %s not found", req.MatchID)
		}
		return "", fmt.Errorf("load match: %w", err)
	}
	if state == "verified" {
		var existing string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM verified_results WHERE match_id = $1::uuid ORDER BY version DESC LIMIT 1`, req.MatchID).Scan(&existing); err != nil {
			return "", fmt.Errorf("load existing verification: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return existing, nil
	}

	sides, err := loadSideIDs(ctx, tx, req.MatchID)
	if err != nil {
		return "", err
	}

	outcome, winningSideID, side1Points, side2Points := resolveOutcome(req.Winner, sides)
	var verificationID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO verified_results (match_id, version, side_1_points, side_2_points, outcome, verification_method, verified_by, verification_reason)
		VALUES ($1::uuid, 1, $2, $3, $4, 'admin_override', $5::uuid, $6) RETURNING id::text`,
		req.MatchID, side1Points, side2Points, verifiedOutcome(req.Winner), req.ActorUserID, req.Reason).Scan(&verificationID); err != nil {
		return "", fmt.Errorf("insert verified result: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE matches SET state = 'verified', updated_at = now() WHERE id = $1::uuid`, req.MatchID); err != nil {
		return "", fmt.Errorf("mark match verified: %w", err)
	}

	eventID2, err := newUUID()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(events.MatchResultVerifiedPayload{
		MatchID: req.MatchID, CompetitionEventID: eventID, VerificationID: verificationID,
		Outcome: outcome, WinningSideID: winningSideID, Score: strings.TrimSpace(req.Score),
	})
	if err != nil {
		return "", fmt.Errorf("marshal result payload: %w", err)
	}
	envelope := events.Envelope{
		ID: eventID2, AggregateType: "match", AggregateID: req.MatchID, AggregateVersion: 1,
		Type: events.MatchResultVerified, Payload: payload, OccurredAt: time.Now().UTC(),
	}
	if err := eventspg.Publish(ctx, tx, envelope, maxOutboxAttempts); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit record result: %w", err)
	}
	return verificationID, nil
}

func loadSideIDs(ctx context.Context, tx pgx.Tx, matchID string) (map[int]string, error) {
	rows, err := tx.Query(ctx, `SELECT side_number, id::text FROM match_sides WHERE match_id = $1::uuid`, matchID)
	if err != nil {
		return nil, fmt.Errorf("load match sides: %w", err)
	}
	defer rows.Close()
	sides := make(map[int]string, 2)
	for rows.Next() {
		var number int
		var id string
		if err := rows.Scan(&number, &id); err != nil {
			return nil, err
		}
		sides[number] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(sides) != 2 {
		return nil, fmt.Errorf("match %s does not have two sides", matchID)
	}
	return sides, nil
}

// resolveOutcome maps the admin winner choice to the event outcome contract
// (Outcome "side_win"/"tie" + winning side ID) and the verified_results points.
func resolveOutcome(winner string, sides map[int]string) (outcome, winningSideID string, side1Points, side2Points float64) {
	switch winner {
	case "side_1":
		return "side_win", sides[1], 1, 0
	case "side_2":
		return "side_win", sides[2], 0, 1
	default: // tie
		return "tie", "", 0.5, 0.5
	}
}

func verifiedOutcome(winner string) string {
	switch winner {
	case "side_1":
		return "side_1"
	case "side_2":
		return "side_2"
	default:
		return "tie"
	}
}

// EventRow is a competition event for the admin matches page.
type EventRow struct {
	ID         string
	Name       string
	SeasonYear int
	Teams      []TeamRow
	Matches    []MatchRow
}

// TeamRow is a team within an event.
type TeamRow struct {
	ID   string
	Name string
}

// MatchRow is a match with its two sides for the admin page. Side IDs are what
// a match market keys its selections to ("side:<id>").
type MatchRow struct {
	ID            string
	Number        int
	Format        string
	State         string
	Side1TeamName string
	Side2TeamName string
	Side1ID       string
	Side2ID       string
}

// ListEvents returns every event with its teams and matches, newest first.
func (s Store) ListEvents(ctx context.Context) ([]EventRow, error) {
	if s.Pool == nil {
		return nil, errors.New("competitionpg: pool is required")
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text, name, season_year FROM events ORDER BY season_year DESC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var eventsList []EventRow
	index := make(map[string]int)
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.ID, &e.Name, &e.SeasonYear); err != nil {
			return nil, err
		}
		index[e.ID] = len(eventsList)
		eventsList = append(eventsList, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(eventsList) == 0 {
		return eventsList, nil
	}

	teamRows, err := s.Pool.Query(ctx, `SELECT event_id::text, id::text, name FROM teams ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer teamRows.Close()
	for teamRows.Next() {
		var eventID string
		var team TeamRow
		if err := teamRows.Scan(&eventID, &team.ID, &team.Name); err != nil {
			return nil, err
		}
		if i, ok := index[eventID]; ok {
			eventsList[i].Teams = append(eventsList[i].Teams, team)
		}
	}
	if err := teamRows.Err(); err != nil {
		return nil, err
	}

	matchRows, err := s.Pool.Query(ctx, `
		SELECT m.event_id::text, m.id::text, m.match_number, m.format, m.state,
		       coalesce(t1.name, ''), coalesce(t2.name, ''),
		       coalesce(s1.id::text, ''), coalesce(s2.id::text, '')
		FROM matches m
		LEFT JOIN match_sides s1 ON s1.match_id = m.id AND s1.side_number = 1
		LEFT JOIN match_sides s2 ON s2.match_id = m.id AND s2.side_number = 2
		LEFT JOIN teams t1 ON t1.id = s1.team_id
		LEFT JOIN teams t2 ON t2.id = s2.team_id
		ORDER BY m.match_number`)
	if err != nil {
		return nil, fmt.Errorf("list matches: %w", err)
	}
	defer matchRows.Close()
	for matchRows.Next() {
		var eventID string
		var m MatchRow
		if err := matchRows.Scan(&eventID, &m.ID, &m.Number, &m.Format, &m.State,
			&m.Side1TeamName, &m.Side2TeamName, &m.Side1ID, &m.Side2ID); err != nil {
			return nil, err
		}
		if i, ok := index[eventID]; ok {
			eventsList[i].Matches = append(eventsList[i].Matches, m)
		}
	}
	return eventsList, matchRows.Err()
}

// PlayerRow is a competition player for selection lists.
type PlayerRow struct {
	ID          string
	DisplayName string
}

// ListPlayers returns all competition players, alphabetically.
func (s Store) ListPlayers(ctx context.Context) ([]PlayerRow, error) {
	if s.Pool == nil {
		return nil, errors.New("competitionpg: pool is required")
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text, display_name FROM players WHERE active ORDER BY display_name`)
	if err != nil {
		return nil, fmt.Errorf("list players: %w", err)
	}
	defer rows.Close()
	var result []PlayerRow
	for rows.Next() {
		var p PlayerRow
		if err := rows.Scan(&p.ID, &p.DisplayName); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// StandingRow is one player's event-derived record for a standings view.
type StandingRow struct {
	PlayerName string
	EventName  string
	Played     int
	Wins       int
	Losses     int
	Ties       int
	Points     string
}

// ListStandings returns event-derived player standings (from verified match
// results), most points first.
func (s Store) ListStandings(ctx context.Context) ([]StandingRow, error) {
	if s.Pool == nil {
		return nil, errors.New("competitionpg: pool is required")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT p.display_name, e.name, sp.matches_played, sp.wins, sp.losses, sp.ties, sp.points_won::text
		FROM player_stat_projections sp
		JOIN players p ON p.id = sp.player_id
		JOIN events e ON e.id = sp.event_id
		ORDER BY e.season_year DESC, sp.points_won DESC, sp.wins DESC, p.display_name`)
	if err != nil {
		return nil, fmt.Errorf("list standings: %w", err)
	}
	defer rows.Close()
	var result []StandingRow
	for rows.Next() {
		var r StandingRow
		if err := rows.Scan(&r.PlayerName, &r.EventName, &r.Played, &r.Wins, &r.Losses, &r.Ties, &r.Points); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
