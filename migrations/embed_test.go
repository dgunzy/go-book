package migrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAll(t *testing.T) {
	t.Parallel()

	definitions := All()
	if len(definitions) != 16 {
		t.Fatalf("migration count = %d, want 16", len(definitions))
	}
	wantNames := []string{"initial", "identity_and_legacy_book", "market_currency", "dynamic_pricing", "player_auto_approve", "credit_limit", "stat_projection_guard", "one_active_match_market", "match_history_and_rosters", "credit_limit_default", "manual_line_moves", "selection_restrictions", "wager_placed_by", "market_stake_limit", "selection_stake_limit", "parlays"}
	for index, migration := range definitions {
		if migration.Version != int64(index+1) || migration.Name != wantNames[index] {
			t.Fatalf("migration %d identity = %d/%q", index, migration.Version, migration.Name)
		}
		if len(migration.Checksum) != 64 {
			t.Fatalf("migration %d checksum length = %d, want 64", index, len(migration.Checksum))
		}
		upperSQL := strings.ToUpper(migration.SQL)
		if strings.Contains(upperSQL, "BEGIN;") || strings.Contains(upperSQL, "COMMIT;") {
			t.Fatalf("embedded migration %d must not manage its own transaction", index)
		}
	}
}

// TestEveryMigrationFileIsRegistered is the guard that a new migration is
// actually applied. A .up.sql file that nobody adds to All() is invisible to
// the migrate command: the schema stays behind while code that depends on it
// ships, and the application fails against a database it believes is current.
func TestEveryMigrationFileIsRegistered(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files found")
	}
	registered := make(map[string]bool, len(All()))
	for _, definition := range All() {
		registered[definition.Name] = true
	}
	for _, file := range files {
		// 000011_manual_line_moves.up.sql -> manual_line_moves
		name := strings.TrimSuffix(filepath.Base(file), ".up.sql")
		if index := strings.Index(name, "_"); index >= 0 {
			name = name[index+1:]
		}
		if !registered[name] {
			t.Errorf("%s is not registered in All(): the migrate command will never apply it", file)
		}
	}

	// Every registered migration must also have a down file, so a rollback is
	// always available and CI can rehearse it.
	for _, definition := range All() {
		matches, err := filepath.Glob("*_" + definition.Name + ".down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Errorf("migration %q has %d down files, want exactly 1", definition.Name, len(matches))
		}
	}
}
