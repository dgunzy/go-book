package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5"
)

// RestrictionRow is one recorded restriction for the admin list: who is barred
// from what, and why.
type RestrictionRow struct {
	MarketID    string
	MarketTitle string
	UserID      string
	MemberName  string
	// SelectionID and SelectionTerms are empty for a whole-market ban.
	SelectionID    string
	SelectionTerms string
	Reason         string
	RestrictedBy   string
	CreatedAt      time.Time
}

// WholeMarket reports whether this bars the member from the market entirely
// rather than from one side of it.
func (r RestrictionRow) WholeMarket() bool { return r.SelectionID == "" }

// RestrictRequest bars a member from a market, or from one selection on it.
// An empty SelectionID restricts the whole market.
type RestrictRequest struct {
	MarketID    string
	UserID      string
	SelectionID string
	Reason      string
	ActorUserID string
}

// RestrictMember records a restriction. Re-recording the same one updates its
// reason rather than failing, so an admin correcting the wording does not have
// to lift and re-apply.
func (s Store) RestrictMember(ctx context.Context, request RestrictRequest) error {
	if !isUUID(request.MarketID) || !isUUID(request.UserID) {
		return fmt.Errorf("%w: a restriction needs a market and a member", betting.ErrInvalid)
	}
	if request.SelectionID != "" && !isUUID(request.SelectionID) {
		return fmt.Errorf("%w: selection %s", betting.ErrNotFound, request.SelectionID)
	}
	if !isUUID(request.ActorUserID) {
		return betting.ErrUnauthorized
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" || len(reason) > 500 {
		return betting.ErrReasonRequired
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A selection-level restriction must name a selection on this market, or
	// it would silently never apply.
	if request.SelectionID != "" {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT true FROM selections WHERE id = $1::uuid AND market_id = $2::uuid`,
			request.SelectionID, request.MarketID).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: selection %s is not on market %s", betting.ErrNotFound, request.SelectionID, request.MarketID)
		}
		if err != nil {
			return fmt.Errorf("verify restricted selection: %w", err)
		}
	}

	// The unique indexes are partial (one for whole-market, one per selection),
	// so the upsert has to name the matching index predicate.
	const wholeMarket = `
		INSERT INTO market_restrictions (market_id, user_id, selection_id, reason, restricted_by)
		VALUES ($1::uuid, $2::uuid, NULL, $3, $4::uuid)
		ON CONFLICT (market_id, user_id) WHERE selection_id IS NULL
		DO UPDATE SET reason = excluded.reason, restricted_by = excluded.restricted_by`
	const oneSelection = `
		INSERT INTO market_restrictions (market_id, user_id, selection_id, reason, restricted_by)
		VALUES ($1::uuid, $2::uuid, $5::uuid, $3, $4::uuid)
		ON CONFLICT (market_id, user_id, selection_id) WHERE selection_id IS NOT NULL
		DO UPDATE SET reason = excluded.reason, restricted_by = excluded.restricted_by`

	if request.SelectionID == "" {
		_, err = tx.Exec(ctx, wholeMarket, request.MarketID, request.UserID, reason, request.ActorUserID)
	} else {
		_, err = tx.Exec(ctx, oneSelection, request.MarketID, request.UserID, reason, request.ActorUserID, request.SelectionID)
	}
	if err != nil {
		return fmt.Errorf("record market restriction: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit market restriction: %w", err)
	}
	return nil
}

// LiftRestriction removes one restriction. An empty selectionID lifts the
// whole-market ban, leaving any side-level ones in place.
func (s Store) LiftRestriction(ctx context.Context, marketID, userID, selectionID string) error {
	if !isUUID(marketID) || !isUUID(userID) {
		return fmt.Errorf("%w: lifting a restriction needs a market and a member", betting.ErrInvalid)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tag pgconnTag
	if selectionID == "" {
		tag, err = tx.Exec(ctx, `
			DELETE FROM market_restrictions
			WHERE market_id = $1::uuid AND user_id = $2::uuid AND selection_id IS NULL`, marketID, userID)
	} else {
		if !isUUID(selectionID) {
			return fmt.Errorf("%w: selection %s", betting.ErrNotFound, selectionID)
		}
		tag, err = tx.Exec(ctx, `
			DELETE FROM market_restrictions
			WHERE market_id = $1::uuid AND user_id = $2::uuid AND selection_id = $3::uuid`, marketID, userID, selectionID)
	}
	if err != nil {
		return fmt.Errorf("lift market restriction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: no such restriction", betting.ErrNotFound)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lifted restriction: %w", err)
	}
	return nil
}

// ListRestrictions returns every restriction on markets that are still live,
// newest first, for the admin view.
func (s Store) ListRestrictions(ctx context.Context) ([]RestrictionRow, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT r.market_id::text, m.title, r.user_id::text, u.display_name,
		       coalesce(r.selection_id::text, ''), coalesce(s.display_terms, ''),
		       r.reason, coalesce(a.display_name, ''), r.created_at
		FROM market_restrictions r
		JOIN markets m ON m.id = r.market_id
		JOIN users u ON u.id = r.user_id
		LEFT JOIN selections s ON s.id = r.selection_id
		LEFT JOIN users a ON a.id = r.restricted_by
		WHERE m.state IN ('draft', 'open', 'closed', 'settlement_pending')
		ORDER BY r.created_at DESC, u.display_name`)
	if err != nil {
		return nil, fmt.Errorf("query market restrictions: %w", err)
	}
	defer rows.Close()

	result := make([]RestrictionRow, 0)
	for rows.Next() {
		var row RestrictionRow
		if err := rows.Scan(&row.MarketID, &row.MarketTitle, &row.UserID, &row.MemberName,
			&row.SelectionID, &row.SelectionTerms, &row.Reason, &row.RestrictedBy, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan market restriction: %w", err)
		}
		row.CreatedAt = row.CreatedAt.UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate market restrictions: %w", err)
	}
	return result, nil
}

// pgconnTag is the subset of pgconn.CommandTag used here, kept as an interface
// so this file does not need the pgconn import.
type pgconnTag interface{ RowsAffected() int64 }

// MemberOption is one member for the restriction picker: identity only, no
// balances or wager history.
type MemberOption struct {
	ID   string
	Name string
}

// ListMembers returns active members for the restriction picker, by name.
func (s Store) ListMembers(ctx context.Context) ([]MemberOption, error) {
	if s.DB == nil {
		return nil, errors.New("bettingpg: PostgreSQL pool is required")
	}
	rows, err := s.DB.Query(ctx, `
		SELECT id::text, display_name FROM users
		WHERE status = 'active' ORDER BY display_name`)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	result := make([]MemberOption, 0)
	for rows.Next() {
		var option MemberOption
		if err := rows.Scan(&option.ID, &option.Name); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		result = append(result, option)
	}
	return result, rows.Err()
}
