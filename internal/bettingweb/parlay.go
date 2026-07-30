package bettingweb

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
)

// parlaySlipView is the live slip: what is selected, what it prices at, and
// what it would return. It is a view model rather than a stored parlay because
// a slip is only a question until the member places it.
type parlaySlipView struct {
	Legs        int
	LegRows     []parlayLegView
	Priced      bool
	Odds        ledger.AmericanOdds
	StakeInput  string
	StakeCents  int64
	ProfitCents int64
	ReturnCents int64
	// Notice explains why there is no price yet, in the member's words.
	Notice string
}

// OddsLabel renders the combined price the way the rest of the board does.
func (v parlaySlipView) OddsLabel() string { return formatOdds(v.Odds) }

// StakeDollars, ProfitDollars and ReturnDollars format money for the slip.
func (v parlaySlipView) StakeDollars() string  { return formatCentsDollars(v.StakeCents) }
func (v parlaySlipView) ProfitDollars() string { return formatCentsDollars(v.ProfitCents) }
func (v parlaySlipView) ReturnDollars() string { return formatCentsDollars(v.ReturnCents) }

type parlayLegView struct {
	MarketTitle string
	Terms       string
	Odds        ledger.AmericanOdds
}

func (l parlayLegView) OddsLabel() string { return formatOdds(l.Odds) }

// parlayLegSeparator joins a market and selection into one form value, so the
// slip can be built from plain checkboxes without any client-side state.
const parlayLegSeparator = ":"

// parseParlayLegs reads the checked legs. Each value is "<marketID>:<selectionID>";
// anything malformed is dropped rather than guessed at, and a duplicated market
// is refused outright because correlated legs are how a book gets picked off.
func parseParlayLegs(values []string) ([]bettingpg.ParlayLegRequest, error) {
	legs := make([]bettingpg.ParlayLegRequest, 0, len(values))
	seenMarket := make(map[string]bool, len(values))
	for _, value := range values {
		marketID, selectionID, found := strings.Cut(strings.TrimSpace(value), parlayLegSeparator)
		if !found || !isUUID(marketID) || !isUUID(selectionID) {
			continue
		}
		if seenMarket[marketID] {
			return nil, betting.ErrParlayDuplicateMarket
		}
		seenMarket[marketID] = true
		legs = append(legs, bettingpg.ParlayLegRequest{MarketID: marketID, SelectionID: selectionID})
	}
	return legs, nil
}

// parlayRequestFrom builds the store request shared by quoting and placing, so
// a quote can never be priced under different rules than the placement that
// follows it.
func (h *Handler) parlayRequestFrom(r *http.Request, userID string) (bettingpg.PlaceParlayRequest, error) {
	legs, err := parseParlayLegs(r.PostForm["leg"])
	if err != nil {
		return bettingpg.PlaceParlayRequest{}, err
	}
	stakeCents, err := parseStakeCents(strings.TrimSpace(r.PostForm.Get("parlay_stake")))
	if err != nil {
		return bettingpg.PlaceParlayRequest{}, err
	}
	return bettingpg.PlaceParlayRequest{
		UserID: userID, Legs: legs,
		FundingAccountType: betting.FundingUserCash,
		StakeCents:         stakeCents, Currency: ledger.CAD,
	}, nil
}

// bookQuoteParlay prices the slip as it stands without storing anything. It is
// what makes the combined price visible before a member commits to it.
func (h *Handler) bookQuoteParlay(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}

	view := parlaySlipView{StakeInput: strings.TrimSpace(r.PostForm.Get("parlay_stake"))}
	request, err := h.parlayRequestFrom(r, session.UserID)
	switch {
	case errors.Is(err, betting.ErrParlayDuplicateMarket):
		view.Notice = "Two legs on the same match cannot be parlayed — pick one side of each."
		h.renderSlip(w, view)
		return
	case err != nil:
		// No stake typed yet is the ordinary state of a half-built slip, not an
		// error worth shouting about.
		view.Legs = len(r.PostForm["leg"])
		view.Notice = "Enter a stake to see the price."
		h.renderSlip(w, view)
		return
	}

	view.Legs = len(request.Legs)
	if view.Legs < betting.MinParlayLegs {
		view.Notice = "Pick at least two matches to build a parlay."
		h.renderSlip(w, view)
		return
	}

	quote, err := h.deps.Parlays.QuoteParlay(r.Context(), request)
	if err != nil {
		_, text := storeErrorStatus(err)
		view.Notice = text
		h.renderSlip(w, view)
		return
	}
	view.Priced = true
	view.Odds = quote.AcceptedOdds
	view.StakeCents = quote.StakeCents
	view.ProfitCents = quote.PotentialProfit
	view.ReturnCents = quote.StakeCents + quote.PotentialProfit
	for _, leg := range quote.Legs {
		view.LegRows = append(view.LegRows, parlayLegView{
			MarketTitle: leg.MarketTitle, Terms: leg.AcceptedTerms, Odds: leg.AcceptedOdds,
		})
	}
	h.renderSlip(w, view)
}

