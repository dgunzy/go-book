package legacy

import "testing"

func TestLoadLegacySnapshot(t *testing.T) {
	snapshot, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := len(snapshot.Players), 23; got != want {
		t.Fatalf("len(Players) = %d, want %d", got, want)
	}
	if got, want := len(snapshot.Events), 8; got != want {
		t.Fatalf("len(Events) = %d, want %d", got, want)
	}
	if snapshot.Events[0].Year != 2019 || snapshot.Events[len(snapshot.Events)-1].Year != 2026 {
		t.Fatalf("event range = %d-%d, want 2019-2026", snapshot.Events[0].Year, snapshot.Events[len(snapshot.Events)-1].Year)
	}
	placeholder := eventForYear(t, snapshot, 2025)
	if !placeholder.Placeholder || placeholder.Winner != "" || placeholder.Score != "" || len(placeholder.Photos) != 0 {
		t.Fatalf("2025 placeholder contains inferred results or media: %#v", placeholder)
	}
	if snapshot.Label != SnapshotLabel || snapshot.Note != SourceNote {
		t.Fatal("snapshot is missing its legacy source labels")
	}

	seen := make(map[string]bool, len(snapshot.Players))
	for _, player := range snapshot.Players {
		if player.Name == "" || player.Slug == "" {
			t.Fatalf("player has empty identity: %#v", player)
		}
		if seen[player.Slug] {
			t.Fatalf("duplicate slug %q", player.Slug)
		}
		seen[player.Slug] = true
		if player.Image == "" {
			t.Fatalf("player %q has no image", player.Name)
		}
		if percentage := player.WinningPercentage(); percentage < 0 || percentage > 100 {
			t.Fatalf("player %q percentage = %d", player.Name, percentage)
		}
	}
}

func eventForYear(t *testing.T, snapshot Snapshot, year int) Event {
	t.Helper()
	for _, event := range snapshot.Events {
		if event.Year == year {
			return event
		}
	}
	t.Fatalf("snapshot has no %d event", year)
	return Event{}
}

func TestEvent2026GalleryIsCompleteAndDistinct(t *testing.T) {
	snapshot, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	event := eventForYear(t, snapshot, 2026)
	if event.Placeholder || event.Winner != "Bears" || event.RunnerUp != "Flamingos" || event.Score != "26 - 10" {
		t.Fatalf("2026 result is not the recorded Bears win: %#v", event)
	}
	if got, want := len(event.Rounds), 3; got != want {
		t.Fatalf("len(Rounds) = %d, want %d", got, want)
	}
	if got, want := event.GalleryCount(), 38; got != want {
		t.Fatalf("GalleryCount() = %d, want %d", got, want)
	}
	if !event.HasGallery() {
		t.Fatal("2026 event reports no gallery")
	}

	seen := make(map[string]bool, event.GalleryCount())
	for _, group := range event.Gallery {
		if group.Label == "" || group.Venue == "" {
			t.Fatalf("gallery group is missing its heading: %#v", group)
		}
		for _, photo := range group.Photos {
			if photo.Alt == "" || photo.Caption == "" {
				t.Fatalf("gallery photo %q has no alt text or caption", photo.URL)
			}
			if photo.Thumb == "" || photo.GridURL() != photo.Thumb {
				t.Fatalf("gallery photo %q has no grid derivative", photo.URL)
			}
			if seen[photo.URL] {
				t.Fatalf("gallery repeats %q", photo.URL)
			}
			seen[photo.URL] = true
		}
	}

	for _, photo := range event.Photos {
		if !seen[photo.URL] {
			t.Fatalf("featured photo %q is not part of the gallery", photo.URL)
		}
	}
}

func TestWinningPercentageUsesHalfPointForTie(t *testing.T) {
	player := Player{SinglesWins: 1, SinglesLosses: 1, SinglesTies: 1}
	if got, want := player.WinningPercentage(), 50; got != want {
		t.Fatalf("WinningPercentage() = %d, want %d", got, want)
	}

	player = Player{SinglesWins: 2, SinglesLosses: 1}
	if got, want := player.WinningPercentage(), 67; got != want {
		t.Fatalf("WinningPercentage() = %d, want %d", got, want)
	}
}

func TestLoadReturnsIndependentSlices(t *testing.T) {
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	first.Players[0].Name = "changed"
	first.Events[0].Winner = "changed"
	if second.Players[0].Name == "changed" || second.Events[0].Winner == "changed" {
		t.Fatal("Load returned shared mutable snapshot data")
	}
}
