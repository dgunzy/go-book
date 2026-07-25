// Package bettingweb provides the authenticated betting UI: members browse
// open markets and place wagers, admins run the book (create/open/close/
// settle markets, accept/reject wagers). Route handlers validate input and
// call the bettingpg store; ledger and settlement rules live in
// internal/betting and internal/bettingpg, never here.
//
// The handler is exported so cmd/cabot can mount it under the existing
// /book and /admin prefixes alongside internal/privateweb.
package bettingweb

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

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/privateweb"
	"github.com/dgunzy/go-book/internal/webtime"
	publicassets "github.com/dgunzy/go-book/web"
)

const (
	maxFormBytes = 64 << 10
	maxReasonLen = 500
	maxTitleLen  = 200
	// startingSelectionSlots is how many blank outcome rows a fresh create
	// form renders. It is a starting point, not a limit: the page adds more on
	// demand and the parser reads whatever the form submits, up to the store's
	// MaxSelectionsPerMarket.
	startingSelectionSlots = 4

	redirectBookWagers   = "/book/wagers"
	redirectAdminMarkets = "/admin/markets"
	redirectAdminWagers  = "/admin/wagers"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(value string) bool { return uuidPattern.MatchString(value) }

// SessionReader is satisfied by authweb's session adapter. The acting user
// for every store call comes from here, never from a form value.
type SessionReader interface {
	CurrentSession(*http.Request) (privateweb.Session, error)
}

// MarketStore is the market surface of bettingpg.Store this handler needs.
// Every state-changing method takes the acting user for audit.
type MarketStore interface {
	ListMarkets(context.Context) ([]bettingpg.MarketRow, error)
	ListOpenMarkets(context.Context) ([]bettingpg.MarketRow, error)
	ListOpenMarketsForUser(ctx context.Context, userID string) ([]bettingpg.MarketRow, error)
	RestrictMember(context.Context, bettingpg.RestrictRequest) error
	LiftRestriction(ctx context.Context, marketID, userID, selectionID string) error
	ListRestrictions(context.Context) ([]bettingpg.RestrictionRow, error)
	ListMembers(context.Context) ([]bettingpg.MemberOption, error)
	ListMarketableMatches(context.Context) ([]bettingpg.MatchMarketOption, error)
	CreateMarket(context.Context, bettingpg.CreateMarketRequest) (betting.Market, error)
	OpenMarket(ctx context.Context, marketID, actor string) error
	CloseMarket(ctx context.Context, marketID, actor string) error
	SettleMarket(context.Context, bettingpg.SettleMarketRequest) (bettingpg.SettleReport, error)
	SetOpeningLine(ctx context.Context, marketID, selectionID string, odds ledger.AmericanOdds, actorUserID, reason string) (bool, error)
	VoidMarket(context.Context, bettingpg.VoidMarketRequest) (bettingpg.SettleReport, error)
}

// WagerStore is the wager surface of bettingpg.Store this handler needs.
type WagerStore interface {
	PlaceWager(context.Context, bettingpg.PlaceWagerRequest) (betting.Wager, error)
	AcceptWager(ctx context.Context, wagerID, actorUserID string) (betting.Wager, error)
	RejectWager(ctx context.Context, wagerID, actorUserID, reason string) (betting.Wager, error)
	CancelWager(ctx context.Context, wagerID, userID string) (betting.Wager, error)
	VoidWager(ctx context.Context, wagerID, actorUserID, reason string) (betting.Wager, error)
	ListWagersByState(context.Context, betting.WagerState) ([]bettingpg.AdminWagerRow, error)
	ListWagerRecord(ctx context.Context, limit int) ([]bettingpg.WagerRecordRow, error)
	ListWagersForUser(context.Context, string) ([]bettingpg.UserWagerRow, error)
	AutoApproveLimitForUser(ctx context.Context, userID string) (int64, bool, error)
}

var (
	_ MarketStore = bettingpg.Store{}
	_ WagerStore  = bettingpg.Store{}
)

type Dependencies struct {
	Sessions SessionReader
	Markets  MarketStore
	Wagers   WagerStore
	// Settlements records money that changed hands outside the app so a
	// member's balance shows what is still outstanding.
	Settlements SettlementStore
	// AutoApproveMaxCents is the largest stake accepted automatically on
	// placement; larger wagers stay pending for manual approval. Zero disables
	// auto-approval.
	AutoApproveMaxCents int64
	// PricingLiquidityDefaultCents is the dynamic-pricing liquidity used when an
	// admin enables dynamic pricing on a new market without typing a value.
	PricingLiquidityDefaultCents int64
	// DefaultCreditLimitCents is the credit limit a newly invited member is
	// created with. Shown on the help page so admins can read the book's
	// current settings without digging through deployment configuration.
	DefaultCreditLimitCents int64
}

type Handler struct {
	mux                          *http.ServeMux
	deps                         Dependencies
	templates                    map[string]*template.Template
	newID                        func() (string, error)
	autoApproveMaxCents          int64
	pricingLiquidityDefaultCents int64
	defaultCreditLimitCents      int64
}

func New(deps Dependencies) (*Handler, error) {
	if deps.Sessions == nil || deps.Markets == nil || deps.Wagers == nil || deps.Settlements == nil {
		return nil, errors.New("betting web dependencies must all be configured")
	}
	templates, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	pricingLiquidityDefaultCents := deps.PricingLiquidityDefaultCents
	if pricingLiquidityDefaultCents <= 0 {
		pricingLiquidityDefaultCents = bettingpg.DefaultPricingLiquidityCents
	}
	handler := &Handler{
		mux: http.NewServeMux(), deps: deps, templates: templates,
		autoApproveMaxCents:          deps.AutoApproveMaxCents,
		pricingLiquidityDefaultCents: pricingLiquidityDefaultCents,
		defaultCreditLimitCents:      deps.DefaultCreditLimitCents,
		newID: func() (string, error) {
			id, err := betting.NewEventID()
			return string(id), err
		},
	}
	handler.routes()
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /book/markets", h.bookMarkets)
	h.mux.HandleFunc("GET /book/wagers", h.bookWagers)
	h.mux.HandleFunc("POST /book/wagers", h.placeWager)
	h.mux.HandleFunc("POST /book/wagers/{id}/cancel", h.cancelWager)
	h.mux.HandleFunc("GET /admin/markets", h.adminMarkets)
	h.mux.HandleFunc("GET /admin/markets/new", h.adminMarketNew)
	h.mux.HandleFunc("POST /admin/markets", h.adminCreateMarket)
	h.mux.HandleFunc("POST /admin/markets/{id}/open", h.adminOpenMarket)
	h.mux.HandleFunc("POST /admin/markets/{id}/close", h.adminCloseMarket)
	h.mux.HandleFunc("POST /admin/markets/{id}/selections/{selectionID}/line", h.adminSetLine)
	h.mux.HandleFunc("POST /admin/markets/{id}/restrict", h.adminRestrictMember)
	h.mux.HandleFunc("POST /admin/markets/{id}/restrict/lift", h.adminLiftRestriction)
	h.mux.HandleFunc("GET /admin/markets/{id}/settle", h.adminSettleForm)
	h.mux.HandleFunc("POST /admin/markets/{id}/settle", h.adminSettleMarket)
	h.mux.HandleFunc("GET /admin/wagers", h.adminWagers)
	h.mux.HandleFunc("GET /admin/wagers/record", h.adminWagerRecord)
	h.mux.HandleFunc("POST /admin/wagers/{id}/accept", h.adminAcceptWager)
	h.mux.HandleFunc("POST /admin/wagers/{id}/reject", h.adminRejectWager)
	h.mux.HandleFunc("POST /admin/wagers/{id}/void", h.adminVoidWager)
	h.mux.HandleFunc("GET /admin/settle-up", h.adminSettleUp)
	h.mux.HandleFunc("POST /admin/settle-up", h.adminRecordSettlement)
	h.mux.HandleFunc("POST /admin/settle-up/{id}/reverse", h.adminReverseSettlement)
	h.mux.HandleFunc("GET /admin/help", h.adminHelp)
}

// adminHelp renders the static how-to guide for admins and owners. When any
// user-facing behavior changes, update templates/admin_help.gohtml to match
// (see AGENTS.md).
func (h *Handler) adminHelp(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	h.render(w, "admin_help", pageData{
		Title: "How to run the book", Current: "admin-help", Session: session,
		AutoApproveDollars:       formatCentsDollars(h.autoApproveMaxCents),
		DefaultLineWeightDollars: formatCentsDollars(h.pricingLiquidityDefaultCents),
		DefaultCreditDollars:     formatCentsDollars(h.defaultCreditLimitCents),
	})
}

// --- view models -----------------------------------------------------------

type selectionView struct {
	bettingpg.MarketSelectionRow
	MarketID string
	PlaceKey string
}

type marketView struct {
	Market     bettingpg.MarketRow
	Selections []selectionView
}

// lineMove describes how the live line compares to the price a pending wager
// was placed at, from the member's point of view.
type lineMove string

const (
	lineUnchanged lineMove = "unchanged"
	// lineToMember means the wager's locked price now pays more than the board
	// does: accepting it books worse value than the current line, so the admin
	// may want to reject.
	lineToMember lineMove = "member"
	// lineToBook means the board now pays more than the locked price, so the
	// wager is cheap for the book.
	lineToBook lineMove = "book"
	// lineOffBoard means the selection or market is no longer taking action,
	// so there is no live line to compare against.
	lineOffBoard lineMove = "off-board"
)

// adminWagerView is one pending wager plus the live line comparison shown in
// the approval queue.
type adminWagerView struct {
	bettingpg.AdminWagerRow
	Move lineMove
	// LiveProfit is what the same stake would win at the current line. It is
	// zero when the line is off the board.
	LiveProfit ledger.Money
	// ProfitDelta is LiveProfit minus the wager's locked potential profit:
	// negative means the book is paying out more than it would today.
	ProfitDelta ledger.Money
}

// MovedToMember reports whether the line moved in the member's favour, which
// is the case the template warns on.
func (v adminWagerView) MovedToMember() bool { return v.Move == lineToMember }

// MovedToBook reports whether the board now prices the selection worse for the
// member than the pending wager locked in.
func (v adminWagerView) MovedToBook() bool { return v.Move == lineToBook }

// LiveLineAvailable reports whether there is a current price to display.
func (v adminWagerView) LiveLineAvailable() bool { return v.Move != lineOffBoard }

// memberWagerView is one of the member's own wagers plus the live line, so a
// member waiting on approval can see whether the price moved and pull the bet
// while it is still pending.
type memberWagerView struct {
	bettingpg.UserWagerRow
	Move lineMove
}

// CanCancel reports whether the member may still withdraw this wager. Only a
// pending wager can be cancelled; once the book accepts it the stake is in
// escrow.
func (v memberWagerView) CanCancel() bool { return v.State == betting.WagerPending }

// Cancelled distinguishes a wager the member pulled from one the book turned
// down, so the status column can say which happened.
func (v memberWagerView) Cancelled() bool {
	return v.State == betting.WagerRejected && v.RejectionReason == betting.CancelWagerReason
}

// MovedAgainstMember reports whether the live line now pays less than the
// pending wager locked in — the case where a member most likely wants out.
func (v memberWagerView) MovedAgainstMember() bool {
	return v.CanCancel() && v.Move == lineToBook
}

// MovedForMember reports whether the live line now pays more than the pending
// wager locked in.
func (v memberWagerView) MovedForMember() bool {
	return v.CanCancel() && v.Move == lineToMember
}

// ShowLiveLine keeps the live line out of settled history, where the price on
// the board today says nothing about a bet already graded.
func (v memberWagerView) ShowLiveLine() bool {
	return v.CanCancel() && v.Move != lineOffBoard
}

func memberWagerViews(rows []bettingpg.UserWagerRow) []memberWagerView {
	views := make([]memberWagerView, 0, len(rows))
	for _, row := range rows {
		view := memberWagerView{UserWagerRow: row}
		if !row.SelectionActive || row.MarketState != betting.MarketOpen {
			view.Move = lineOffBoard
		} else {
			view.Move = compareLines(row.Odds, row.CurrentOdds)
		}
		views = append(views, view)
	}
	return views
}

func adminWagerViews(rows []bettingpg.AdminWagerRow) []adminWagerView {
	views := make([]adminWagerView, 0, len(rows))
	for _, row := range rows {
		views = append(views, newAdminWagerView(row))
	}
	return views
}

func newAdminWagerView(row bettingpg.AdminWagerRow) adminWagerView {
	view := adminWagerView{AdminWagerRow: row}
	if !row.SelectionActive || row.MarketState != betting.MarketOpen {
		view.Move = lineOffBoard
		return view
	}
	view.Move = compareLines(row.Odds, row.CurrentOdds)
	liveProfit, err := row.CurrentOdds.Profit(row.Stake)
	if err != nil {
		// A price that cannot be priced against this stake is not worth
		// blocking the queue over; the comparison alone still renders.
		return view
	}
	view.LiveProfit = liveProfit
	if negated, negateErr := row.PotentialProfit.Negate(); negateErr == nil {
		if delta, deltaErr := liveProfit.Add(negated); deltaErr == nil {
			view.ProfitDelta = delta
		}
	}
	return view
}

// compareLines ranks two American prices by the profit each pays per unit of
// stake, using integer cross-multiplication so no float rounding can flip a
// near-tie. A locked price that pays more than the live one is a move in the
// member's favour.
func compareLines(accepted, current ledger.AmericanOdds) lineMove {
	if accepted == current {
		return lineUnchanged
	}
	acceptedNum, acceptedDen := payoutRatio(accepted)
	currentNum, currentDen := payoutRatio(current)
	left := acceptedNum * currentDen
	right := currentNum * acceptedDen
	switch {
	case left > right:
		return lineToMember
	case left < right:
		return lineToBook
	default:
		return lineUnchanged
	}
}

// payoutRatio returns profit per unit stake as an exact fraction.
func payoutRatio(odds ledger.AmericanOdds) (numerator, denominator int64) {
	if odds > 0 {
		return int64(odds), 100
	}
	return 100, -int64(odds)
}

type pageData struct {
	Title             string
	Current           string
	Session           privateweb.Session
	Markets           []marketView
	Market            marketView
	MemberWagers      []memberWagerView
	AdminWagers       []adminWagerView
	Outstanding       []settleUpView
	WagerRecord       []wagerRecordView
	Settlements       []bettingpg.SettlementRow
	Restrictions      []bettingpg.RestrictionRow
	Members           []bettingpg.MemberOption
	FormError         string
	Notice            string
	BackLink          string
	Form              url.Values
	NewMarketID       string
	SelectionSlots    []int
	MaxSelections     int
	MarketableMatches []bettingpg.MatchMarketOption
	// AutoApproveDollars and DefaultLineWeightDollars are the human-readable
	// current settings shown on the help page.
	AutoApproveDollars       string
	DefaultLineWeightDollars string
	DefaultCreditDollars     string
}

// formatCentsDollars renders an integer-cents amount as a plain dollar string
// (no currency symbol), e.g. 10000 -> "100.00".
func formatCentsDollars(cents int64) string {
	if cents < 0 {
		cents = 0
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

type fragmentData struct {
	Message string
	Detail  string
	IsError bool
}

// --- member routes ---------------------------------------------------------

func (h *Handler) bookMarkets(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	views, ok := h.openMarketViews(w, r.Context(), session.UserID, true)
	if !ok {
		return
	}
	h.render(w, "book_markets", pageData{Title: "Markets", Current: "markets", Session: session, Markets: views})
}

func (h *Handler) bookWagers(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	wagers, err := h.deps.Wagers.ListWagersForUser(r.Context(), session.UserID)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "book_wagers", pageData{Title: "Wagers", Current: "wagers", Session: session,
		MemberWagers: memberWagerViews(wagers)})
}

// cancelWager withdraws the member's own pending wager. The wager ID comes
// from the path and the owner from the session, so a member can only ever
// cancel their own bet.
func (h *Handler) cancelWager(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	wagerID := r.PathValue("id")
	if !isUUID(wagerID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested wager was not found.", redirectBookWagers)
		return
	}
	wager, err := h.deps.Wagers.CancelWager(r.Context(), wagerID, session.UserID)
	if err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, betting.ErrInvalidTransition) {
			text = "This wager is no longer pending — the book has already reviewed it."
		}
		h.failPost(w, r, session, status, text, redirectBookWagers)
		return
	}
	h.completePost(w, r, redirectBookWagers, "Wager cancelled.",
		fmt.Sprintf("%s was never staked; nothing left your balance.", formatMoney(wager.Stake)))
}

