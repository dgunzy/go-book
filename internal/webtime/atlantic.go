// Package webtime owns the timezone used by Cabot Cup's user-facing pages.
// Domain events and PostgreSQL remain UTC; only form parsing and display use
// Atlantic time.
package webtime

import (
	"fmt"
	"time"
	_ "time/tzdata"
)

const (
	LocationName  = "America/Halifax"
	FormLayout    = "2006-01-02T15:04"
	DisplayLayout = "Jan 2, 2006 15:04 MST"
)

var atlantic = mustLoadAtlantic()

func mustLoadAtlantic() *time.Location {
	location, err := time.LoadLocation(LocationName)
	if err != nil {
		panic(fmt.Sprintf("load Atlantic timezone: %v", err))
	}
	return location
}

// ParseForm interprets a datetime-local value as Atlantic wall-clock time and
// returns the corresponding UTC instant for storage and domain validation.
func ParseForm(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation(FormLayout, value, atlantic)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// Format renders an instant in Atlantic time, including the seasonally correct
// AST/ADT abbreviation.
func Format(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.In(atlantic).Format(DisplayLayout)
}
