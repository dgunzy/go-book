package migrations

import (
	"strings"
	"testing"
)

func TestAll(t *testing.T) {
	t.Parallel()

	definitions := All()
	if len(definitions) != 2 {
		t.Fatalf("migration count = %d, want 2", len(definitions))
	}
	wantNames := []string{"initial", "identity_and_legacy_book"}
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