func (h *Handler) placeWager(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	marketID := r.PostForm.Get("market_id")
	selectionID := r.PostForm.Get("selection_id")
	idempotencyKey := r.PostForm.Get("idempotency_key")
	if !isUUID(marketID) || !isUUID(selectionID) || !isUUID(idempotencyKey) {
		h.failPost(w, r, session, http.StatusBadRequest, "The wager form was incomplete. Reload the markets page and try again.", "/book/markets")
		return
	}
	stakeCents, err := parseStakeCents(r.PostForm.Get("stake"))
	if err != nil {
		h.failPost(w, r, session, http.StatusBadRequest, "Enter the stake as dollars and cents, for example 25 or 25.50.", "/book/markets")
		return
	}

	// The market's currency comes from the database, never from the form.
	openMarkets, err := h.deps.Markets.ListOpenMarkets(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	var currency ledger.Currency
	found := false
	for _, market := range openMarkets {
		if market.ID != marketID {
			continue
		}
		for _, selection := range market.Selections {
			if selection.ID == selectionID && selection.Active {
				currency = market.Currency
				found = true
			}
		}
	}
	if !found {
		h.failPost(w, r, session, http.StatusConflict, "That market is no longer open for wagers.", "/book/markets")
		return
	}

	wagerID, err := h.newID()
	if err != nil {
		h.internalError(w)
		return
	}
	wager, err := h.deps.Wagers.PlaceWager(r.Context(), bettingpg.PlaceWagerRequest{
		WagerID:            wagerID,
		UserID:             session.UserID,
		MarketID:           marketID,
		SelectionID:        selectionID,
		FundingAccountType: betting.FundingUserCash,
		StakeCents:         stakeCents,
		Currency:           currency,
		IdempotencyKey:     idempotencyKey,
	})
	if err != nil {
		status, message := storeErrorStatus(err)
		h.failPost(w, r, session, status, message, "/book/markets")
		return
	}

	// Auto-approve small wagers immediately; larger ones wait for an admin. A
	// failed auto-accept (e.g. insufficient funds) simply leaves the wager
	// pending for manual review rather than surfacing an error to the bettor.
	message := "Wager placed."
	detail := fmt.Sprintf("%s at %s for %s. It is pending admin approval.",
		wager.AcceptedTerms, formatOdds(wager.AcceptedOdds), formatMoney(wager.Stake))
	// A per-player override, when set, replaces the book-wide threshold.
	limit := h.autoApproveMaxCents
	if override, ok, limitErr := h.deps.Wagers.AutoApproveLimitForUser(r.Context(), session.UserID); limitErr == nil && ok {
		limit = override
	}
	if limit > 0 && stakeCents <= limit {
		if accepted, acceptErr := h.deps.Wagers.AcceptWager(r.Context(), string(wager.ID), bettingpg.AutoApproveActor); acceptErr == nil {
			message = "Wager accepted."
			detail = fmt.Sprintf("%s at %s for %s. Auto-approved and held in escrow.",
				accepted.AcceptedTerms, formatOdds(accepted.AcceptedOdds), formatMoney(accepted.Stake))
		}
	}
	h.completePost(w, r, redirectBookWagers, message, detail)
}

// --- admin routes ----------------------------------------------------------

func (h *Handler) adminMarkets(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	markets, err := h.deps.Markets.ListMarkets(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	restrictions, err := h.deps.Markets.ListRestrictions(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	members, err := h.deps.Markets.ListMembers(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "admin_markets", pageData{
		Title: "Markets", Current: "admin-markets", Session: session, Markets: plainViews(markets),
		Restrictions: restrictions, Members: members,
	})
}

func (h *Handler) adminMarketNew(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	marketID, err := h.newID()
	if err != nil {
		h.internalError(w)
		return
	}
	matches, err := h.deps.Markets.ListMarketableMatches(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "admin_market_new", pageData{
		Title: "New market", Current: "admin-markets", Session: session,
		NewMarketID: marketID, Form: defaultMarketForm(h.pricingLiquidityDefaultCents), SelectionSlots: selectionSlots(nil), MaxSelections: bettingpg.MaxSelectionsPerMarket, MarketableMatches: matches,
	})
}

func (h *Handler) adminCreateMarket(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	request, formError := parseCreateMarketForm(r.PostForm)
	if formError != "" {
		h.rerenderCreateForm(w, r, session, formError)
		return
	}
	// When dynamic pricing is on but the admin cleared the liquidity field, apply
	// the configured book default so the operator knob is the single source.
	if request.DynamicPricing && request.PricingLiquidityCents <= 0 {
		request.PricingLiquidityCents = h.pricingLiquidityDefaultCents
	}
	if request.Type == betting.MarketMatch {
		match, ok, err := h.findMarketableMatch(r.Context(), request.MatchID)
		if err != nil {
			h.internalError(w)
			return
		}
		if !ok {
			h.rerenderCreateForm(w, r, session, "Choose an open match that does not already have a market.")
			return
		}
		request.Title = match.Title()
		request.Selections[0].Key = "side-1"
		request.Selections[0].DisplayTerms = match.Side1BetLabel() + " to win"
		request.Selections[0].SemanticResultKey = "side:" + match.Side1ID
		request.Selections[1].Key = "side-2"
		request.Selections[1].DisplayTerms = match.Side2BetLabel() + " to win"
		request.Selections[1].SemanticResultKey = "side:" + match.Side2ID
	}
	request.ActorUserID = session.UserID
	if _, err := h.deps.Markets.CreateMarket(r.Context(), request); err != nil {
		status, message := storeErrorStatus(err)
		if status == http.StatusInternalServerError {
			h.internalError(w)
			return
		}
		h.rerenderCreateForm(w, r, session, message)
		return
	}
	h.completePost(w, r, redirectAdminMarkets, "Market created.", "The market is in draft state until you open it.")
}

func (h *Handler) rerenderCreateForm(w http.ResponseWriter, r *http.Request, session privateweb.Session, formError string) {
	marketID := r.PostForm.Get("market_id")
	if !isUUID(marketID) {
		var err error
		if marketID, err = h.newID(); err != nil {
			h.internalError(w)
			return
		}
	}
	if isHTMX(r) {
		h.fragment(w, http.StatusBadRequest, fragmentData{Message: "Market was not created.", Detail: formError, IsError: true})
		return
	}
	matches, err := h.deps.Markets.ListMarketableMatches(r.Context())
	if err != nil {
		h.internalError(w)
		return
	}
	h.renderStatus(w, http.StatusBadRequest, "admin_market_new", pageData{
		Title: "New market", Current: "admin-markets", Session: session, FormError: formError,
		NewMarketID: marketID, Form: r.PostForm, SelectionSlots: selectionSlots(r.PostForm), MaxSelections: bettingpg.MaxSelectionsPerMarket, MarketableMatches: matches,
	})
}

func (h *Handler) findMarketableMatch(ctx context.Context, matchID string) (bettingpg.MatchMarketOption, bool, error) {
	matches, err := h.deps.Markets.ListMarketableMatches(ctx)
	if err != nil {
		return bettingpg.MatchMarketOption{}, false, err
	}
	for _, match := range matches {
		if match.MatchID == matchID {
			return match, true, nil
		}
	}
	return bettingpg.MatchMarketOption{}, false, nil
}

func (h *Handler) adminOpenMarket(w http.ResponseWriter, r *http.Request) {
	h.adminMarketAction(w, r, "open")
}

func (h *Handler) adminCloseMarket(w http.ResponseWriter, r *http.Request) {
	h.adminMarketAction(w, r, "close")
}

func (h *Handler) adminMarketAction(w http.ResponseWriter, r *http.Request, action string) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	marketID := r.PathValue("id")
	if !isUUID(marketID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested market was not found.", redirectAdminMarkets)
		return
	}
	var err error
	message := "Market opened."
	if action == "open" {
		err = h.deps.Markets.OpenMarket(r.Context(), marketID, session.UserID)
	} else {
		message = "Market closed."
		err = h.deps.Markets.CloseMarket(r.Context(), marketID, session.UserID)
	}
	if err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, redirectAdminMarkets)
		return
	}
	h.completePost(w, r, redirectAdminMarkets, message, "")
}

