package bettingweb

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
)

// adminRestrictMember bars a member from a market, or from one side of it.
// The player a prop is about is the usual case: they may be kept off one side
// of their own line while the rest of the board stays open to them.
func (h *Handler) adminRestrictMember(w http.ResponseWriter, r *http.Request) {
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
	userID := strings.TrimSpace(r.PostForm.Get("user_id"))
	if !isUUID(userID) {
		h.failPost(w, r, session, http.StatusBadRequest, "Choose the member to restrict.", redirectAdminMarkets)
		return
	}
	// An empty selection means the whole market.
	selectionID := strings.TrimSpace(r.PostForm.Get("selection_id"))
	if selectionID != "" && !isUUID(selectionID) {
		h.failPost(w, r, session, http.StatusBadRequest, "Choose a valid outcome, or restrict the whole market.", redirectAdminMarkets)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say why this member is restricted (up to 500 characters) — it goes on the audit trail.", redirectAdminMarkets)
		return
	}

	err := h.deps.Markets.RestrictMember(r.Context(), bettingpg.RestrictRequest{
		MarketID: marketID, UserID: userID, SelectionID: selectionID,
		Reason: reason, ActorUserID: session.UserID,
	})
	if err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, redirectAdminMarkets)
		return
	}
	detail := "They will not see this market at all, and a wager on it is refused."
	if selectionID != "" {
		detail = "That outcome is hidden from them, and a wager on it is refused. The rest of the market stays open to them."
	}
	h.completePost(w, r, redirectAdminMarkets, "Member restricted.", detail)
}

// adminLiftRestriction removes one restriction, putting the market or side
// back in front of that member.
func (h *Handler) adminLiftRestriction(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	marketID := r.PathValue("id")
	userID := strings.TrimSpace(r.PostForm.Get("user_id"))
	if !isUUID(marketID) || !isUUID(userID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested restriction was not found.", redirectAdminMarkets)
		return
	}
	selectionID := strings.TrimSpace(r.PostForm.Get("selection_id"))

	if err := h.deps.Markets.LiftRestriction(r.Context(), marketID, userID, selectionID); err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, betting.ErrNotFound) {
			status, text = http.StatusNotFound, "That restriction is no longer in place."
		}
		h.failPost(w, r, session, status, text, redirectAdminMarkets)
		return
	}
	h.completePost(w, r, redirectAdminMarkets, "Restriction lifted.", "They can bet this again.")
}
