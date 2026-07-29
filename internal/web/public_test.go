package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/legacy"
)

type fakeCompetitionReader struct {
	snapshot competitionpg.PublicCompetitionSnapshot
	err      error
}

func (f fakeCompetitionReader) PublicCompetition(context.Context) (competitionpg.PublicCompetitionSnapshot, error) {
	return f.snapshot, f.err
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func TestPublicPages(t *testing.T) {
	handler := newTestHandler(t)
	tests := []struct {
		path string
		want []string
	}{
		{path: "/", want: []string{"<h1>Cabot Cup</h1>", "Legacy snapshot through 2024", "/assets/site.css"}},
		{path: "/history", want: []string{"<h1>Cup history</h1>", "2019 Cabot Cup", "2024 Cabot Cup", "2025 Cabot Cup", "Results not yet entered"}},
		{path: "/history/2022", want: []string{"<h1>2022 Cabot Cup</h1>", "Turtles", "Fox Harb&#39;r Resort", "historical editorial content"}},
		{path: "/history/2025", want: []string{"<h1>2025 Cabot Cup</h1>", "Archive in progress", "No match results have been inferred", "Match results &amp; statistics", "Awaiting verified scorecards"}},
		{path: "/history/2026", want: []string{"<h1>2026 Cabot Cup</h1>", "Bears", "Flamingos", "26 - 10", "The Links at Crowbush Cove", "Dundarave Golf Course", "Brudenell River Golf Course", "/history/2026/photos", "See all 38 photos"}},
		{path: "/history/2026/photos", want: []string{"<h1>2026 photos</h1>", "All 38 photographs", "/2026/thumb/20260728-Z52_1275.jpg", "The Cabot Cup", "Brudenell River Golf Course", "/2026/original/20260728-Z52_1275.jpg", "Download original"}},
		{path: "/players", want: []string{"<h1>Players</h1>", "Portrait of Alex", "Portrait of Wally", "cup records from the former public site"}},
		{path: "/stats", want: []string{"<h1>Statistics</h1>", "Player-match entries", ">1</td>"}},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusOK, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			for _, want := range test.want {
				if !strings.Contains(response.Body.String(), want) {
					t.Errorf("body does not contain %q", want)
				}
			}
			for _, forbidden := range []string{"cdn.jsdelivr.net", "unpkg.com", "bootstrap.min.css", "jquery"} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Errorf("body contains runtime dependency %q", forbidden)
				}
			}
		})
	}
}

func TestPlayerSorts(t *testing.T) {
	handler := newTestHandler(t)
	tests := []struct {
		query       string
		firstPlayer string
	}{
		{query: "name", firstPlayer: "Alex"},
		{query: "cups", firstPlayer: "Alex"},
		{query: "record", firstPlayer: "Wally"},
		{query: "invalid", firstPlayer: "Alex"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/players?sort="+test.query, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			firstCard := strings.Index(body, `<article class="player-card"`)
			firstName := strings.Index(body, ">"+test.firstPlayer+"</h2>")
			if firstCard == -1 || firstName < firstCard {
				t.Fatalf("first player is not %q", test.firstPlayer)
			}
		})
	}
}

func TestVerifiedCompetitionHistoryAndCareerStats(t *testing.T) {
	snapshot := competitionpg.PublicCompetitionSnapshot{
		Seasons: []competitionpg.PublicSeasonRow{{
			EventID: "event", Name: "Cabot Cup", Year: 2026, Venue: "Cabot Links", VerifiedCount: 1,
			Teams:   []competitionpg.PublicTeamStandingRow{{TeamName: "Bears", Played: 1, Wins: 1, Points: "1.00"}},
			Matches: []competitionpg.PublicMatchRow{{Number: 1, Format: "singles", Side1Team: "Bears", Side1Players: "Dan Guns", Side2Team: "Flamingos", Side2Players: "Alex", Outcome: "side_1", Score: "3 & 2", VerificationMethod: "admin_override", VerifiedAt: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}},
			Players: []competitionpg.PublicPlayerStatRow{{PlayerName: "Dan Guns", Played: 1, Wins: 1, Points: "1.00", SinglesPlayed: 1, SinglesWins: 1, SinglesPoints: "1.00", TeamPoints: "0.00"}},
		}},
		Career: []competitionpg.PublicPlayerStatRow{{PlayerName: "Dan Guns", Played: 1, Wins: 1, Points: "1.00", SinglesPlayed: 1, SinglesPoints: "1.00", TeamPoints: "0.00"}},
	}
	handler, err := NewWithCompetition(fakeCompetitionReader{snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		want []string
	}{
		{path: "/history", want: []string{"Verified match history", "2026", "1 verified match"}},
		{path: "/history/2026", want: []string{"Authoritative competition record", "Dan Guns", "3 &amp; 2", "Admin verified", "Match record", "not the cup score"}},
		{path: "/stats", want: []string{"Verified match record", "Dan Guns", "Season records:", "/history/2026", "Every cup, one table"}},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%q", test.path, rec.Code, rec.Body.String())
		}
		for _, want := range test.want {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("GET %s missing %q", test.path, want)
			}
		}
	}
}