func (h *Handler) adminSettleForm(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	market, ok := h.findMarket(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	h.render(w, "admin_market_settle", pageData{
		Title: "Settle market", Current: "admin-markets", Session: session, Market: plainView(market),
	})
}

func (h *Handler) adminSettleMarket(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	market, ok := h.findMarket(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"A settlement reason is required (up to 500 characters). Match markets normally settle automatically from the verified result; a manual grade must say why.",
			redirectAdminMarkets)
		return
	}
	switch r.PostForm.Get("action") {
	case "void":
		_, err := h.deps.Markets.VoidMarket(r.Context(), bettingpg.VoidMarketRequest{
			MarketID: market.ID, ActorUserID: session.UserID, Reason: reason,
		})
		if err != nil {
			status, text := storeErrorStatus(err)
			h.failPost(w, r, session, status, text, redirectAdminMarkets)
			return
		}
		h.completePost(w, r, redirectAdminMarkets, "Market voided.", "Every accepted wager was refunded.")
	case "settle":
		// The gradable selection list comes from the database row, not from
		// the submitted form keys.
		outcome := make(map[string]betting.SettlementResult, len(market.Selections))
		for _, selection := range market.Selections {
			result := betting.SettlementResult(r.PostForm.Get("outcome_" + selection.ID))
			if result != betting.ResultWin && result != betting.ResultLoss && result != betting.ResultPush {
				h.failPost(w, r, session, http.StatusBadRequest,
					"Pick win, loss, or push for every selection, or void the whole market.", redirectAdminMarkets)
				return
			}
			outcome[selection.ID] = result
		}
		_, err := h.deps.Markets.SettleMarket(r.Context(), bettingpg.SettleMarketRequest{
			MarketID: market.ID, Outcome: outcome, ActorUserID: session.UserID, Reason: reason,
		})
		if err != nil {
			status, text := storeErrorStatus(err)
			h.failPost(w, r, session, status, text, redirectAdminMarkets)
			return
		}
		h.completePost(w, r, redirectAdminMarkets, "Market settled.", "Wagers were graded and paid from escrow.")
	default:
		h.failPost(w, r, session, http.StatusBadRequest, "Choose either settle or void.", redirectAdminMarkets)
	}
}

