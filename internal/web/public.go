// Package web provides the unauthenticated Cabot Cup HTTP experience.
package web

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
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
	Career          []CareerRow
	CareerFrom      int
	CareerTo        int
	QualifyingLimit int
	Debutants       []CareerRow
}

// CareerRow is one player's whole record: the legacy aggregate through the
// import cutoff plus every verified match since, added together. The two halves
// cover disjoint years, which is what makes the sum meaningful rather than a
// double count — buildCareer refuses to combine them if that ever stops holding.
type CareerRow struct {
	Name   string
	Slug   string
	Played int
	Wins   int
	Losses int
	Ties   int

	LegacyPlayed   int
	VerifiedPlayed int
	HasLegacy      bool
	HasVerified    bool

	CupWins   int
	CupLosses int

	firstVerifiedYear int
}

// PointsPercent scores a tie as half a win and rounds to a whole percent, the
// convention the legacy site used and the archive still displays.
func (c CareerRow) PointsPercent() int {
	if c.Played == 0 {
		return 0
	}
	pointsTimesTwo := 2*c.Wins + c.Ties
	return (pointsTimesTwo*50 + c.Played/2) / c.Played
}

// Qualified reports whether the player has played enough matches for a rate to
// mean anything. An unqualified player still appears, but is not ranked above
// the field on the strength of two matches.
func (c CareerRow) Qualified() bool { return c.Played >= QualifyingMatches }

// Sources describes which halves of the record book a player appears in.
func (c CareerRow) Sources() string {
	switch {
	case c.HasLegacy && c.HasVerified:
		return "Archive + verified"
	case c.HasVerified:
		return "Verified"
	default:
		return "Archive"
	}
}

// QualifyingMatches is the minimum career matches required before a win rate is
// treated as ranked. Two-match players otherwise sit permanently at the top.
const QualifyingMatches = 6

// adminSlugSuffix matches the uniqueness suffix the admin roster appends when it
// creates a player, so "bradford-f243f7ac" can find the portrait filed under
// "bradford". Legacy imported slugs carry no suffix and are unaffected.
var adminSlugSuffix = regexp.MustCompile(`-[0-9a-f]{8}$`)

// Image resolves a portrait by slug convention. A player who first appears in
// the verified record has no legacy row to carry an image path, so the file is
// looked up as players/<slug>.jpg. The full slug wins when a file matches it
// exactly; otherwise the admin suffix is stripped and retried. A player with no
// portrait at all gets the blank profile rather than a broken image.
func (c CareerRow) Image() string {
	for _, slug := range []string{c.Slug, adminSlugSuffix.ReplaceAllString(c.Slug, "")} {
		if slug == "" {
			continue
		}
		if _, err := fs.Stat(publicassets.Files, "players/"+slug+".jpg"); err == nil {
			return "/assets/players/" + slug + ".jpg"
		}
	}
	return "/assets/players/empty_profile.jpeg"
}

// DebutYear reports the first year this player appears in the verified record.
// It is only meaningful for players with no legacy history.
func (c CareerRow) DebutYear() int { return c.firstVerifiedYear }

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
	h.mux.HandleFunc("GET /history/{year}/photos", h.historyPhotos)
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