func TestVerifiedCompetitionReadFailureIsNotSilentlyStale(t *testing.T) {
	handler, err := NewWithCompetition(fakeCompetitionReader{err: errors.New("database unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Unable to load verified competition records") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestNotFoundAndMethodHandling(t *testing.T) {
	handler := newTestHandler(t)
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodGet, path: "/history/2018", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/history/2025/photos", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/history/2018/photos", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/history/nineteen/photos", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/missing", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/players", status: http.StatusMethodNotAllowed},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.status)
		}
	}
}

func TestAssetsAreEmbeddedAndProtected(t *testing.T) {
	handler := newTestHandler(t)
	for _, path := range []string{"/assets/site.css", "/assets/site.js", "/assets/players/alex_image.jpeg"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result := response.Result()
		defer result.Body.Close()
		body, err := io.ReadAll(result.Body)
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("GET %s status = %d, bytes = %d", path, result.StatusCode, len(body))
		}
		if got := result.Header.Get("Cache-Control"); got != "public, max-age=3600" {
			t.Errorf("GET %s Cache-Control = %q", path, got)
		}
		if !strings.Contains(result.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("GET %s missing content security policy", path)
		}
		if path == "/assets/site.js" {
			script := string(body)
			for _, want := range []string{
				"selected.dataset.sideOneName",
				"selected.dataset.sideOnePlayers",
				"selected.dataset.sideTwoName",
				"selected.dataset.sideTwoPlayers",
			} {
				if !strings.Contains(script, want) {
					t.Errorf("GET %s does not contain %q", path, want)
				}
			}
			for _, stale := range []string{
				"selected.dataset.side1 ||",
				"selected.dataset.side1Players",
				"selected.dataset.side2 ||",
				"selected.dataset.side2Players",
			} {
				if strings.Contains(script, stale) {
					t.Errorf("GET %s contains stale numeric data attribute mapping %q", path, stale)
				}
			}
		}
	}
}

func TestBuildCareerCombinesArchiveWithVerifiedMatches(t *testing.T) {
	snapshot, err := legacy.Load()
	if err != nil {
		t.Fatal(err)
	}
	verified := competitionpg.PublicCompetitionSnapshot{
		Seasons: []competitionpg.PublicSeasonRow{{Year: 2026}},
		Career: []competitionpg.PublicPlayerStatRow{
			{PlayerName: "Alex", PlayerSlug: "alex", Played: 3, Wins: 2, Losses: 1},
			{PlayerName: "Bradford", PlayerSlug: "bradford", Played: 3, Wins: 3},
		},
	}
	rows, from, to := buildCareer(snapshot, verified)
	if len(rows) == 0 {
		t.Fatal("buildCareer returned no rows")
	}
	if from != 2019 || to != 2026 {
		t.Fatalf("career range = %d-%d, want 2019-2026", from, to)
	}

	byName := make(map[string]CareerRow, len(rows))
	for _, row := range rows {
		if _, duplicate := byName[row.Name]; duplicate {
			t.Fatalf("career lists %q twice", row.Name)
		}
		byName[row.Name] = row
	}

	// Alex is in both halves: the archive's 18 matches plus 2026's 3.
	alex := byName["Alex"]
	if alex.Played != 21 || alex.Wins != 13 || alex.Losses != 6 || alex.Ties != 2 {
		t.Fatalf("Alex combined = %+v, want 21 played / 13-6-2", alex)
	}
	if !alex.HasLegacy || !alex.HasVerified || alex.Sources() != "Archive + verified" {
		t.Fatalf("Alex sources = %q", alex.Sources())
	}

	// Bradford played his first cup in 2026 and has no archive row at all.
	bradford := byName["Bradford"]
	if bradford.Played != 3 || bradford.HasLegacy || bradford.Sources() != "Verified" {
		t.Fatalf("Bradford = %+v, want 3 played, verified only", bradford)
	}
	if bradford.Qualified() {
		t.Fatal("a three-match player must not be ranked")
	}

	// Qualified players sort ahead of everyone short of the threshold.
	lastQualified := -1
	for i, row := range rows {
		if row.Qualified() {
			lastQualified = i
		} else if lastQualified > i {
			t.Fatal("an unqualified player sorted above a qualified one")
		}
	}
	if !rows[0].Qualified() {
		t.Fatalf("leader %q is not qualified", rows[0].Name)
	}
}