func (h *Handler) adminWagers(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	wagers, err := h.deps.Wagers.ListWagersByState(r.Context(), betting.WagerPending)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "admin_wagers", pageData{Title: "Pending wagers", Current: "admin-wagers", Session: session,
		AdminWagers: adminWagerViews(wagers)})
}

func (h *Handler) adminAcceptWager(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	wagerID := r.PathValue("id")
	if !isUUID(wagerID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested wager was not found.", redirectAdminWagers)
		return
	}
	wager, err := h.deps.Wagers.AcceptWager(r.Context(), wagerID, session.UserID)
	if err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, redirectAdminWagers)
		return
	}
	h.completePost(w, r, redirectAdminWagers, "Wager accepted.",
		fmt.Sprintf("%s moved to escrow.", formatMoney(wager.Stake)))
}

func (h *Handler) adminRejectWager(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	wagerID := r.PathValue("id")
	if !isUUID(wagerID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested wager was not found.", redirectAdminWagers)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest, "A rejection reason is required (up to 500 characters).", redirectAdminWagers)
		return
	}
	if _, err := h.deps.Wagers.RejectWager(r.Context(), wagerID, session.UserID, reason); err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, redirectAdminWagers)
		return
	}
	h.completePost(w, r, redirectAdminWagers, "Wager rejected.", "No funds moved.")
}

