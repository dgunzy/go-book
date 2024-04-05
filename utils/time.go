package utils

import (
	"time"
)

const (
	UIInputFormat = "January 2, 2006, 15:04"
	SQLiteFormat  = time.RFC3339 // "2006-01-02T15:04:05Z"
)

// UIToGo converts a UI input time string to a Go time.Time object.
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

// GoToUI converts a Go time.Time object to a UI input format string.
func GoToUI(goTime time.Time) string {
	return goTime.Format(UIInputFormat)
}
