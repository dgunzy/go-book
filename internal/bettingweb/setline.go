package bettingweb

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
)

// adminSetLine moves one selection's line by hand. It sets the opening line —
// the price the engine works from — rather than the offered price, so the move
// survives the next accepted wager instead of being recomputed away.
func (h *Handler) adminSetLine(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	marketID, selectionID := r.PathValue("id"), r.PathValue("selectionID")
	if !isUUID(marketID) || !isUUID(selectionID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested market was not found.", redirectAdminMarkets)
		return
	}
	odds, err := parseLineOdds(r.PostForm.Get("odds"))
	if err != nil {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Enter a line as American odds, at least +100 or at most -100.", redirectAdminMarkets)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say why the line is being moved (up to 500 characters) — it goes on the audit trail.", redirectAdminMarkets)
		return
	}

	changed, err := h.deps.Markets.SetOpeningLine(r.Context(), marketID, selectionID, odds, session.UserID, reason)
	if err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, bettingpg.ErrMarketNotPriceable) {
			status, text = http.StatusConflict, "Only a draft or open market's line can be moved."
		}
		h.failPost(w, r, session, status, text, redirectAdminMarkets)
		return
	}
	detail := "The board was already at that price."
	if changed {
		detail = "The rest of the board was repriced from the new line and the action already on it. Accepted wagers keep the odds they were filled at."
	}
	h.completePost(w, r, redirectAdminMarkets, "Line moved to "+formatOdds(odds)+".", detail)
}

// parseLineOdds accepts a line the way an admin writes one: -150, +120, or a
// bare 120 meaning +120.
func parseLineOdds(value string) (ledger.AmericanOdds, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "+")
	parsed, err := strconv.ParseInt(trimmed, 10, 32)
	if err != nil {
		return 0, err
	}
	return ledger.NewAmericanOdds(int32(parsed))
}

// adminSetCloseTime moves when a market stops taking action. Wagers already on
// it keep their odds and stand; this only changes how long new money can come
// in.
func (h *Handler) adminSetCloseTime(w http.ResponseWriter, r *http.Request) {
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
	closesAt, err := parseFormTime(strings.TrimSpace(r.PostForm.Get("closes_at")))
	if err != nil {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Choose a valid closing date and time in Atlantic time.", redirectAdminMarkets)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say why the closing time is moving (up to 500 characters) — it goes on the audit trail.", redirectAdminMarkets)
		return
	}

	if err := h.deps.Markets.SetMarketCloseTime(r.Context(), marketID, closesAt, session.UserID, reason); err != nil {
		status, text := storeErrorStatus(err)
		switch {
		case errors.Is(err, bettingpg.ErrCloseTimeInPast):
			status, text = http.StatusBadRequest,
				"That time has already passed. To stop taking action now, use Close."
		case errors.Is(err, bettingpg.ErrMarketNotPriceable):
			status, text = http.StatusConflict,
				"Only a draft or open market's closing time can be moved."
		}
		h.failPost(w, r, session, status, text, redirectAdminMarkets)
		return
	}
	h.completePost(w, r, redirectAdminMarkets, "Closing time moved.",
		"Wagers already on this market keep their odds; only new action is affected.")
}

// adminSetStakeLimit changes how much one member may have on a market, or on
// one side of it. An empty selection sets the market-wide cap.
func (h *Handler) adminSetStakeLimit(w http.ResponseWriter, r *http.Request) {
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
	selectionID := strings.TrimSpace(r.PostForm.Get("selection_id"))
	if selectionID != "" && !isUUID(selectionID) {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Choose a valid outcome, or set the limit for the whole market.", redirectAdminMarkets)
		return
	}
	// Blank clears the limit; anything else must be a real amount.
	var cents int64
	if raw := strings.TrimSpace(r.PostForm.Get("max_stake")); raw != "" {
		parsed, err := parseStakeCents(raw)
		if err != nil {
			h.failPost(w, r, session, http.StatusBadRequest,
				"Enter the limit as a dollars-and-cents amount, or leave it blank to remove it.", redirectAdminMarkets)
			return
		}
		cents = parsed
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say why the limit is changing (up to 500 characters) — it goes on the audit trail.", redirectAdminMarkets)
		return
	}

	if err := h.deps.Markets.SetStakeLimit(r.Context(), marketID, selectionID, cents, session.UserID, reason); err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, bettingpg.ErrMarketNotPriceable) {
			status, text = http.StatusConflict, "Only a draft or open market's limits can be changed."
		}
		h.failPost(w, r, session, status, text, redirectAdminMarkets)
		return
	}
	scope := "the whole market"
	if selectionID != "" {
		scope = "that outcome"
	}
	detail := "Wagers already on it stand; the limit applies to new money."
	if cents == 0 {
		h.completePost(w, r, redirectAdminMarkets, "Limit removed from "+scope+".", detail)
		return
	}
	h.completePost(w, r, redirectAdminMarkets,
		"Limit on "+scope+" set to $"+formatCentsDollars(cents)+" per member.", detail)
}