// --- authentication and authorization --------------------------------------

func (h *Handler) requireMember(w http.ResponseWriter, r *http.Request) (privateweb.Session, bool) {
	session, err := h.deps.Sessions.CurrentSession(r)
	if errors.Is(err, privateweb.ErrNoSession) {
		http.Redirect(w, r, privateweb.LoginRedirectURL(r), http.StatusSeeOther)
		return privateweb.Session{}, false
	}
	if err != nil {
		h.internalError(w)
		return privateweb.Session{}, false
	}
	if !session.Active || session.UserID == "" || !validMemberRole(session.Role) {
		h.renderStatus(w, http.StatusForbidden, "forbidden", pageData{Title: "Access denied", Session: session})
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
		h.renderStatus(w, http.StatusForbidden, "forbidden", pageData{Title: "Access denied", Session: session})
		return privateweb.Session{}, false
	}
	return session, true
}

func validMemberRole(role privateweb.Role) bool {
	return role == privateweb.RoleMember || role == privateweb.RoleAdmin || role == privateweb.RoleOwner
}

// checkedForm parses the POST body and validates the session-bound CSRF
// token with a constant-time comparison. The token in the session was
// already validated against the server-side session hash by the session
// reader, so matching the form token to it binds the form to this session.
func (h *Handler) checkedForm(w http.ResponseWriter, r *http.Request, session privateweb.Session) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		h.failPost(w, r, session, http.StatusBadRequest, "The submitted form could not be read.", "/book")
		return false
	}
	token := r.PostForm.Get("csrf_token")
	if token == "" || session.CSRFToken == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(session.CSRFToken)) != 1 {
		h.renderStatus(w, http.StatusForbidden, "forbidden", pageData{Title: "Access denied", Session: session})
		return false
	}
	return true
}

