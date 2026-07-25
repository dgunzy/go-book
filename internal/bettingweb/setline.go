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
