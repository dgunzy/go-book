package authweb

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidLoginAttempt = errors.New("invalid or expired login attempt")

const (
	stateHashContext = "cabot-cup/oidc-state/v1\x00"
	nonceHashContext = "cabot-cup/oidc-nonce/v1\x00"
)

type AttemptHash [sha256.Size]byte

func HashState(value string) AttemptHash { return hashAttemptValue(stateHashContext, value) }
func HashNonce(value string) AttemptHash { return hashAttemptValue(nonceHashContext, value) }

func hashAttemptValue(purpose, value string) AttemptHash {
	hash := sha256.New()
	_, _ = hash.Write([]byte(purpose))
	_, _ = hash.Write([]byte(value))
	var result AttemptHash
	copy(result[:], hash.Sum(nil))
	return result
}

func (h AttemptHash) Bytes() []byte {
	result := make([]byte, len(h))
	copy(result, h[:])
	return result
}

func (h AttemptHash) Equal(other AttemptHash) bool {
	return subtle.ConstantTimeCompare(h[:], other[:]) == 1
}

type LoginAttempt struct {
	StateHash    AttemptHash
	NonceHash    AttemptHash
	PKCEVerifier string
	ReturnPath   string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

func (a LoginAttempt) validate() error {
	if a.StateHash == (AttemptHash{}) || a.NonceHash == (AttemptHash{}) || a.StateHash.Equal(a.NonceHash) {
		return errors.New("login attempt requires distinct state and nonce hashes")
	}
	if len(a.PKCEVerifier) < 43 || len(a.PKCEVerifier) > 128 {
		return errors.New("login attempt PKCE verifier must contain 43 to 128 characters")
	}
	if safeReturnPath(a.ReturnPath, "") != a.ReturnPath {
		return errors.New("login attempt return path is unsafe")
	}
	if a.CreatedAt.IsZero() || !a.ExpiresAt.After(a.CreatedAt) {
		return errors.New("login attempt timestamps are invalid")
	}
	return nil
}

type ConsumedAttempt struct {
	NonceHash    AttemptHash
	PKCEVerifier string
	ReturnPath   string
}

type AttemptStore interface {
	Create(context.Context, LoginAttempt) error
	Consume(context.Context, AttemptHash, time.Time) (ConsumedAttempt, error)
}

type postgresAttempts interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// PostgresAttemptStore persists only hashes of browser-visible state and nonce
// values. Consume is a single atomic statement, so a callback can be used once.
type PostgresAttemptStore struct {
	pool postgresAttempts
}

func NewPostgresAttemptStore(pool *pgxpool.Pool) (*PostgresAttemptStore, error) {
	if pool == nil {
		return nil, errors.New("OIDC login attempt PostgreSQL pool is required")
	}
	return &PostgresAttemptStore{pool: pool}, nil
}

func (s PostgresAttemptStore) Create(ctx context.Context, attempt LoginAttempt) error {
	if s.pool == nil {
		return errors.New("OIDC login attempt PostgreSQL pool is required")
	}
	if err := attempt.validate(); err != nil {
		return fmt.Errorf("validate OIDC login attempt: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
DELETE FROM oidc_login_attempts
WHERE expires_at < now() - interval '1 hour'
   OR consumed_at < now() - interval '1 hour'`); err != nil {
		return fmt.Errorf("clean up OIDC login attempts: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
INSERT INTO oidc_login_attempts
    (state_hash, nonce_hash, pkce_verifier, return_path, created_at, expires_at)
SELECT $1, $2, $3, $4, $5, $6
WHERE (SELECT count(*) FROM oidc_login_attempts WHERE consumed_at IS NULL AND expires_at > now()) < 10000`,
		attempt.StateHash.Bytes(), attempt.NonceHash.Bytes(), attempt.PKCEVerifier,
		attempt.ReturnPath, attempt.CreatedAt, attempt.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create OIDC login attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("create OIDC login attempt: active attempt capacity reached")
	}
	return nil
}

func (s PostgresAttemptStore) Consume(ctx context.Context, stateHash AttemptHash, now time.Time) (ConsumedAttempt, error) {
	if s.pool == nil {
		return ConsumedAttempt{}, errors.New("OIDC login attempt PostgreSQL pool is required")
	}
	if stateHash == (AttemptHash{}) || now.IsZero() {
		return ConsumedAttempt{}, ErrInvalidLoginAttempt
	}
	var nonceBytes []byte
	var result ConsumedAttempt
	err := s.pool.QueryRow(ctx, `
UPDATE oidc_login_attempts
   SET consumed_at = $2
 WHERE state_hash = $1
   AND consumed_at IS NULL
   AND expires_at > $2
RETURNING nonce_hash, pkce_verifier, return_path`, stateHash.Bytes(), now).
		Scan(&nonceBytes, &result.PKCEVerifier, &result.ReturnPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConsumedAttempt{}, ErrInvalidLoginAttempt
	}
	if err != nil {
		return ConsumedAttempt{}, fmt.Errorf("consume OIDC login attempt: %w", err)
	}
	if len(nonceBytes) != sha256.Size {
		return ConsumedAttempt{}, errors.New("consume OIDC login attempt: invalid nonce hash")
	}
	copy(result.NonceHash[:], nonceBytes)
	if len(result.PKCEVerifier) < 43 || len(result.PKCEVerifier) > 128 ||
		safeReturnPath(result.ReturnPath, "") != result.ReturnPath || strings.TrimSpace(result.ReturnPath) == "" {
		return ConsumedAttempt{}, errors.New("consume OIDC login attempt: invalid stored values")
	}
	return result, nil
}