// --- shared helpers --------------------------------------------------------

// openMarketViews builds the open board. userID scopes it to what that member
// may bet: markets and sides they are restricted from are not shown at all.
// Pass an empty userID for an admin view of the whole board. Hiding is a
// courtesy — placement re-checks the restriction against the database.
func (h *Handler) openMarketViews(w http.ResponseWriter, ctx context.Context, userID string, withPlaceKeys bool) ([]marketView, bool) {
	var markets []bettingpg.MarketRow
	var err error
	if userID == "" {
		markets, err = h.deps.Markets.ListOpenMarkets(ctx)
	} else {
		markets, err = h.deps.Markets.ListOpenMarketsForUser(ctx, userID)
	}
	if err != nil {
		h.internalError(w)
		return nil, false
	}
	views := make([]marketView, 0, len(markets))
	for _, market := range markets {
		view := marketView{Market: market}
		for _, selection := range market.Selections {
			item := selectionView{MarketSelectionRow: selection, MarketID: market.ID}
			if withPlaceKeys {
				key, err := h.newID()
				if err != nil {
					h.internalError(w)
					return nil, false
				}
				item.PlaceKey = key
			}
			view.Selections = append(view.Selections, item)
		}
		views = append(views, view)
	}
	return views, true
}

func plainViews(markets []bettingpg.MarketRow) []marketView {
	views := make([]marketView, 0, len(markets))
	for _, market := range markets {
		views = append(views, plainView(market))
	}
	return views
}

func plainView(market bettingpg.MarketRow) marketView {
	view := marketView{Market: market}
	for _, selection := range market.Selections {
		view.Selections = append(view.Selections, selectionView{MarketSelectionRow: selection, MarketID: market.ID})
	}
	return view
}

func (h *Handler) findMarket(w http.ResponseWriter, r *http.Request, marketID string) (bettingpg.MarketRow, bool) {
	if !isUUID(marketID) {
		h.renderStatus(w, http.StatusNotFound, "message", pageData{
			Title: "Market not found", Notice: "404", FormError: "The requested market was not found.", BackLink: redirectAdminMarkets,
		})
		return bettingpg.MarketRow{}, false
	}
	markets, err := h.deps.Markets.ListMarkets(r.Context())
	if err != nil {
		h.internalError(w)
		return bettingpg.MarketRow{}, false
	}
	for _, market := range markets {
		if market.ID == marketID {
			return market, true
		}
	}
	h.renderStatus(w, http.StatusNotFound, "message", pageData{
		Title: "Market not found", Notice: "404", FormError: "The requested market was not found.", BackLink: redirectAdminMarkets,
	})
	return bettingpg.MarketRow{}, false
}

