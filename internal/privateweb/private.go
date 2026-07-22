// Package privateweb provides the authenticated, read-only member and admin UI.
package privateweb

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/webtime"
	publicassets "github.com/dgunzy/go-book/web"
)

var ErrNoSession = errors.New("no active session")

type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

type Session struct {
	UserID      string
	DisplayName string
	Role        Role
	Active      bool
	CSRFToken   string
	Acceptance  bool
}

type SessionReader interface {
	CurrentSession(*http.Request) (Session, error)
}

type DashboardReader interface {
	DashboardSummary(context.Context, string) (DashboardSummary, error)
}

type LedgerReader interface {
	LedgerRows(context.Context, string) ([]LedgerRow, error)
}

type WagerReader interface {
	WagerRows(context.Context, string) ([]WagerRow, error)
}

type ReconciliationReader interface {
	ReconciliationSummary(context.Context) (AdminReconciliationSummary, error)
}

type Dependencies struct {
	Sessions       SessionReader
	Dashboard      DashboardReader
	Ledger         LedgerReader
	Wagers         WagerReader
	Reconciliation ReconciliationReader
}

type BalanceRow struct {
	Label   string
	Account string
	Amount  ledger.Money
}

type DashboardSummary struct {
	Balances        []BalanceRow
	OpenWagers      int
	PendingWagers   int
	SettledWagers   int
	RecentActivity  []LedgerRow
	CreditLimit     ledger.Money
	CreditAvailable ledger.Money
}

type LedgerRow struct {
	OccurredAt        time.Time
	Description       string
	TransactionType   string
	Reference         string
	Account           string
	Amount            ledger.Money
	RunningBalance    ledger.Money
	HasRunningBalance bool
}

type WagerRow struct {
	PlacedAt        time.Time
	Market          string
	Selection       string
	Odds            ledger.AmericanOdds
	Stake           ledger.Money
	PotentialProfit ledger.Money
	Status          string
}

type AdminReconciliationSummary struct {
	AsOf                   time.Time
	LedgerBalanced         bool
	LedgerTransactions     int
	UnbalancedTransactions int
	PendingOutboxEvents    int
	FailedOutboxEvents     int
	MigrationDifference    ledger.Money
}

type Handler struct {
	mux       *http.ServeMux
	deps      Dependencies
	templates map[string]*template.Template
}

type pageData struct {
	Title          string
	Current        string
	Session        Session
	Dashboard      DashboardSummary
	LedgerRows     []LedgerRow
	WagerRows      []WagerRow
	Reconciliation AdminReconciliationSummary
}

func New(deps Dependencies) (*Handler, error) {
	if deps.Sessions == nil || deps.Dashboard == nil || deps.Ledger == nil || deps.Wagers == nil || deps.Reconciliation == nil {
		return nil, errors.New("private web dependencies must all be configured")
	}
	templates, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	handler := &Handler{mux: http.NewServeMux(), deps: deps, templates: templates}
	handler.routes()
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /book", h.book)
	h.mux.HandleFunc("GET /book/ledger", h.ledger)
	h.mux.HandleFunc("GET /book/wagers", h.wagers)
	h.mux.HandleFunc("GET /admin", h.admin)
}

func (h *Handler) book(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	summary, err := h.deps.Dashboard.DashboardSummary(r.Context(), session.UserID)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "book", pageData{Title: "Member book", Current: "book", Session: session, Dashboard: summary})
}

func (h *Handler) ledger(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	rows, err := h.deps.Ledger.LedgerRows(r.Context(), session.UserID)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "ledger", pageData{Title: "Ledger", Current: "ledger", Session: session, LedgerRows: rows})
}

func (h *Handler) wagers(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	rows, err := h.deps.Wagers.WagerRows(r.Context(), session.UserID)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "wagers", pageData{Title: "Wagers", Current: "wagers", Session: session, WagerRows: rows})
}

func (h *Handler) admin(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	if session.Role != RoleAdmin && session.Role != RoleOwner {
		h.renderStatus(w, http.StatusForbidden, "forbidden", pageData{Title: "Access denied", Session: session})
		return
	}
	summary, err := h.deps.Reconciliation.ReconciliationSummary(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "admin", pageData{Title: "Reconciliation", Current: "admin", Session: session, Reconciliation: summary})
}

func (h *Handler) requireMember(w http.ResponseWriter, r *http.Request) (Session, bool) {
	session, err := h.deps.Sessions.CurrentSession(r)
	if errors.Is(err, ErrNoSession) {
		query := url.Values{"next": []string{r.URL.RequestURI()}}
		destination := (&url.URL{Path: "/login", RawQuery: query.Encode()}).String()
		http.Redirect(w, r, destination, http.StatusSeeOther)
		return Session{}, false
	}
	if err != nil {
		h.internalError(w)
		return Session{}, false
	}
	if !session.Active || session.UserID == "" || !validMemberRole(session.Role) {
		h.renderStatus(w, http.StatusForbidden, "forbidden", pageData{Title: "Access denied", Session: session})
		return Session{}, false
	}
	return session, true
}

func validMemberRole(role Role) bool {
	return role == RoleMember || role == RoleAdmin || role == RoleOwner
}

func (h *Handler) render(w http.ResponseWriter, name string, data pageData) {
	h.renderStatus(w, http.StatusOK, name, data)
}

func (h *Handler) renderStatus(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates[name].ExecuteTemplate(w, "private_layout", data); err != nil {
		return
	}
}

func (h *Handler) internalError(w http.ResponseWriter) {
	http.Error(w, "Unable to load this page", http.StatusInternalServerError)
}

func parseTemplates() (map[string]*template.Template, error) {
	functions := template.FuncMap{
		"money": formatMoney,
		"odds":  formatOdds,
		"when":  formatTime,
	}
	pages := []string{"book", "ledger", "wagers", "admin", "forbidden"}
	result := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		tmpl, err := template.New("private_layout").Funcs(functions).ParseFS(
			publicassets.Files,
			"templates/private_layout.gohtml",
			"templates/private_"+page+".gohtml",
		)
		if err != nil {
			return nil, fmt.Errorf("parse private %s template: %w", page, err)
		}
		result[page] = tmpl
	}
	return result, nil
}

func formatMoney(amount ledger.Money) string {
	negative := amount.Cents < 0
	var magnitude uint64
	if negative {
		magnitude = uint64(-(amount.Cents + 1)) + 1
	} else {
		magnitude = uint64(amount.Cents)
	}
	prefix := string(amount.Currency) + " "
	switch amount.Currency {
	case ledger.CAD:
		prefix = "CA$"
	case "USD":
		prefix = "US$"
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%s%d.%02d", sign, prefix, magnitude/100, magnitude%100)
}

func formatOdds(value ledger.AmericanOdds) string {
	if value > 0 {
		return "+" + strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatInt(int64(value), 10)
}

func formatTime(value time.Time) string {
	return webtime.Format(value)
}
