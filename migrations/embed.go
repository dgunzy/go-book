// Package migrations contains the ordered SQL schema migrations embedded in the
// application image.
package migrations

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
)

// Definition is one immutable, forward-only schema migration.
type Definition struct {
	Version  int64
	Name     string
	SQL      string
	Checksum string
}

//go:embed 000001_initial.up.sql
var initialSQL string

// All returns migrations in application order.
func All() []Definition {
	return []Definition{newDefinition(1, "initial", initialSQL)}
}

func newDefinition(version int64, name, sql string) Definition {
	checksum := sha256.Sum256([]byte(sql))
	return Definition{
		Version:  version,
		Name:     name,
		SQL:      sql,
		Checksum: fmt.Sprintf("%x", checksum),
	}
}