// completePost finishes a successful state change: an HTMX request receives
// a status fragment; a plain browser request is redirected (always to one of
// the fixed local paths above, never to input-derived targets).
func (h *Handler) completePost(w http.ResponseWriter, r *http.Request, redirectTo, message, detail string) {
	if isHTMX(r) {
		h.fragment(w, http.StatusOK, fragmentData{Message: message, Detail: detail})
		return
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// failPost reports a failed state change without leaking internals: an HTMX
// request receives an error fragment, a plain request a full message page.
func (h *Handler) failPost(w http.ResponseWriter, r *http.Request, session privateweb.Session, status int, message, backLink string) {
	if isHTMX(r) {
		h.fragment(w, status, fragmentData{Message: "The request was not completed.", Detail: message, IsError: true})
		return
	}
	h.renderStatus(w, status, "message", pageData{
		Title: "The request was not completed", Notice: "Betting", Session: session,
		FormError: message, BackLink: backLink,
	})
}

func (h *Handler) fragment(w http.ResponseWriter, status int, data fragmentData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.templates["message"].ExecuteTemplate(w, "betting_action_result", data)
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func storeErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, betting.ErrNotFound):
		return http.StatusNotFound, "The requested record was not found."
	case errors.Is(err, bettingpg.ErrInsufficientFunds):
		return http.StatusConflict, "The member's funding account does not cover this stake."
	case errors.Is(err, bettingpg.ErrMarketNotSettleable):
		return http.StatusConflict, "This market is not in a state that can be settled or voided."
	case errors.Is(err, bettingpg.ErrMarketNotOpenable):
		return http.StatusConflict, "This market cannot be opened from its current state."
	case errors.Is(err, bettingpg.ErrMatchMarketExists):
		return http.StatusConflict, "That match already has an active market. Choose another match."
	case errors.Is(err, bettingpg.ErrIdempotencyConflict), errors.Is(err, betting.ErrIdempotencyConflict):
		return http.StatusConflict, "This request conflicts with one already recorded."
	case errors.Is(err, betting.ErrMarketNotOpen):
		return http.StatusConflict, "This market is not open for wagers."
	case errors.Is(err, betting.ErrSelectionInactive):
		return http.StatusConflict, "This selection is no longer available."
	case errors.Is(err, betting.ErrUserRestricted):
		return http.StatusForbidden, "This account is not able to bet on this market."
	case errors.Is(err, betting.ErrInvalidTransition):
		return http.StatusConflict, "This action is not allowed in the current state."
	case errors.Is(err, betting.ErrReasonRequired):
		return http.StatusBadRequest, "A reason is required for this action."
	case errors.Is(err, betting.ErrUnauthorized):
		return http.StatusForbidden, "You are not authorized to perform this action."
	case errors.Is(err, ledger.ErrCurrencyMismatch):
		return http.StatusBadRequest, "The stake currency does not match the market."
	case errors.Is(err, betting.ErrInvalid):
		return http.StatusBadRequest, "The submitted values were not valid."
	default:
		return http.StatusInternalServerError, "Unable to complete this request."
	}
}

// --- create-market form parsing --------------------------------------------

// defaultMarketForm pre-fills a fresh create-market form so dynamic pricing is
// on by default, pre-filled with the configured liquidity, and the admin need
// not enter it.
func defaultMarketForm(liquidityDefaultCents int64) url.Values {
	return url.Values{
		"market_type": {"future"}, "dynamic_pricing": {"1"},
		"pricing_liquidity": {formatCentsDollars(liquidityDefaultCents)}, "currency": {"CAD"},
		"match_sign_1": {"-"}, "match_odds_1": {"110"},
		"match_sign_2": {"-"}, "match_odds_2": {"110"},
	}
}

// selectionSlots returns the outcome row numbers to render. A fresh form gets
// a handful of blank rows; a form coming back from a validation error keeps
// every row the admin had filled in, however many they added.
func selectionSlots(form url.Values) []int {
	count := startingSelectionSlots
	if used := highestSelectionSlot(form); used >= count {
		count = used + 1
	}
	if count > bettingpg.MaxSelectionsPerMarket {
		count = bettingpg.MaxSelectionsPerMarket
	}
	slots := make([]int, count)
	for i := range slots {
		slots[i] = i + 1
	}
	return slots
}

// highestSelectionSlot finds the largest outcome row number the form actually
// carries, so a market with sixteen runners round-trips through a validation
// error without losing rows.
func highestSelectionSlot(form url.Values) int {
	highest := 0
	for slot := 1; slot <= bettingpg.MaxSelectionsPerMarket; slot++ {
		if strings.TrimSpace(form.Get(fmt.Sprintf("selection_terms_%d", slot))) != "" ||
			strings.TrimSpace(form.Get(fmt.Sprintf("selection_odds_%d", slot))) != "" {
			highest = slot
		}
	}
	return highest
}