func (h *Handler) historyPhotos(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil {
		h.notFound(w)
		return
	}
	for i := range h.snapshot.Events {
		if h.snapshot.Events[i].Year != year {
			continue
		}
		if !h.snapshot.Events[i].HasGallery() {
			h.notFound(w)
			return
		}
		data := h.baseData(
			fmt.Sprintf("%d photos", year),
			fmt.Sprintf("Every photograph from the %d Cabot Cup.", year),
			"history",
		)
		data.Event = &h.snapshot.Events[i]
		h.render(w, "photos", data)
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

	verified, err := h.verifiedCompetition(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}

	data := h.baseData("Players", "Player profiles and Cabot Cup records, from the legacy archive and the verified match record.", "players")
	data.Players = players
	data.Sort = sortBy
	// Players whose first cup came after the legacy import have no aggregate row,
	// so they would otherwise appear in the match history and nowhere else.
	career, _, _ := buildCareer(h.snapshot, verified)
	for _, row := range career {
		if row.HasVerified && !row.HasLegacy {
			data.Debutants = append(data.Debutants, row)
		}
	}
	sort.SliceStable(data.Debutants, func(i, j int) bool {
		return data.Debutants[i].Name < data.Debutants[j].Name
	})
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
	data.Career, data.CareerFrom, data.CareerTo = buildCareer(h.snapshot, verified)
	data.QualifyingLimit = QualifyingMatches
	for _, player := range players {
		data.CupAppearances += player.CupsPlayed()
		data.MatchEntries += player.MatchesPlayed()
	}
	// The headline leader must have played enough golf to deserve it. players is
	// already sorted by rate, so the first qualified player is the leader.
	for i := range players {
		if players[i].MatchesPlayed() >= QualifyingMatches {
			data.Leader = &players[i]
			break
		}
	}
	h.render(w, "stats", data)
}

// buildCareer merges the legacy aggregate with verified match records into one
// career table. The legacy snapshot stops at legacy.CutoffYear and verified
// seasons begin after it, so the two never describe the same match. If a
// verified season ever falls on or before the cutoff the totals would count that
// cup twice, so the combined view is withheld rather than shown wrong.
func buildCareer(snapshot legacy.Snapshot, verified competitionpg.PublicCompetitionSnapshot) ([]CareerRow, int, int) {
	for _, season := range verified.Seasons {
		if season.Year <= legacy.CutoffYear {
			return nil, 0, 0
		}
	}

	players := snapshot.Players
	rows := make([]CareerRow, 0, len(players)+len(verified.Career))
	index := make(map[string]int, len(players))
	for _, player := range players {
		index[player.Slug] = len(rows)
		rows = append(rows, CareerRow{
			Name: player.Name, Slug: player.Slug,
			Played: player.MatchesPlayed(), Wins: player.MatchWins(),
			Losses: player.MatchLosses(), Ties: player.MatchTies(),
			LegacyPlayed: player.MatchesPlayed(), HasLegacy: true,
			CupWins: player.TeamWins, CupLosses: player.TeamLosses,
		})
	}

	for _, stat := range verified.Career {
		position, known := index[stat.PlayerSlug]
		if !known {
			index[stat.PlayerSlug] = len(rows)
			rows = append(rows, CareerRow{Name: stat.PlayerName, Slug: stat.PlayerSlug})
			position = len(rows) - 1
		}
		row := &rows[position]
		row.Played += stat.Played
		row.Wins += stat.Wins
		row.Losses += stat.Losses
		row.Ties += stat.Ties
		row.VerifiedPlayed += stat.Played
		row.HasVerified = true
	}

	// Career totals are aggregated across seasons, so a debut year has to come
	// from the per-season rows.
	for _, season := range verified.Seasons {
		for _, stat := range season.Players {
			position, known := index[stat.PlayerSlug]
			if !known {
				continue
			}
			if year := rows[position].firstVerifiedYear; year == 0 || season.Year < year {
				rows[position].firstVerifiedYear = season.Year
			}
		}
	}

	// Qualified players rank first: a 100% record from two matches should not
	// out-rank a decade of golf.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Qualified() != rows[j].Qualified() {
			return rows[i].Qualified()
		}
		if rows[i].PointsPercent() != rows[j].PointsPercent() {
			return rows[i].PointsPercent() > rows[j].PointsPercent()
		}
		if rows[i].Played != rows[j].Played {
			return rows[i].Played > rows[j].Played
		}
		return rows[i].Name < rows[j].Name
	})

	from, to := 0, legacy.CutoffYear
	for _, event := range snapshot.Events {
		if from == 0 || event.Year < from {
			from = event.Year
		}
	}
	for _, season := range verified.Seasons {
		if season.Year > to {
			to = season.Year
		}
	}
	return rows, from, to
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
	pages := []string{"home", "history", "event", "photos", "verified_event", "players", "stats", "not_found"}
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
