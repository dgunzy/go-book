package webtime

import (
	"testing"
	"time"
)

func TestParseFormUsesAtlanticDaylightTime(t *testing.T) {
	parsed, err := ParseForm("2026-07-22T08:00")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.July, 22, 11, 0, 0, 0, time.UTC)
	if !parsed.Equal(want) {
		t.Fatalf("parsed = %s, want %s", parsed, want)
	}
	if got := Format(parsed); got != "Jul 22, 2026 08:00 ADT" {
		t.Fatalf("formatted = %q", got)
	}
}

func TestParseFormUsesAtlanticStandardTime(t *testing.T) {
	parsed, err := ParseForm("2026-12-22T08:00")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.December, 22, 12, 0, 0, 0, time.UTC)
	if !parsed.Equal(want) {
		t.Fatalf("parsed = %s, want %s", parsed, want)
	}
	if got := Format(parsed); got != "Dec 22, 2026 08:00 AST" {
		t.Fatalf("formatted = %q", got)
	}
}