func parseCreateMarketForm(form url.Values) (bettingpg.CreateMarketRequest, string) {
	var request bettingpg.CreateMarketRequest

	request.MarketID = form.Get("market_id")
	if !isUUID(request.MarketID) {
		return request, "The form is missing its server token. Reload the page and try again."
	}
	request.Type = betting.MarketType(form.Get("market_type"))
	if err := request.Type.Validate(); err != nil {
		return request, "Choose a market type of match, future, or prop."
	}
	request.MatchID = strings.TrimSpace(form.Get("match_id"))
	if request.Type == betting.MarketMatch && !isUUID(request.MatchID) {
		return request, "Choose the match this market belongs to."
	}
	if request.Type != betting.MarketMatch && request.MatchID != "" {
		return request, "Only match markets may reference a match."
	}
	request.Title = strings.TrimSpace(form.Get("title"))
	if request.Type != betting.MarketMatch && (request.Title == "" || len(request.Title) > maxTitleLen) {
		return request, "Enter a market title between 1 and 200 characters."
	}
	currency, err := ledger.ParseCurrency(strings.TrimSpace(form.Get("currency")))
	if err != nil {
		return request, "Choose a three-letter currency code such as CAD."
	}
	request.Currency = currency

	// Dynamic pricing is enabled by default (the checkbox is pre-checked); an
	// admin only turns it off by unchecking it. When on, the liquidity is
	// optional and defaults to $500 in the store, so it never has to be typed.
	request.DynamicPricing = form.Get("dynamic_pricing") != ""
	if request.DynamicPricing {
		if raw := strings.TrimSpace(form.Get("pricing_liquidity")); raw != "" {
			liquidityCents, err := parseStakeCents(raw)
			if err != nil {
				return request, "Enter the pricing liquidity as a dollar amount, for example 500 (larger moves the line less)."
			}
			request.PricingLiquidityCents = liquidityCents
		}
	}

	closesAt, err := parseFormTime(form.Get("closes_at"))
	if err != nil || closesAt.IsZero() {
		return request, "Choose a closing date and time in Atlantic time."
	}
	if !closesAt.After(time.Now().UTC()) {
		return request, "The closing time must be in the future."
	}
	request.ClosesAt = closesAt
	if opensRaw := strings.TrimSpace(form.Get("opens_at")); opensRaw != "" {
		opensAt, err := parseFormTime(opensRaw)
		if err != nil {
			return request, "Choose a valid opening date and time in Atlantic time."
		}
		if !closesAt.After(opensAt) {
			return request, "The closing time must be after the opening time."
		}
		request.OpensAt = opensAt
	}

	// Read every outcome row the form carries, not a fixed number: an outright
	// can name the whole field. A match market is always exactly two sides.
	lastSlot := bettingpg.MaxSelectionsPerMarket
	if request.Type == betting.MarketMatch {
		lastSlot = 2
	}
	for slot := 1; slot <= lastSlot; slot++ {
		terms := strings.TrimSpace(form.Get(fmt.Sprintf("selection_terms_%d", slot)))
		oddsPrefix := "selection"
		if request.Type == betting.MarketMatch {
			terms = fmt.Sprintf("Match side %d", slot)
			oddsPrefix = "match"
		}
		oddsRaw := strings.TrimSpace(form.Get(fmt.Sprintf("%s_odds_%d", oddsPrefix, slot)))
		if terms == "" && oddsRaw == "" {
			continue
		}
		if terms == "" {
			return request, fmt.Sprintf("Outcome %d has odds but no name.", slot)
		}
		if oddsRaw == "" {
			return request, fmt.Sprintf("Enter American odds for outcome %d.", slot)
		}
		odds, err := parseAmericanOdds(form, oddsPrefix, slot)
		if err != nil {
			return request, fmt.Sprintf("Outcome %d odds must have a magnitude of at least 100, for example −110 or +150.", slot)
		}
		request.Selections = append(request.Selections, bettingpg.CreateMarketSelection{
			Key: fmt.Sprintf("outcome-%d", slot), DisplayTerms: terms, OfferedAmericanOdds: odds,
		})
	}
	if len(request.Selections) == 0 {
		return request, "Add at least one outcome and its odds."
	}
	if request.Type == betting.MarketMatch && len(request.Selections) != 2 {
		return request, "Enter prices for both sides of the match."
	}
	if request.DynamicPricing && len(request.Selections) < 2 {
		return request, "Dynamic pricing needs at least two outcomes. Add another outcome or turn dynamic pricing off."
	}
	return request, ""
}

func parseFormTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return webtime.ParseForm(value)
}

func parseAmericanOdds(form url.Values, prefix string, slot int) (int32, error) {
	raw := strings.TrimSpace(form.Get(fmt.Sprintf("%s_odds_%d", prefix, slot)))
	if raw == "" {
		return 0, ledger.ErrInvalidAmericanOdds
	}
	sign := strings.TrimSpace(form.Get(fmt.Sprintf("%s_sign_%d", prefix, slot)))
	if sign != "" && sign != "+" && sign != "-" {
		return 0, ledger.ErrInvalidAmericanOdds
	}
	if sign != "" && raw[0] != '+' && raw[0] != '-' {
		raw = sign + raw
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, err
	}
	if _, err := ledger.NewAmericanOdds(int32(parsed)); err != nil {
		return 0, err
	}
	return int32(parsed), nil
}

var stakePattern = regexp.MustCompile(`^\$?([0-9]{1,7})(?:\.([0-9]{1,2}))?$`)

// parseStakeCents converts a dollars-and-cents form value ("25", "25.5",
// "25.50") to integer cents without any floating-point arithmetic.
func parseStakeCents(value string) (int64, error) {
	match := stakePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, errors.New("stake must be a dollars-and-cents amount")
	}
	dollars, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, err
	}
	var cents int64
	if match[2] != "" {
		if cents, err = strconv.ParseInt(match[2], 10, 64); err != nil {
			return 0, err
		}
		if len(match[2]) == 1 {
			cents *= 10
		}
	}
	total := dollars*100 + cents
	if total <= 0 {
		return 0, errors.New("stake must be greater than zero")
	}
	return total, nil
}

// --- rendering -------------------------------------------------------------

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
	pages := map[string]string{
		"book_markets":        "templates/book_markets.gohtml",
		"book_wagers":         "templates/book_wagers.gohtml",
		"admin_markets":       "templates/admin_markets.gohtml",
		"admin_market_new":    "templates/admin_market_new.gohtml",
		"admin_market_settle": "templates/admin_market_settle.gohtml",
		"admin_wagers":        "templates/admin_wagers.gohtml",
		"admin_settle_up":     "templates/admin_settle_up.gohtml",
		"admin_wager_record":  "templates/admin_wager_record.gohtml",
		"admin_help":          "templates/admin_help.gohtml",
		"message":             "templates/betting_message.gohtml",
		"forbidden":           "templates/private_forbidden.gohtml",
	}
	result := make(map[string]*template.Template, len(pages))
	for name, file := range pages {
		tmpl, err := template.New("private_layout").Funcs(functions).ParseFS(
			publicassets.Files,
			"templates/private_layout.gohtml",
			"templates/betting_partials.gohtml",
			file,
		)
		if err != nil {
			return nil, fmt.Errorf("parse betting %s template: %w", name, err)
		}
		result[name] = tmpl
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
