package bettingweb

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/dgunzy/go-book/internal/privateweb"
)

// LedgerReader is the member ledger surface this handler needs. It is the same
// read privateweb uses for a member's own ledger, called here with another
// member's ID, so an admin can see the account they are acting on.
type LedgerReader interface {
	LedgerRows(context.Context, string) ([]privateweb.LedgerRow, error)
}

// memberBookPath is where an admin looks at one member's account.
func memberBookPath(userID string) string { return "/admin/members/" + userID + "/book" }

// adminMemberBook shows one member's standing — balance, credit, their wagers
// and ledger — and the board an admin can bet into on their behalf.
func (h *Handler) adminMemberBook(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")
	if !isUUID(userID) {
		h.renderStatus(w, http.StatusNotFound, "message", pageData{
			Title: "Member not found", Session: session,
			FormError: "The requested member was not found.", BackLink: "/admin/members",
		})
		return
	}

	book, err := h.deps.Members.MemberBook(r.Context(), userID, h.autoApproveMaxCents)
	if err != nil {
		status, text := storeErrorStatus(err)
		h.renderStatus(w, status, "message", pageData{
			Title: "Member not found", Session: session, FormError: text, BackLink: "/admin/members",
		})
		return
	}
	wagers, err := h.deps.Wagers.ListWagersForUser(r.Context(), userID)
	if err != nil {
		h.internalError(w)
		return
	}
	entries, err := h.deps.Ledger.LedgerRows(r.Context(), userID)
	if err != nil {
		h.internalError(w)
		return
	}
	// The board is scoped to the member, so a market or side they are
	// restricted from is not offered to bet on their behalf either.
	views, ok := h.openMarketViews(w, r.Context(), userID, true)
	if !ok {
		return
	}

	h.render(w, "admin_member_book", pageData{
		Title: book.Name, Current: "admin-members", Session: session,
		MemberBook: book, MemberWagers: memberWagerViews(wagers), LedgerRows: entries, Markets: views,
	})
}

// adminPlaceWagerForMember puts a bet on for a member. The wager belongs to
// them — their stake, their balance, their result — and the row records which
// admin placed it.
//
// It fills immediately rather than queueing for approval: the admin placing it
// is the book, so there is nobody left to approve it. If the member's funds and
// credit will not cover the stake the wager stays pending instead, and the
// admin is told why.
func (h *Handler) adminPlaceWagerForMember(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	userID := r.PathValue("id")
	if !isUUID(userID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested member was not found.", "/admin/members")
		return
	}
	marketID := r.PostForm.Get("market_id")
	selectionID := r.PostForm.Get("selection_id")
	if !isUUID(marketID) || !isUUID(selectionID) {
		h.failPost(w, r, session, http.StatusBadRequest,
			"The wager form was incomplete. Reload the page and try again.", memberBookPath(userID))
		return
	}
	stakeCents, err := parseStakeCents(r.PostForm.Get("stake"))
	if err != nil {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Enter the stake as dollars and cents, for example 25 or 25.50.", memberBookPath(userID))
		return
	}

	// The market must still be open to this member: the currency comes from
	// the database, and a market or side they are restricted from is not on
	// the list at all, so it cannot be bet for them here.
	markets, err := h.deps.Markets.ListOpenMarketsForUser(r.Context(), userID)
	if err != nil {
		h.internalError(w)
		return
	}
	var currency ledger.Currency
	found := false
	for _, market := range markets {
		if market.ID != marketID {
			continue
		}
		for _, selection := range market.Selections {
			if selection.ID == selectionID && selection.Active {
				currency, found = market.Currency, true
			}
		}
	}
	if !found {
		h.failPost(w, r, session, http.StatusConflict,
			"That market is not open to this member.", memberBookPath(userID))
		return
	}

	// Both IDs are generated here, never taken from the form, so a resubmitted
	// page cannot place the same bet twice.
	wagerID, err := h.newID()
	if err != nil {
		h.internalError(w)
		return
	}
	idempotencyKey, err := h.newID()
	if err != nil {
		h.internalError(w)
		return
	}

	wager, err := h.deps.Wagers.PlaceWager(r.Context(), bettingpg.PlaceWagerRequest{
		WagerID: wagerID, UserID: userID, MarketID: marketID, SelectionID: selectionID,
		FundingAccountType: betting.FundingUserCash, StakeCents: stakeCents, Currency: currency,
		IdempotencyKey: idempotencyKey, PlacedByUserID: session.UserID,
	})
	if err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, memberBookPath(userID))
		return
	}

	// An admin placing the bet is the book accepting it, so it fills now.
	accepted, acceptErr := h.deps.Wagers.AcceptWager(r.Context(), string(wager.ID), session.UserID)
	if acceptErr != nil {
		_, text := storeErrorStatus(acceptErr)
		h.completePost(w, r, memberBookPath(userID), "Wager placed, but not filled.",
			fmt.Sprintf("%s at %s for %s is waiting in the approval queue: %s",
				wager.AcceptedTerms, formatOdds(wager.AcceptedOdds), formatMoney(wager.Stake), text))
		return
	}
	h.completePost(w, r, memberBookPath(userID), "Wager placed for "+wagerOwnerName(r),
		fmt.Sprintf("%s at %s for %s. Filled and held in escrow; it is recorded as placed by you.",
			accepted.AcceptedTerms, formatOdds(accepted.AcceptedOdds), formatMoney(accepted.Stake)))
}

// wagerOwnerName is the member's name when the form carried it, for the
// confirmation message only — never for anything the store acts on.
func wagerOwnerName(r *http.Request) string {
	if name := r.PostForm.Get("member_name"); name != "" {
		return name
	}
	return "the member"
}
