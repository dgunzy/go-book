package publicseed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestLoadSeedData(t *testing.T) {
	data, err := loadSeedData()
	if err != nil {
		t.Fatalf("loadSeedData() error = %v", err)
	}
	if got, want := len(data.snapshot.Players), 23; got != want {
		t.Fatalf("players = %d, want %d", got, want)
	}
	if got, want := len(data.snapshot.Events), 7; got != want {
		t.Fatalf("events = %d, want %d", got, want)
	}
	if got, want := countImportableEvents(data.snapshot), 6; got != want {
		t.Fatalf("importable events = %d, want %d", got, want)
	}
	if got, want := len(data.media), 22; got != want {
		t.Fatalf("media = %d, want %d", got, want)
	}
	if got, want := countEventPhotos(data.snapshot), 18; got != want {
		t.Fatalf("event photos = %d, want %d", got, want)
	}

	links := 0
	seenKeys := make(map[string]bool, len(data.media))
	for _, media := range data.media {
		links += len(media.PlayerSlugs)
		if seenKeys[media.ObjectKey] {
			t.Errorf("duplicate object key %q", media.ObjectKey)
		}
		seenKeys[media.ObjectKey] = true
		if media.ByteSize <= 0 || media.Width <= 0 || media.Height <= 0 {
			t.Errorf("invalid dimensions or size for %#v", media)
		}
		if len(media.Checksum) != 64 {
			t.Errorf("checksum length for %q = %d", media.Filename, len(media.Checksum))
		}
		if media.ContentType != "image/jpeg" && media.ContentType != "image/png" {
			t.Errorf("content type for %q = %q", media.Filename, media.ContentType)
		}
	}
	if links != 23 {
		t.Fatalf("media player links = %d, want 23", links)
	}
}

func TestApplyIsRepeatable(t *testing.T) {
	db := &fakeDB{}

	first, err := Apply(context.Background(), db)
	if err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	second, err := Apply(context.Background(), db)
	if err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}

	want := Report{
		Players: 23, Events: 6, StatSnapshots: 23, MediaAssets: 22,
		MediaPlayerLinks: 23, SkippedEventPhotos: 18,
	}
	if first != want || second != want {
		t.Fatalf("reports = %#v, %#v; want %#v", first, second, want)
	}
	if got, want := db.queryCalls, 76*2; got != want {
		t.Fatalf("QueryRow calls = %d, want %d", got, want)
	}
	if got, want := db.execCalls, 98*2; got != want {
		t.Fatalf("Exec calls = %d, want %d", got, want)
	}
	for _, statement := range db.statements {
		if strings.Contains(statement, "INSERT INTO") && !strings.Contains(statement, "ON CONFLICT") {
			t.Errorf("insert is not repeatable: %s", compact(statement))
		}
	}
}

func TestApplyWrapsDatabaseFailure(t *testing.T) {
	db := &fakeDB{queryErrAt: 3, queryErr: errors.New("database unavailable")}
	_, err := Apply(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), `upsert player "alex": database unavailable`) {
		t.Fatalf("Apply() error = %v", err)
	}
}

type fakeDB struct {
	queryCalls int
	execCalls  int
	queryErrAt int
	queryErr   error
	statements []string
}

func (db *fakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	db.queryCalls++
	db.statements = append(db.statements, sql)
	if db.queryErrAt == db.queryCalls {
		return fakeRow{err: db.queryErr}
	}
	return fakeRow{value: fmt.Sprintf("00000000-0000-0000-0000-%012d", db.queryCalls)}
}

func (db *fakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	db.execCalls++
	db.statements = append(db.statements, sql)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

type fakeRow struct {
	value string
	err   error
}

func (row fakeRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("scan destinations = %d, want 1", len(dest))
	}
	value, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("scan destination type = %T, want *string", dest[0])
	}
	*value = row.value
	return nil
}

func compact(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
