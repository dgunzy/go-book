package migrations

import (
	"strings"
	"testing"
)

func TestAll(t *testing.T) {
	t.Parallel()

	definitions := All()
	if len(definitions) != 1 {
		t.Fatalf("migration count = %d, want 1", len(definitions))
	}
	migration := definitions[0]
	if migration.Version != 1 || migration.Name != "initial" {
		t.Fatalf("migration identity = %d/%q", migration.Version, migration.Name)
	}
	if len(migration.Checksum) != 64 {
		t.Fatalf("checksum length = %d, want 64", len(migration.Checksum))
	}
	upperSQL := strings.ToUpper(migration.SQL)
	if strings.Contains(upperSQL, "BEGIN;") || strings.Contains(upperSQL, "COMMIT;") {
		t.Fatal("embedded migration must not manage its own transaction")
	}
}
