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

//go:embed 000002_identity_and_legacy_book.up.sql
var identityAndLegacyBookSQL string

//go:embed 000003_market_currency.up.sql
var marketCurrencySQL string

//go:embed 000004_dynamic_pricing.up.sql
var dynamicPricingSQL string

//go:embed 000005_player_auto_approve.up.sql
var playerAutoApproveSQL string

//go:embed 000006_credit_limit.up.sql
var creditLimitSQL string

// All returns migrations in application order.
func All() []Definition {
	return []Definition{
		newDefinition(1, "initial", initialSQL),
		newDefinition(2, "identity_and_legacy_book", identityAndLegacyBookSQL),
		newDefinition(3, "market_currency", marketCurrencySQL),
		newDefinition(4, "dynamic_pricing", dynamicPricingSQL),
		newDefinition(5, "player_auto_approve", playerAutoApproveSQL),
		newDefinition(6, "credit_limit", creditLimitSQL),
	}
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
