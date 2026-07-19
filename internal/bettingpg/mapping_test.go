package bettingpg

import (
	"testing"

	"github.com/dgunzy/go-book/internal/betting"
)

func TestBuildMatchOutcomeSideWin(t *testing.T) {
	selections := map[string]string{
		"sel-1": "side:side-a",
		"sel-2": "side:side-b",
	}
	got, ok := buildMatchOutcome(selections, "side_win", "side-a")
	if !ok {
		t.Fatalf("buildMatchOutcome() ok = false, want true")
	}
	want := map[string]betting.SettlementResult{"sel-1": betting.ResultWin, "sel-2": betting.ResultLoss}
	if got["sel-1"] != want["sel-1"] || got["sel-2"] != want["sel-2"] {
		t.Fatalf("buildMatchOutcome() = %v, want %v", got, want)
	}
}

func TestBuildMatchOutcomeTieWithTieSelection(t *testing.T) {
	selections := map[string]string{
		"sel-1": "side:side-a",
		"sel-2": "side:side-b",
		"sel-3": "tie",
	}
	got, ok := buildMatchOutcome(selections, "tie", "")
	if !ok {
		t.Fatalf("buildMatchOutcome() ok = false, want true")
	}
	if got["sel-3"] != betting.ResultWin {
		t.Fatalf("tie selection result = %v, want win", got["sel-3"])
	}
	if got["sel-1"] != betting.ResultLoss || got["sel-2"] != betting.ResultLoss {
		t.Fatalf("side selections on tie = %v, want loss", got)
	}
}

func TestBuildMatchOutcomeTieWithoutTieSelectionPushesEveryone(t *testing.T) {
	selections := map[string]string{
		"sel-1": "side:side-a",
		"sel-2": "side:side-b",
	}
	got, ok := buildMatchOutcome(selections, "tie", "")
	if !ok {
		t.Fatalf("buildMatchOutcome() ok = false, want true")
	}
	for id, result := range got {
		if result != betting.ResultPush {
			t.Fatalf("selection %s result = %v, want push", id, result)
		}
	}
}

func TestBuildMatchOutcomeSkipsUnrecognizedKeys(t *testing.T) {
	cases := map[string]map[string]string{
		"empty key":       {"sel-1": "", "sel-2": "side:side-b"},
		"garbage key":     {"sel-1": "handicap:9", "sel-2": "side:side-b"},
		"no selections":   {},
		"whitespace only": {"sel-1": "   "},
	}
	for name, selections := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := buildMatchOutcome(selections, "side_win", "side-a"); ok {
				t.Fatalf("buildMatchOutcome() ok = true, want false for %s", name)
			}
		})
	}
}

func TestBuildMatchOutcomeUnrecognizedOutcomeValue(t *testing.T) {
	selections := map[string]string{"sel-1": "side:side-a", "sel-2": "side:side-b"}
	if _, ok := buildMatchOutcome(selections, "void", ""); ok {
		t.Fatalf("buildMatchOutcome() ok = true, want false for unrecognized outcome")
	}
}
