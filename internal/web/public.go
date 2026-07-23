// Package web provides the unauthenticated Cabot Cup HTTP experience.
package web

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strconv"

	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/legacy"
	publicassets "github.com/dgunzy/go-book/web"
)

const mediaHost = "https://d18fc2989jrcic.cloudfront.net"

type Handler struct {
	mux         *http.ServeMux
	templates   map[string]*template.Template
	snapshot    legacy.Snapshot
	competition CompetitionReader
}

// CompetitionReader supplies authoritative verified match history. It is
// optional so the static public archive remains independently testable.
type CompetitionReader interface {
	PublicCompetition(context.Context) (competitionpg.PublicCompetitionSnapshot, error)
}

type pageData struct {
	Title           string
	Description     string
	Current         string
	SnapshotLabel   string
	SnapshotNote    string
	Players         []legacy.Player
	Events          []legacy.Event
	Event           *legacy.Event
	Sort            string
	TotalPlayers    int
	TotalEvents     int
	CupAppearances  int
	MatchEntries    int
	Leader          *legacy.Player
	VerifiedSeasons []competitionpg.PublicSeasonRow
	VerifiedSeason  *competitionpg.PublicSeasonRow
	VerifiedCareer  []competitionpg.PublicPlayerStatRow
}

// New builds an independent handler for all public routes and assets.
func New() (*Handler, error) {
	return NewWithCompetition(nil)
}

// NewWithCompetition builds the public handler with authoritative verified
// match and statistics read models in addition to the legacy snapshot.
func NewWithCompetition(reader CompetitionReader) (*Handler, error) {
	snapshot, err := legacy.Load()
	if err != nil {
		return nil, err
	}
	templates, err := parseTemplates()
	if err != nil {
		return nil, err
	}

	h := &Handler{
		mux:         http.NewServeMux(),
		templates:   templates,
		snapshot:    snapshot,
		competition: reader,
	}
	h.routes()
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.securityHeaders(w)
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() {
	staticFS, err := fs.Sub(publicassets.Files, "static")
	if err != nil {
		panic(fmt.Sprintf("load public static assets: %v", err))
	}
	playerFS, err := fs.Sub(publicassets.Files, "players")
	if err != nil {
		panic(fmt.Sprintf("load player images: %v", err))
	}

	h.mux.Handle("GET /assets/players/", cacheAssets(http.StripPrefix("/assets/players/", http.FileServer(http.FS(playerFS)))))
	h.mux.Handle("GET /assets/", cacheAssets(http.StripPrefix("/assets/", http.FileServer(http.FS(staticFS)))))
	h.mux.HandleFunc("GET /history/{year}", h.historyDetail)
	h.mux.HandleFunc("GET /history", h.history)
	h.mux.HandleFunc("GET /players", h.players)
	h.mux.HandleFunc("GET /stats", h.stats)
	h.mux.HandleFunc("GET /", h.home)
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.notFound(w)
		return
	}
	data := h.baseData("Cabot Cup", "History, player records, and photographs from the Cabot Cup.", "home")
	h.render(w, "home", data)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	data := h.baseData("Cup history", "Explore the Cabot Cup archive from 2019 through the 2025 placeholder.", "history")
	verified, err := h.verifiedCompetition(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	data.VerifiedSeasons = verified.Seasons
	h.render(w, "history", data)
}

func (h *Handler) historyDetail(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil {
		h.notFound(w)
		return
	}
	verified, readErr := h.verifiedCompetition(r.Context())
	if readErr != nil {
		h.internalError(w)
		return
	}
	var verifiedSeason *competitionpg.PublicSeasonRow
	for i := range verified.Seasons {
		if verified.Seasons[i].Year == year {
			verifiedSeason = &verified.Seasons[i]
			break
		}
	}
	for i := range h.snapshot.Events {
		if h.snapshot.Events[i].Year == year {
			data := h.baseData(fmt.Sprintf("%d Cabot Cup", year), fmt.Sprintf("Story and photographs from the %d Cabot Cup.", year), "history")
			data.Event = &h.snapshot.Events[i]
			data.VerifiedSeason = verifiedSeason
			h.render(w, "event", data)
			return
		}
	}
	if verifiedSeason != nil {
		data := h.baseData(fmt.Sprintf("%d Cabot Cup", year), fmt.Sprintf("Verified match history from the %d Cabot Cup.", year), "history")
		data.VerifiedSeason = verifiedSeason
		data.VerifiedSeasons = verified.Seasons
		h.render(w, "verified_event", data)
		return
	}
	h.notFound(w)
}

func (h *Handler) players(w http.ResponseWriter, r *http.Request) {
	players := append([]legacy.Player(nil), h.snapshot.Players...)
	sortBy := r.URL.Query().Get("sort")
	switch sortBy {
	case "cups":
		sort.SliceStable(players, func(i, j int) bool {
			if players[i].CupsPlayed() == players[j].CupsPlayed() {
				return players[i].Name < players[j].Name
			}
			return players[i].CupsPlayed() > players[j].CupsPlayed()
		})
	case "record":
		sort.SliceStable(players, func(i, j int) bool {
			if players[i].WinningPercentage() == players[j].WinningPercentage() {
				return players[i].Name < players[j].Name
			}
			return players[i].WinningPercentage() > players[j].WinningPercentage()
		})
	default:
		sortBy = "name"
	}

	data := h.baseData("Players", "Legacy player profiles and aggregate Cabot Cup records.", "players")
	data.Players = players
	data.Sort = sortBy
	h.render(w, "players", data)
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	players := append([]legacy.Player(nil), h.snapshot.Players...)
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].WinningPercentage() == players[j].WinningPercentage() {
			return players[i].MatchesPlayed() > players[j].MatchesPlayed()
		}
		return players[i].WinningPercentage() > players[j].WinningPercentage()
	})

	data := h.baseData("Statistics", "Aggregate records from the legacy Cabot Cup dataset.", "stats")
	verified, err := h.verifiedCompetition(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	data.VerifiedSeasons = verified.Seasons
	data.VerifiedCareer = verified.Career
	data.Players = players
	for _, player := range players {
		data.CupAppearances += player.CupsPlayed()
		data.MatchEntries += player.MatchesPlayed()
	}
	if len(players) > 0 {
		data.Leader = &players[0]
	}
	h.render(w, "stats", data)
}

