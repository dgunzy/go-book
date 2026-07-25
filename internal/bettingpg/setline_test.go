package bettingpg

import (
	"context"
	"errors"
	"testing"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
)

// SetOpeningLine validates its input before opening a transaction, so these
// run without a database — and they are the checks that must never reach one.
// A reason of spaces used to slip through and be rejected by the audit row's
// own constraint, which surfaced as a database error instead of a clear one.
func TestSetOpeningLineValidatesBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	const (
		marketID    = "11111111-1111-4111-8111-111111111111"
		selectionID = "22222222-2222-4222-8222-222222222222"
		actorID     = "33333333-3333-4333-8333-333333333333"
	)
	store := Store{} // no pool: reaching the database at all would panic or error out

	tests := []struct {
		name                             string
		market, selection, actor, reason string
		odds                             int32
		wantErr                          error
	}{
		{"reason of spaces", marketID, selectionID, actorID, "   ", -150, betting.ErrReasonRequired},
		{"empty reason", marketID, selectionID, actorID, "", -150, betting.ErrReasonRequired},
		{"no actor", marketID, selectionID, "", "moving the line", -150, betting.ErrUnauthorized},
		{"malformed market", "nope", selectionID, actorID, "moving the line", -150, betting.ErrInvalid},
		{"malformed selection", marketID, "nope", actorID, "moving the line", -150, betting.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := store.SetOpeningLine(context.Background(), test.market, test.selection,
				ledger.AmericanOdds(test.odds), test.actor, test.reason)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("SetOpeningLine() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

// An odds value inside the -100..+100 dead band is rejected before any
// database work as well.
func TestSetOpeningLineRejectsOddsInTheDeadBand(t *testing.T) {
	t.Parallel()
	store := Store{}
	_, err := store.SetOpeningLine(context.Background(),
		"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		50, "33333333-3333-4333-8333-333333333333", "moving the line")
	if err == nil {
		t.Fatal("SetOpeningLine() accepted an odds value inside the dead band")
	}
}