func (h *Handler) renderSlip(w http.ResponseWriter, view parlaySlipView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.templates["message"].ExecuteTemplate(w, "parlay_slip", view)
}

// bookPlaceParlay stores the slip as a pending parlay. It is never accepted
// here: every parlay waits for an admin, whatever its stake.
func (h *Handler) bookPlaceParlay(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	request, err := h.parlayRequestFrom(r, session.UserID)
	if err != nil {
		message := "Enter the stake as a dollars-and-cents amount."
		if errors.Is(err, betting.ErrParlayDuplicateMarket) {
			message = "Two legs on the same match cannot be parlayed — pick one side of each."
		}
		h.failPost(w, r, session, http.StatusBadRequest, message, "/book/markets")
		return
	}
	parlayID, err := h.newID()
	if err != nil {
		h.internalError(w)
		return
	}
	request.ParlayID = parlayID
	request.IdempotencyKey = strings.TrimSpace(r.PostForm.Get("idempotency_key"))
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = "parlay:" + parlayID
	}

	placed, err := h.deps.Parlays.PlaceParlay(r.Context(), request)
	if err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, "/book/markets")
		return
	}
	h.completePost(w, r, redirectBookWagers,
		"Parlay submitted for review.",
		"Every parlay is checked by an admin before the book takes it, so nothing has left your balance yet. "+
			formatOdds(placed.AcceptedOdds)+" on "+pluralLegs(len(placed.Legs))+".")
}

// bookCancelParlay withdraws the member's own parlay while it still waits for
// review.
func (h *Handler) bookCancelParlay(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMember(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	parlayID := r.PathValue("id")
	if !isUUID(parlayID) {
		h.failPost(w, r, session, http.StatusNotFound, "That parlay was not found.", redirectBookWagers)
		return
	}
	if err := h.deps.Parlays.CancelParlay(r.Context(), parlayID, session.UserID); err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, betting.ErrInvalidTransition) {
			status, text = http.StatusConflict,
				"The book has already taken this parlay, so it has to be graded rather than withdrawn."
		}
		h.failPost(w, r, session, status, text, redirectBookWagers)
		return
	}
	h.completePost(w, r, redirectBookWagers, "Parlay withdrawn.", "Nothing had left your balance.")
}

// adminAcceptParlay takes a parlay onto the book. Review is mandatory for
// parlays, so this is the only way one is ever accepted.
func (h *Handler) adminAcceptParlay(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	parlayID := r.PathValue("id")
	if !isUUID(parlayID) {
		h.failPost(w, r, session, http.StatusNotFound, "That parlay was not found.", redirectAdminWagers)
		return
	}
	accepted, err := h.deps.Parlays.AcceptParlay(r.Context(), parlayID, session.UserID)
	if err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, bettingpg.ErrMarketDecided) {
			status, text = http.StatusConflict,
				"One of its matches has already been settled, so this parlay can no longer be taken fairly. Reject it instead."
		}
		h.failPost(w, r, session, status, text, redirectAdminWagers)
		return
	}
	h.completePost(w, r, redirectAdminWagers, "Parlay accepted.",
		"The stake is held in escrow at "+formatOdds(accepted.AcceptedOdds)+".")
}

// adminRejectParlay refuses a parlay. Nothing has been debited, so there is
// nothing to give back.
func (h *Handler) adminRejectParlay(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	parlayID := r.PathValue("id")
	if !isUUID(parlayID) {
		h.failPost(w, r, session, http.StatusNotFound, "That parlay was not found.", redirectAdminWagers)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say why the parlay is being refused (up to 500 characters) — the member sees it.", redirectAdminWagers)
		return
	}
	if err := h.deps.Parlays.RejectParlay(r.Context(), parlayID, session.UserID, reason); err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, redirectAdminWagers)
		return
	}
	h.completePost(w, r, redirectAdminWagers, "Parlay rejected.", "Nothing left the member's balance.")
}

func pluralLegs(count int) string {
	if count == 1 {
		return "1 leg"
	}
	return strconv.Itoa(count) + " legs"
}
