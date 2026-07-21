package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
		{path: "/players", want: []string{"<h1>Players</h1>", "Portrait of Alex", "Portrait of Wally", "aggregate Cabot Cup records"}},
		{path: "/stats", want: []string{"<h1>Statistics</h1>", "Player-match entries", "Wally &middot; 100%", ">1</td>"}},
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

func TestNotFoundAndMethodHandling(t *testing.T) {
	handler := newTestHandler(t)
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodGet, path: "/history/2018", status: http.StatusNotFound},
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
	for _, path := range []string{"/assets/site.css", "/assets/players/alex_image.jpeg"} {
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
	}
}
