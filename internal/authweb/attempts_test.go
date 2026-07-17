package authweb

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeAttemptDB struct {
	execSQL   string
	execArgs  []any
	execErr   error
	querySQL  string
	queryArgs []any
	row       pgx.Row
}

func (f *fakeAttemptDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execSQL, f.execArgs = sql, args
	return pgconn.NewCommandTag("INSERT 0 1"), f.execErr
}

func (f *fakeAttemptDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.querySQL, f.queryArgs = sql, args
	return f.row
}

type fakeAttemptRow struct {
	scan func(...any) error
}

func (r fakeAttemptRow) Scan(dest ...any) error { return r.scan(dest...) }

func TestAttemptHashesAreDomainSeparatedAndCopied(t *testing.T) {
	t.Parallel()

	state, nonce := HashState("same-value"), HashNonce("same-value")
	if state.Equal(nonce) || !state.Equal(HashState("same-value")) {
		t.Fatal("attempt hash domains are not isolated or deterministic")
	}
	copyBytes := state.Bytes()
	copyBytes[0] ^= 0xff
	if bytes.Equal(copyBytes, state.Bytes()) {
		t.Fatal("AttemptHash.Bytes aliases internal memory")
	}
}

func TestNewPostgresAttemptStoreRejectsNilPool(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresAttemptStore(nil); err == nil {
		t.Fatal("NewPostgresAttemptStore(nil) error = nil")
	}
}

func TestPostgresAttemptStoreCreateUsesHashedValues(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	db := &fakeAttemptDB{}
	store := PostgresAttemptStore{pool: db}
	attempt := LoginAttempt{
		StateHash: HashState("raw-state"), NonceHash: HashNonce("raw-nonce"),
		PKCEVerifier: strings.Repeat("v", 43), ReturnPath: "/book?tab=wagers",
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := store.Create(context.Background(), attempt); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !strings.Contains(db.execSQL, "INSERT INTO oidc_login_attempts") || len(db.execArgs) != 6 {
		t.Fatalf("SQL=%q args=%#v", db.execSQL, db.execArgs)
	}
	for _, argument := range db.execArgs {
		if argument == "raw-state" || argument == "raw-nonce" {
			t.Fatal("raw state or nonce was persisted")
		}
	}
	if !bytes.Equal(db.execArgs[0].([]byte), attempt.StateHash.Bytes()) || !bytes.Equal(db.execArgs[1].([]byte), attempt.NonceHash.Bytes()) {
		t.Fatal("stored hashes do not match attempt")
	}
}

func TestPostgresAttemptStoreCreateRejectsUnsafeValuesBeforeDatabase(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	for _, mutate := range []func(*LoginAttempt){
		func(a *LoginAttempt) { a.ReturnPath = "https://evil.example" },
		func(a *LoginAttempt) { a.PKCEVerifier = "short" },
		func(a *LoginAttempt) { a.ExpiresAt = a.CreatedAt },
		func(a *LoginAttempt) { a.NonceHash = a.StateHash },
	} {
		db := &fakeAttemptDB{}
		attempt := LoginAttempt{
			StateHash: HashState("state"), NonceHash: HashNonce("nonce"), PKCEVerifier: strings.Repeat("v", 43),
			ReturnPath: "/book", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
		}
		mutate(&attempt)
		if err := (PostgresAttemptStore{pool: db}).Create(context.Background(), attempt); err == nil || db.execSQL != "" {
			t.Fatalf("Create(%#v) error=%v SQL=%q", attempt, err, db.execSQL)
		}
	}
}

func TestPostgresAttemptStoreConsumeIsAtomicAndValidatesRow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	nonceHash := HashNonce("nonce")
	db := &fakeAttemptDB{row: fakeAttemptRow{scan: func(dest ...any) error {
		*(dest[0].(*[]byte)) = nonceHash.Bytes()
		*(dest[1].(*string)) = strings.Repeat("p", 43)
		*(dest[2].(*string)) = "/book/ledger"
		return nil
	}}}
	stateHash := HashState("state")
	result, err := (PostgresAttemptStore{pool: db}).Consume(context.Background(), stateHash, now)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if !result.NonceHash.Equal(nonceHash) || result.ReturnPath != "/book/ledger" || len(result.PKCEVerifier) != 43 {
		t.Fatalf("Consume() = %#v", result)
	}
	if !strings.Contains(db.querySQL, "SET consumed_at = $2") || !strings.Contains(db.querySQL, "consumed_at IS NULL") || !strings.Contains(db.querySQL, "expires_at > $2") {
		t.Fatalf("consume SQL is not atomic: %s", db.querySQL)
	}
	if len(db.queryArgs) != 2 || !bytes.Equal(db.queryArgs[0].([]byte), stateHash.Bytes()) || db.queryArgs[1] != now {
		t.Fatalf("consume args = %#v", db.queryArgs)
	}
}

func TestPostgresAttemptStoreConsumeMapsMissingAndCorruptRows(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	db := &fakeAttemptDB{row: fakeAttemptRow{scan: func(...any) error { return pgx.ErrNoRows }}}
	if _, err := (PostgresAttemptStore{pool: db}).Consume(context.Background(), HashState("state"), now); !errors.Is(err, ErrInvalidLoginAttempt) {
		t.Fatalf("missing row error = %v", err)
	}
	db.row = fakeAttemptRow{scan: func(dest ...any) error {
		*(dest[0].(*[]byte)) = make([]byte, 31)
		*(dest[1].(*string)) = strings.Repeat("p", 43)
		*(dest[2].(*string)) = "/book"
		return nil
	}}
	if _, err := (PostgresAttemptStore{pool: db}).Consume(context.Background(), HashState("state"), now); err == nil || errors.Is(err, ErrInvalidLoginAttempt) {
		t.Fatalf("corrupt row error = %v", err)
	}
}
