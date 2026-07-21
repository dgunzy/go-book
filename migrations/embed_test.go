package migrations

import (
	"strings"
	"testing"
)

func TestAll(t *testing.T) {
	t.Parallel()

	definitions := All()
	if len(definitions) != 6 {
		t.Fatalf("migration count = %d, want 6", len(definitions))
	}
	wantNames := []string{"initial", "identity_and_legacy_book", "market_currency", "dynamic_pricing", "player_auto_approve", "credit_limit"}
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