func (h *Handler) verifiedCompetition(ctx context.Context) (competitionpg.PublicCompetitionSnapshot, error) {
	if h.competition == nil {
		return competitionpg.PublicCompetitionSnapshot{}, nil
	}
	return h.competition.PublicCompetition(ctx)
}

func (h *Handler) baseData(title, description, current string) pageData {
	return pageData{
		Title: title, Description: description, Current: current,
		SnapshotLabel: h.snapshot.Label, SnapshotNote: h.snapshot.Note,
		Players: h.snapshot.Players, Events: h.snapshot.Events,
		TotalPlayers: len(h.snapshot.Players), TotalEvents: len(h.snapshot.Events),
	}
}

func (h *Handler) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := h.templates[name].ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "Unable to render this page", http.StatusInternalServerError)
	}
}

func (h *Handler) internalError(w http.ResponseWriter) {
	http.Error(w, "Unable to load verified competition records", http.StatusInternalServerError)
}

func (h *Handler) notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	data := h.baseData("Page not found", "The requested Cabot Cup page could not be found.", "")
	_ = h.templates["not_found"].ExecuteTemplate(w, "layout", data)
}

func (h *Handler) securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' "+mediaHost+"; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func cacheAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func parseTemplates() (map[string]*template.Template, error) {
	pages := []string{"home", "history", "event", "verified_event", "players", "stats", "not_found"}
	result := make(map[string]*template.Template, len(pages))
	functions := template.FuncMap{
		"add1": func(value int) int { return value + 1 },
	}
	for _, page := range pages {
		tmpl, err := template.New("layout").Funcs(functions).ParseFS(publicassets.Files,
			"templates/layout.gohtml", "templates/verified_records.gohtml", "templates/"+page+".gohtml")
		if err != nil {
			return nil, fmt.Errorf("parse public %s template: %w", page, err)
		}
		result[page] = tmpl
	}
	return result, nil
}
