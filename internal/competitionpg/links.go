package competitionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5"
)

var (
	ErrPlayerNotFound      = errors.New("player not found")
	ErrMemberNotFound      = errors.New("active member not found")
	ErrPlayerAlreadyLinked = errors.New("player is already linked to another member")
	ErrMemberAlreadyLinked = errors.New("member is already linked to another player")
	ErrPlayerLinkMismatch  = errors.New("player is not linked to that member")
)

// PlayerLink maps a historical/competition player to a login user (member).
// LinkedUserID is empty when the player is not yet mapped to any member.
type PlayerLink struct {
	PlayerID     string
	PlayerName   string
	LinkedUserID string
}

// ListPlayerLinks returns every player and the login user (if any) it is
// mapped to, so admins can wire an onboarded member to their historical
// player.
func (s Store) ListPlayerLinks(ctx context.Context, actorUserID string) ([]PlayerLink, error) {
	if s.Pool == nil {
		return nil, errors.New("competitionpg: pool is required")
	}
	if err := requireStaff(ctx, s.Pool, actorUserID); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, display_name, coalesce(linked_user_id::text, '')
		FROM players
		WHERE active OR linked_user_id IS NOT NULL
		ORDER BY display_name, id`)
	if err != nil {
		return nil, fmt.Errorf("list player links: %w", err)
	}
	defer rows.Close()
	var result []PlayerLink
	for rows.Next() {
		var l PlayerLink
		if err := rows.Scan(&l.PlayerID, &l.PlayerName, &l.LinkedUserID); err != nil {
			return nil, fmt.Errorf("scan player link: %w", err)
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate player links: %w", err)
	}
	return result, nil
}

// LinkPlayerToUser maps a player to a login user so that the player's record
// (statistics) is associated with that member. Admins and owners may do this.
// A user may map to at most one player and a player to at most one user; the
// call fails cleanly if either is already linked to someone else. Audited.
func (s Store) LinkPlayerToUser(ctx context.Context, actorUserID, playerID, targetUserID string) error {
	if s.Pool == nil {
		return errors.New("competitionpg: pool is required")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin link player: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := requireStaff(ctx, tx, actorUserID); err != nil {
		return err
	}

	// Locking the active target membership serializes attempts to map the same
	// member to different players. It also ensures only an onboarded, active
	// member can be linked, even if this method is called outside the HTTP UI.
	var membershipID string
	if err := tx.QueryRow(ctx, `
		SELECT memberships.id::text
		FROM memberships
		JOIN users ON users.id = memberships.user_id
		WHERE memberships.user_id = $1::uuid
		  AND memberships.revoked_at IS NULL
		  AND users.status = 'active'
		FOR UPDATE`, targetUserID).Scan(&membershipID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberNotFound
		}
		return fmt.Errorf("load target membership: %w", err)
	}

	// The target user must not already be mapped to a different player.
	var otherPlayer string
	err = tx.QueryRow(ctx, `SELECT id::text FROM players WHERE linked_user_id = $1::uuid AND id <> $2::uuid`, targetUserID, playerID).Scan(&otherPlayer)
	if err == nil {
		return ErrMemberAlreadyLinked
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check existing user link: %w", err)
	}

	// The player must be currently unlinked (or already this user).
	var currentUser string
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(linked_user_id::text, '')
		FROM players
		WHERE id = $1::uuid AND active
		FOR UPDATE`, playerID).Scan(&currentUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlayerNotFound
		}
		return fmt.Errorf("load player: %w", err)
	}
	if currentUser == targetUserID {
		return tx.Commit(ctx) // already linked
	}
	if currentUser != "" {
		return ErrPlayerAlreadyLinked
	}

	if _, err := tx.Exec(ctx, `UPDATE players SET linked_user_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, playerID, targetUserID); err != nil {
		return fmt.Errorf("link player: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'player.linked_to_user', 'player', $2::uuid, 'mapped member to historical player',
		        jsonb_build_object('user_id', null), jsonb_build_object('user_id', $3::text))`,
		actorUserID, playerID, targetUserID); err != nil {
		return fmt.Errorf("record link audit: %w", err)
	}
	if err := publishPlayerLinkEvent(ctx, tx, playerID, targetUserID, events.PlayerLinkedToUser); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UnlinkPlayer removes a player's mapping to a login user. Admins and owners
// may do this. Audited.
func (s Store) UnlinkPlayer(ctx context.Context, actorUserID, playerID, targetUserID string) error {
	if s.Pool == nil {
		return errors.New("competitionpg: pool is required")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin unlink player: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := requireStaff(ctx, tx, actorUserID); err != nil {
		return err
	}
	var currentUser string
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(linked_user_id::text, '')
		FROM players
		WHERE id = $1::uuid
		FOR UPDATE`, playerID).Scan(&currentUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlayerNotFound
		}
		return fmt.Errorf("load player for unlink: %w", err)
	}
	if currentUser == "" {
		return tx.Commit(ctx) // idempotent replay after a successful unlink
	}
	if currentUser != targetUserID {
		return ErrPlayerLinkMismatch
	}
	if _, err := tx.Exec(ctx, `
		UPDATE players
		SET linked_user_id = NULL, updated_at = now()
		WHERE id = $1::uuid`, playerID); err != nil {
		return fmt.Errorf("unlink player: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_entries (actor_user_id, action, target_type, target_id, reason, before_data, after_data)
		VALUES ($1::uuid, 'player.unlinked_from_user', 'player', $2::uuid, 'removed member-player mapping',
		        jsonb_build_object('user_id', $3::text), jsonb_build_object('user_id', null))`,
		actorUserID, playerID, targetUserID); err != nil {
		return fmt.Errorf("record unlink audit: %w", err)
	}
	if err := publishPlayerLinkEvent(ctx, tx, playerID, targetUserID, events.PlayerUnlinkedFromUser); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func publishPlayerLinkEvent(ctx context.Context, tx pgx.Tx, playerID, userID string, eventType events.Type) error {
	var aggregateVersion int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM audit_entries
		WHERE target_type = 'player'
		  AND target_id = $1::uuid
		  AND action IN ('player.linked_to_user', 'player.unlinked_from_user')`, playerID).Scan(&aggregateVersion); err != nil {
		return fmt.Errorf("load player aggregate version: %w", err)
	}
	eventID, err := newUUID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(events.PlayerUserLinkChangedPayload{PlayerID: playerID, UserID: userID})
	if err != nil {
		return fmt.Errorf("marshal player link event: %w", err)
	}
	envelope := events.Envelope{
		ID: eventID, AggregateType: "player", AggregateID: playerID, AggregateVersion: aggregateVersion,
		Type: eventType, Payload: payload, OccurredAt: time.Now().UTC(),
	}
	if err := eventspg.Publish(ctx, tx, envelope, maxOutboxAttempts); err != nil {
		return fmt.Errorf("publish player link event: %w", err)
	}
	return nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// requireStaff verifies the actor holds an active admin or owner membership.
func requireStaff(ctx context.Context, db queryRower, actorUserID string) error {
	var role string
	err := db.QueryRow(ctx, `
		SELECT memberships.role
		FROM memberships
		JOIN users ON users.id = memberships.user_id
		WHERE memberships.user_id = $1::uuid
		  AND memberships.revoked_at IS NULL
		  AND users.status = 'active'
		FOR SHARE OF memberships`, actorUserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && role != "admin" && role != "owner") {
		return fmt.Errorf("%w: only an admin or owner may manage competition data", identity.ErrUnauthorized)
	}
	if err != nil {
		return fmt.Errorf("verify staff role: %w", err)
	}
	return nil
}
