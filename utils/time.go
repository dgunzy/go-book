package utils

import (
	"time"
)

const (
	UIInputFormat = time.RFC3339 // Updated to match Flatpickr's ISO 8601 output
	SQLiteFormat  = time.RFC3339 // Keeping this the same
)

// UIToGo converts a UI input time string (now in ISO 8601 format) to a Go time.Time object.
func UIToGo(uiTime string) (time.Time, error) {
	return time.Parse(UIInputFormat, uiTime)
}

// GoToSQLite converts a Go time.Time object to a SQLite-compatible string.
func GoToSQLite(goTime time.Time) string {
	return goTime.Format(SQLiteFormat)
}

// SQLiteToGo converts a SQLite time string to a Go time.Time object.
func SQLiteToGo(sqliteTime string) (time.Time, error) {
	return time.Parse(SQLiteFormat, sqliteTime)
}

// GoToUI converts a Go time.Time object to a UI input format string (ISO 8601).
func GoToUI(goTime time.Time) string {
	return goTime.Format(UIInputFormat)
}
