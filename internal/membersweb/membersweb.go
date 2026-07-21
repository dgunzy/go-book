// Package membersweb provides the authenticated member-management UI: admins
// view members and invite new members via one-time links; owners additionally
// invite admins, change roles, and revoke members. Route handlers validate
// input and call the identitypg membership store; authorization is enforced
// both here and in the store.
package membersweb

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
	"time"

	"github.com/dgunzy/go-book/internal/competitionpg"
	"github.com/dgunzy/go-book/internal/identity"
	"github.com/dgunzy/go-book/internal/identitypg"
	"github.com/dgunzy/go-book/internal/privateweb"
	publicassets "github.com/dgunzy/go-book/web"
)

const (
	maxFormBytes     = 16 << 10
	maxReasonLen     = 500
	defaultInviteTTL = 7 * 24 * time.Hour
	redirectMembers  = "/admin/members"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(value string) bool { return uuidPattern.MatchString(value) }

// SessionReader is satisfied by authweb's session adapter.
type SessionReader interface {
	CurrentSession(*http.Request) (privateweb.Session, error)
}

// MemberStore is the membership surface this handler needs. Every mutating
// method takes the acting user; the store re-checks owner authorization.
type MemberStore interface {
	ListMembers(context.Context) ([]identitypg.MemberRow, error)
	ListPendingInvitations(context.Context) ([]identitypg.InvitationRow, error)
	IssueInvitation(ctx context.Context, actorUserID, role, intendedEmail string, ttl time.Duration) (string, error)
	RevokeInvitation(ctx context.Context, actorUserID, invitationID string) error
	ChangeMemberRole(ctx context.Context, actorUserID, targetUserID, newRole, reason string) error
	RevokeMember(ctx context.Context, actorUserID, targetUserID, reason string) error
	SetAutoApproveLimit(ctx context.Context, actorUserID, targetUserID string, cents *int64) error
	SetCreditLimit(ctx context.Context, actorUserID, targetUserID string, cents int64) error
}

// PlayerLinker maps onboarded members to historical/competition players so a
// login identity is associated with the correct event-derived statistics.
type PlayerLinker interface {
	ListPlayerLinks(ctx context.Context, actorUserID string) ([]competitionpg.PlayerLink, error)
	LinkPlayerToUser(ctx context.Context, actorUserID, playerID, targetUserID string) error
	UnlinkPlayer(ctx context.Context, actorUserID, playerID, targetUserID string) error
}

var (
	_ MemberStore  = identitypg.Store{}
	_ PlayerLinker = competitionpg.Store{}
)

type Dependencies struct {
	Sessions      SessionReader
	Members       MemberStore
	Players       PlayerLinker
	PublicBaseURL string
}

type Handler struct {
	mux       *http.ServeMux
	deps      Dependencies
	templates map[string]*template.Template
	baseURL   string
}

func New(deps Dependencies) (*Handler, error) {
	if deps.Sessions == nil || deps.Members == nil || deps.Players == nil {
		return nil, errors.New("members web dependencies must all be configured")
	}
	base := strings.TrimRight(strings.TrimSpace(deps.PublicBaseURL), "/")
	if base == "" {
		return nil, errors.New("members web requires a public base URL for invite links")
	}
	templates, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	handler := &Handler{mux: http.NewServeMux(), deps: deps, templates: templates, baseURL: base}
	handler.routes()
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /admin/members", h.list)
	h.mux.HandleFunc("POST /admin/members/invite", h.invite)
	h.mux.HandleFunc("POST /admin/members/invite/{id}/revoke", h.revokeInvite)
	h.mux.HandleFunc("POST /admin/members/{id}/role", h.changeRole)
	h.mux.HandleFunc("POST /admin/members/{id}/revoke", h.revokeMember)
	h.mux.HandleFunc("POST /admin/members/{id}/limit", h.setLimit)
	h.mux.HandleFunc("POST /admin/members/{id}/credit", h.setCredit)
	h.mux.HandleFunc("POST /admin/members/{id}/link-player", h.linkPlayer)
	h.mux.HandleFunc("POST /admin/members/{id}/unlink-player", h.unlinkPlayer)
}

func (h *Handler) linkPlayer(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	targetID := r.PathValue("id")
	playerID := r.PostForm.Get("player_id")
	if !isUUID(targetID) || !isUUID(playerID) {
		h.renderList(w, r, session, pageData{FormError: "Pick a member and a player to link."})
		return
	}
	if err := h.deps.Players.LinkPlayerToUser(r.Context(), session.UserID, playerID, targetID); err != nil {
		h.renderList(w, r, session, pageData{FormError: linkErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

func (h *Handler) unlinkPlayer(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	targetID := r.PathValue("id")
	playerID := r.PostForm.Get("player_id")
	if !isUUID(targetID) || !isUUID(playerID) {
		h.renderList(w, r, session, pageData{FormError: "That member or player was not found."})
		return
	}
	if err := h.deps.Players.UnlinkPlayer(r.Context(), session.UserID, playerID, targetID); err != nil {
		h.renderList(w, r, session, pageData{FormError: linkErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

func linkErrorMessage(err error) string {
	switch {
	case errors.Is(err, identity.ErrUnauthorized):
		return "You are not authorized to map players."
	case errors.Is(err, competitionpg.ErrPlayerNotFound):
		return "That historical player was not found or is no longer active."
	case errors.Is(err, competitionpg.ErrMemberNotFound):
		return "That active member was not found."
	case errors.Is(err, competitionpg.ErrPlayerAlreadyLinked):
		return "That historical player is already linked to another member."
	case errors.Is(err, competitionpg.ErrMemberAlreadyLinked):
		return "That member is already linked to another historical player."
	case errors.Is(err, competitionpg.ErrPlayerLinkMismatch):
		return "That historical player is no longer linked to this member. Refresh and try again."
	default:
		return "The player mapping could not be updated."
	}
}

var stakePattern = regexp.MustCompile(`^\$?([0-9]{1,7})(?:\.([0-9]{1,2}))?$`)

// parseLimitCents converts a dollars-and-cents limit to integer cents. A blank
// value returns nil, meaning "clear the override and use the book default".
func parseLimitCents(value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	match := stakePattern.FindStringSubmatch(value)
	if match == nil {
		return nil, errors.New("limit must be a dollar amount")
	}
	dollars, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return nil, err
	}
	var cents int64
	if match[2] != "" {
		if cents, err = strconv.ParseInt(match[2], 10, 64); err != nil {
			return nil, err
		}
		if len(match[2]) == 1 {
			cents *= 10
		}
	}
	total := dollars*100 + cents
	return &total, nil
}

func (h *Handler) setCredit(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	targetID := r.PathValue("id")
	if !isUUID(targetID) {
		h.renderList(w, r, session, pageData{FormError: "That member was not found."})
		return
	}
	cents, err := parseLimitCents(r.PostForm.Get("credit"))
	if err != nil || cents == nil {
		h.renderList(w, r, session, pageData{FormError: "Enter the credit limit as a dollar amount, for example 1000."})
		return
	}
	if err := h.deps.Members.SetCreditLimit(r.Context(), session.UserID, targetID, *cents); err != nil {
		h.renderList(w, r, session, pageData{FormError: storeErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

func (h *Handler) setLimit(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	targetID := r.PathValue("id")
	if !isUUID(targetID) {
		h.renderList(w, r, session, pageData{FormError: "That member was not found."})
		return
	}
	cents, err := parseLimitCents(r.PostForm.Get("limit"))
	if err != nil {
		h.renderList(w, r, session, pageData{FormError: "Enter the auto-approve limit as a dollar amount, or leave it blank to use the default."})
		return
	}
	if err := h.deps.Members.SetAutoApproveLimit(r.Context(), session.UserID, targetID, cents); err != nil {
		h.renderList(w, r, session, pageData{FormError: storeErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

type pageData struct {
	Title       string
	Current     string
	Session     privateweb.Session
	Members     []identitypg.MemberRow
	Invitations []identitypg.InvitationRow
	IsOwner     bool
	FormError   string
	Notice      string
	InviteLink  string
	// LinkedPlayerByUser maps a member's user ID to the player they are mapped
	// to; UnlinkedPlayers are the players still available to map.
	LinkedPlayerByUser map[string]competitionpg.PlayerLink
	UnlinkedPlayers    []competitionpg.PlayerLink
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	h.renderList(w, r, session, pageData{})
}

func (h *Handler) renderList(w http.ResponseWriter, r *http.Request, session privateweb.Session, extra pageData) {
	members, err := h.deps.Members.ListMembers(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	invitations, err := h.deps.Members.ListPendingInvitations(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	links, err := h.deps.Players.ListPlayerLinks(r.Context(), session.UserID)
	if err != nil {
		h.internalError(w)
		return
	}
	linkedByUser := make(map[string]competitionpg.PlayerLink)
	var unlinked []competitionpg.PlayerLink
	for _, link := range links {
		if link.LinkedUserID != "" {
			linkedByUser[link.LinkedUserID] = link
		} else {
			unlinked = append(unlinked, link)
		}
	}
	data := extra
	data.Title = "Members"
	data.Current = "admin-members"
	data.Session = session
	data.Members = members
	data.Invitations = invitations
	data.IsOwner = session.Role == privateweb.RoleOwner
	data.LinkedPlayerByUser = linkedByUser
	data.UnlinkedPlayers = unlinked
	status := http.StatusOK
	if data.FormError != "" {
		status = http.StatusBadRequest
	}
	h.renderStatus(w, status, data)
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	role := r.PostForm.Get("role")
	if role != "member" && role != "admin" {
		h.renderList(w, r, session, pageData{FormError: "Choose a role of member or admin."})
		return
	}
	if role == "admin" && session.Role != privateweb.RoleOwner {
		h.renderList(w, r, session, pageData{FormError: "Only an owner may invite an admin."})
		return
	}
	intendedEmail := strings.TrimSpace(r.PostForm.Get("email"))
	token, err := h.deps.Members.IssueInvitation(r.Context(), session.UserID, role, intendedEmail, defaultInviteTTL)
	if err != nil {
		h.renderList(w, r, session, pageData{FormError: storeErrorMessage(err)})
		return
	}
	link := h.baseURL + "/invite/" + token
	notice := fmt.Sprintf("Invite link created for a new %s. Copy it now — it is shown only once and expires in 7 days.", role)
	h.renderList(w, r, session, pageData{Notice: notice, InviteLink: link})
}

func (h *Handler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	invitationID := r.PathValue("id")
	if !isUUID(invitationID) {
		h.renderList(w, r, session, pageData{FormError: "That invitation was not found."})
		return
	}
	if err := h.deps.Members.RevokeInvitation(r.Context(), session.UserID, invitationID); err != nil {
		h.renderList(w, r, session, pageData{FormError: storeErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	targetID := r.PathValue("id")
	newRole := r.PostForm.Get("role")
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if !isUUID(targetID) || (newRole != "member" && newRole != "admin" && newRole != "owner") {
		h.renderList(w, r, session, pageData{FormError: "Pick a valid member and target role."})
		return
	}
	if reason == "" || len(reason) > maxReasonLen {
		h.renderList(w, r, session, pageData{FormError: "A reason is required to change a role."})
		return
	}
	if err := h.deps.Members.ChangeMemberRole(r.Context(), session.UserID, targetID, newRole, reason); err != nil {
		h.renderList(w, r, session, pageData{FormError: storeErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

func (h *Handler) revokeMember(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	targetID := r.PathValue("id")
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if !isUUID(targetID) {
		h.renderList(w, r, session, pageData{FormError: "That member was not found."})
		return
	}
	if reason == "" || len(reason) > maxReasonLen {
		h.renderList(w, r, session, pageData{FormError: "A reason is required to revoke a member."})
		return
	}
	if err := h.deps.Members.RevokeMember(r.Context(), session.UserID, targetID, reason); err != nil {
		h.renderList(w, r, session, pageData{FormError: storeErrorMessage(err)})
		return
	}
	http.Redirect(w, r, redirectMembers, http.StatusSeeOther)
}

// --- auth + helpers (mirrors bettingweb) ---

func (h *Handler) requireMember(w http.ResponseWriter, r *http.Request) (privateweb.Session, bool) {
	session, err := h.deps.Sessions.CurrentSession(r)
	if errors.Is(err, privateweb.ErrNoSession) {
		query := url.Values{"next": []string{r.URL.RequestURI()}}
		http.Redirect(w, r, (&url.URL{Path: "/login", RawQuery: query.Encode()}).String(), http.StatusSeeOther)
		return privateweb.Session{}, false
	}
	if err != nil {
		h.internalError(w)
		return privateweb.Session{}, false
	}
	if !session.Active || session.UserID == "" {
		h.forbidden(w, session)
		return privateweb.Session{}, false
	}
	return session, true
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (privateweb.Session, bool) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return privateweb.Session{}, false
	}
	if session.Role != privateweb.RoleAdmin && session.Role != privateweb.RoleOwner {
		h.forbidden(w, session)
		return privateweb.Session{}, false
	}
	return session, true
}

func (h *Handler) requireOwner(w http.ResponseWriter, r *http.Request) (privateweb.Session, bool) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return privateweb.Session{}, false
	}
	if session.Role != privateweb.RoleOwner {
		h.forbidden(w, session)
		return privateweb.Session{}, false
	}
	return session, true
}

func (h *Handler) checkedForm(w http.ResponseWriter, r *http.Request, session privateweb.Session) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		h.forbidden(w, session)
		return false
	}
	token := r.PostForm.Get("csrf_token")
	if token == "" || session.CSRFToken == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(session.CSRFToken)) != 1 {
		h.forbidden(w, session)
		return false
	}
	return true
}

func (h *Handler) forbidden(w http.ResponseWriter, session privateweb.Session) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = h.templates["forbidden"].ExecuteTemplate(w, "private_layout", pageData{Title: "Access denied", Session: session})
}

func (h *Handler) renderStatus(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.templates["members"].ExecuteTemplate(w, "private_layout", data)
}

func (h *Handler) internalError(w http.ResponseWriter) {
	http.Error(w, "Unable to load this page", http.StatusInternalServerError)
}

func storeErrorMessage(err error) string {
	if errors.Is(err, identity.ErrUnauthorized) {
		return "You are not authorized to perform this action."
	}
	return "The request could not be completed."
}

func parseTemplates() (map[string]*template.Template, error) {
	functions := template.FuncMap{
		"when": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.Format("Jan 2, 2006 15:04 MST")
		},
		"limitDollars": func(cents *int64) string {
			if cents == nil {
				return ""
			}
			return fmt.Sprintf("%d.%02d", *cents/100, *cents%100)
		},
		"creditDollars": func(cents int64) string {
			return fmt.Sprintf("%d.%02d", cents/100, cents%100)
		},
	}
	result := make(map[string]*template.Template, 2)
	for name, page := range map[string]string{
		"members":   "templates/admin_members.gohtml",
		"forbidden": "templates/private_forbidden.gohtml",
	} {
		tmpl, err := template.New("private_layout").Funcs(functions).ParseFS(
			publicassets.Files, "templates/private_layout.gohtml", page,
		)
		if err != nil {
			return nil, fmt.Errorf("parse members %s template: %w", name, err)
		}
		result[name] = tmpl
	}
	return result, nil
}
