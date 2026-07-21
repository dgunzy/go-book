// Package competitionweb provides the admin UI for the competition model:
// creating events, teams, and matches, and recording verified match results.
// Recording a result publishes MatchResultVerified, which the betting
// settlement consumer turns into settled markets — so this UI is the human
// entry point to the match-driven, event-based settlement flow.
package competitionweb

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/privateweb"
	publicassets "github.com/dgunzy/go-book/web"
)

const maxFormBytes = 16 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(v string) bool { return uuidPattern.MatchString(v) }

// SessionReader is satisfied by authweb's session adapter.
type SessionReader interface {
	CurrentSession(*http.Request) (privateweb.Session, error)
}

// CompetitionStore is the competition surface this handler needs.
type CompetitionStore interface {
	ListEvents(context.Context) ([]competitionpg.EventRow, error)
	CreateEvent(context.Context, competitionpg.CreateEventRequest) (string, error)
	CreateTeam(ctx context.Context, eventID, name, createdBy string) (string, error)
	CreateMatch(ctx context.Context, eventID, format, side1TeamID, side2TeamID, createdBy string) (competitionpg.MatchCreated, error)
	RecordAdminResult(context.Context, competitionpg.RecordResultRequest) (string, error)
}

var _ CompetitionStore = competitionpg.Store{}

type Dependencies struct {
	Sessions    SessionReader
	Competition CompetitionStore
}

type Handler struct {
	mux  *http.ServeMux
	deps Dependencies
	tmpl *template.Template
}

func New(deps Dependencies) (*Handler, error) {
	if deps.Sessions == nil || deps.Competition == nil {
		return nil, errors.New("competition web dependencies must be configured")
	}
	tmpl, err := template.New("private_layout").ParseFS(publicassets.Files,
		"templates/private_layout.gohtml", "templates/admin_matches.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse matches template: %w", err)
	}
	h := &Handler{mux: http.NewServeMux(), deps: deps, tmpl: tmpl}
	h.mux.HandleFunc("GET /admin/matches", h.list)
	h.mux.HandleFunc("POST /admin/events", h.createEvent)
	h.mux.HandleFunc("POST /admin/events/{id}/teams", h.createTeam)
	h.mux.HandleFunc("POST /admin/matches", h.createMatch)
	h.mux.HandleFunc("POST /admin/matches/{id}/result", h.recordResult)
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

type pageData struct {
	Title     string
	Current   string
	Session   privateweb.Session
	Events    []competitionpg.EventRow
	FormError string
	Notice    string
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	h.renderList(w, r, session, pageData{})
}

func (h *Handler) renderList(w http.ResponseWriter, r *http.Request, session privateweb.Session, extra pageData) {
	eventsList, err := h.deps.Competition.ListEvents(r.Context())
	if err != nil {
		http.Error(w, "Unable to load matches", http.StatusInternalServerError)
		return
	}
	extra.Title, extra.Current, extra.Session, extra.Events = "Matches", "admin-matches", session, eventsList
	status := http.StatusOK
	if extra.FormError != "" {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.tmpl.ExecuteTemplate(w, "private_layout", extra)
}

func (h *Handler) createEvent(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	year, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("season_year")))
	if err != nil {
		h.renderList(w, r, session, pageData{FormError: "Enter a valid season year."})
		return
	}
	if _, err := h.deps.Competition.CreateEvent(r.Context(), competitionpg.CreateEventRequest{
		Name: r.PostForm.Get("name"), Venue: r.PostForm.Get("venue"), SeasonYear: year, CreatedBy: session.UserID,
	}); err != nil {
		h.renderList(w, r, session, pageData{FormError: "Could not create event: " + err.Error()})
		return
	}
	http.Redirect(w, r, "/admin/matches", http.StatusSeeOther)
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	eventID := r.PathValue("id")
	if !isUUID(eventID) {
		h.renderList(w, r, session, pageData{FormError: "That event was not found."})
		return
	}
	if _, err := h.deps.Competition.CreateTeam(r.Context(), eventID, r.PostForm.Get("name"), session.UserID); err != nil {
		h.renderList(w, r, session, pageData{FormError: "Could not add team: " + err.Error()})
		return
	}
	http.Redirect(w, r, "/admin/matches", http.StatusSeeOther)
}

func (h *Handler) createMatch(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	eventID := r.PostForm.Get("event_id")
	side1 := r.PostForm.Get("side1_team_id")
	side2 := r.PostForm.Get("side2_team_id")
	format := r.PostForm.Get("format")
	if !isUUID(eventID) || !isUUID(side1) || !isUUID(side2) {
		h.renderList(w, r, session, pageData{FormError: "Pick an event and two teams."})
		return
	}
	created, err := h.deps.Competition.CreateMatch(r.Context(), eventID, format, side1, side2, session.UserID)
	if err != nil {
		h.renderList(w, r, session, pageData{FormError: "Could not create match: " + err.Error()})
		return
	}
	h.renderList(w, r, session, pageData{Notice: fmt.Sprintf(
		"Match created. To bet on it, create a Match market with Match ID %s and selections keyed side:%s and side:%s.",
		created.MatchID, created.Side1ID, created.Side2ID)})
}

func (h *Handler) recordResult(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	matchID := r.PathValue("id")
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if !isUUID(matchID) {
		h.renderList(w, r, session, pageData{FormError: "That match was not found."})
		return
	}
	if reason == "" {
		h.renderList(w, r, session, pageData{FormError: "A reason is required to record a result."})
		return
	}
	if _, err := h.deps.Competition.RecordAdminResult(r.Context(), competitionpg.RecordResultRequest{
		MatchID: matchID, Winner: r.PostForm.Get("winner"), Score: r.PostForm.Get("score"),
		ActorUserID: session.UserID, Reason: reason,
	}); err != nil {
		h.renderList(w, r, session, pageData{FormError: "Could not record result: " + err.Error()})
		return
	}
	h.renderList(w, r, session, pageData{Notice: "Result recorded. Any linked match markets will settle automatically."})
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (privateweb.Session, bool) {
	session, err := h.deps.Sessions.CurrentSession(r)
	if errors.Is(err, privateweb.ErrNoSession) {
		query := url.Values{"next": []string{r.URL.RequestURI()}}
		http.Redirect(w, r, (&url.URL{Path: "/login", RawQuery: query.Encode()}).String(), http.StatusSeeOther)
		return privateweb.Session{}, false
	}
	if err != nil {
		http.Error(w, "Unable to load session", http.StatusInternalServerError)
		return privateweb.Session{}, false
	}
	if !session.Active || (session.Role != privateweb.RoleAdmin && session.Role != privateweb.RoleOwner) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Access denied"))
		return privateweb.Session{}, false
	}
	return session, true
}

func (h *Handler) checkedForm(w http.ResponseWriter, r *http.Request, session privateweb.Session) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	token := r.PostForm.Get("csrf_token")
	if token == "" || session.CSRFToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(session.CSRFToken)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	return true
}