func TestBuildCareerRefusesToDoubleCountAnImportedYear(t *testing.T) {
	snapshot, err := legacy.Load()
	if err != nil {
		t.Fatal(err)
	}
	// 2024 is inside the legacy aggregate, so adding verified 2024 matches to it
	// would count that cup twice. The combined view must be withheld instead.
	verified := competitionpg.PublicCompetitionSnapshot{
		Seasons: []competitionpg.PublicSeasonRow{{Year: 2026}, {Year: legacy.CutoffYear}},
		Career:  []competitionpg.PublicPlayerStatRow{{PlayerName: "Alex", PlayerSlug: "alex", Played: 3, Wins: 3}},
	}
	if rows, _, _ := buildCareer(snapshot, verified); rows != nil {
		t.Fatalf("buildCareer combined an imported year: %d rows", len(rows))
	}
}

func TestStatsHeadlineLeaderMustBeQualified(t *testing.T) {
	handler := newTestHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	body := rec.Body.String()
	// Wally tops the archive on rate with two matches played. The headline must
	// name a player with a real sample instead.
	if strings.Contains(body, "Wally &middot; 100%") {
		t.Error("headline leader is a two-match player")
	}
	if !strings.Contains(body, "Walker &middot; 79%") {
		t.Error("headline leader is not the top qualified player")
	}
}

func TestDebutantPortraitsResolveBySlug(t *testing.T) {
	for _, test := range []struct{ slug, want string }{
		{"bradford", "/assets/players/bradford.jpg"},
		{"jb", "/assets/players/jb.jpg"},
		{"albert", "/assets/players/albert.jpg"},
		{"colin-d", "/assets/players/colin-d.jpg"},
		// The admin roster appends a uniqueness suffix, which production slugs
		// carry and the portrait filenames do not.
		{"bradford-f243f7ac", "/assets/players/bradford.jpg"},
		{"jb-f04ebdd2", "/assets/players/jb.jpg"},
		{"albert-6feb555a", "/assets/players/albert.jpg"},
		{"colin-d-29782e13", "/assets/players/colin-d.jpg"},
		// A legacy slug must never be truncated looking for a suffix.
		{"sammy-sosa", "/assets/players/empty_profile.jpeg"},
		{"nobody-has-this-portrait", "/assets/players/empty_profile.jpeg"},
		{"", "/assets/players/empty_profile.jpeg"},
	} {
		if got := (CareerRow{Slug: test.slug}).Image(); got != test.want {
			t.Errorf("Image(%q) = %q, want %q", test.slug, got, test.want)
		}
	}
}

func TestPlayersPageShowsVerifiedOnlyDebutants(t *testing.T) {
	snapshot := competitionpg.PublicCompetitionSnapshot{
		Seasons: []competitionpg.PublicSeasonRow{{
			EventID: "e", Name: "Cabot Cup", Year: 2026, VerifiedCount: 3,
			Players: []competitionpg.PublicPlayerStatRow{
				{PlayerName: "Bradford", PlayerSlug: "bradford", Played: 3, Wins: 3},
				{PlayerName: "Alex", PlayerSlug: "alex", Played: 3, Wins: 2, Losses: 1},
			},
		}},
		Career: []competitionpg.PublicPlayerStatRow{
			{PlayerName: "Bradford", PlayerSlug: "bradford", Played: 3, Wins: 3},
			{PlayerName: "Alex", PlayerSlug: "alex", Played: 3, Wins: 2, Losses: 1},
		},
	}
	handler, err := NewWithCompetition(fakeCompetitionReader{snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/players", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"First cup after the archive",
		`id="bradford"`,
		"/assets/players/bradford.jpg",
		"Portrait of Bradford",
		"Debut 2026",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("players page missing %q", want)
		}
	}
	// Alex already has a legacy profile card and must not be duplicated below it.
	if strings.Count(body, `id="alex"`) != 1 {
		t.Errorf("Alex appears %d times, want 1", strings.Count(body, `id="alex"`))
	}
}
