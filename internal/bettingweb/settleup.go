package bettingweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
)

const redirectAdminSettleUp = "/admin/settle-up"

// settlementHistoryLimit caps the settle-up history on the page. The full
// record always remains in each member's ledger.
const settlementHistoryLimit = 50

// SettlementStore is the settle-up surface of bettingpg.Store: recording money
// that changed hands outside the app, and backing out a mistake.
type SettlementStore interface {
	RecordSettlement(context.Context, bettingpg.RecordSettlementRequest) (bettingpg.SettlementRow, error)
	ReverseSettlement(ctx context.Context, adjustmentID, actorUserID, reason string) (bettingpg.SettlementRow, error)
	ListSettlements(ctx context.Context, limit int) ([]bettingpg.SettlementRow, error)
	ListMemberBalances(context.Context, ledger.Currency) ([]bettingpg.MemberBalanceRow, error)
}

var _ SettlementStore = bettingpg.Store{}

// settleUpView is one member's outstanding position with the form defaults for
// squaring it: the direction is chosen for the admin from the sign of the
// balance, so nobody has to reason about which way the money goes.
type settleUpView struct {
	bettingpg.MemberBalanceRow
	// Direction is the settle-up that would clear this balance.
	Direction betting.AdjustmentDirection
	// Amount is the balance as a positive dollars-and-cents string.
	Amount string
}

// PaysTheBook reports whether clearing this balance means the member paying in.
func (v settleUpView) PaysTheBook() bool { return v.Direction == betting.AdjustmentPaymentReceived }

func settleUpViews(balances []bettingpg.MemberBalanceRow) []settleUpView {
	views := make([]settleUpView, 0, len(balances))
	for _, balance := range balances {
		view := settleUpView{MemberBalanceRow: balance}
		cents := balance.Balance.Cents
		if cents < 0 {
			// A negative balance is money the member owes, cleared by a payment in.
			view.Direction = betting.AdjustmentPaymentReceived
			cents = -cents
		} else {
			view.Direction = betting.AdjustmentPayoutSent
		}
		view.Amount = formatCentsDollars(cents)
		views = append(views, view)
	}
	return views
}

// adminSettleUp shows who owes what and the history of money already settled.
func (h *Handler) adminSettleUp(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	balances, err := h.deps.Settlements.ListMemberBalances(r.Context(), ledger.CAD)
	if err != nil {
		h.internalError(w)
		return
	}
	history, err := h.deps.Settlements.ListSettlements(r.Context(), settlementHistoryLimit)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "admin_settle_up", pageData{
		Title: "Settle up", Current: "admin-settle-up", Session: session,
		Outstanding: settleUpViews(balances), Settlements: history,
	})
}

// adminRecordSettlement writes a payment or payout to the ledger. It grades no
// wager and touches no escrow: it records that real money moved so the
// member's balance shows what is still outstanding.
func (h *Handler) adminRecordSettlement(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	userID := r.PostForm.Get("user_id")
	if !isUUID(userID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested member was not found.", redirectAdminSettleUp)
		return
	}
	direction := betting.AdjustmentDirection(r.PostForm.Get("direction"))
	if err := direction.Validate(); err != nil {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Choose whether the member paid the book or the book paid the member.", redirectAdminSettleUp)
		return
	}
	amountCents, err := parseStakeCents(r.PostForm.Get("amount"))
	if err != nil {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Enter the amount that changed hands as a dollars-and-cents figure greater than zero.", redirectAdminSettleUp)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say how the money moved (up to 500 characters) — this is the audit record.", redirectAdminSettleUp)
		return
	}
	// The ID is generated here rather than accepted from the form, so a
	// resubmitted page cannot be replayed into a second posting.
	adjustmentID, err := h.newID()
	if err != nil {
		h.internalError(w)
		return
	}

	recorded, err := h.deps.Settlements.RecordSettlement(r.Context(), bettingpg.RecordSettlementRequest{
		AdjustmentID: adjustmentID, UserID: userID, ActorUserID: session.UserID,
		Direction: direction, AmountCents: amountCents, Currency: ledger.CAD, Reason: reason,
	})
	if err != nil {
		status, text := storeErrorStatus(err)
		h.failPost(w, r, session, status, text, redirectAdminSettleUp)
		return
	}
	h.completePost(w, r, redirectAdminSettleUp, "Settlement recorded.",
		fmt.Sprintf("%s %s %s.", recorded.MemberName, settlementVerb(recorded.Direction), formatMoney(recorded.Amount)))
}

// adminReverseSettlement backs out a settle-up recorded in error. The original
// entry stays in the ledger; a correcting entry cancels it.
func (h *Handler) adminReverseSettlement(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	adjustmentID := r.PathValue("id")
	if !isUUID(adjustmentID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested settlement was not found.", redirectAdminSettleUp)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"A reversal needs a reason (up to 500 characters).", redirectAdminSettleUp)
		return
	}

	reversed, err := h.deps.Settlements.ReverseSettlement(r.Context(), adjustmentID, session.UserID, reason)
	if err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, bettingpg.ErrAlreadyReversed) {
			status, text = http.StatusConflict, "This settlement has already been reversed."
		}
		h.failPost(w, r, session, status, text, redirectAdminSettleUp)
		return
	}
	h.completePost(w, r, redirectAdminSettleUp, "Settlement reversed.",
		fmt.Sprintf("%s of %s was cancelled by a correcting entry; nothing was deleted.",
			settlementNoun(reversed.Direction), formatMoney(reversed.Amount)))
}

func settlementVerb(direction betting.AdjustmentDirection) string {
	if direction == betting.AdjustmentPayoutSent {
		return "was paid"
	}
	return "paid the book"
}

func settlementNoun(direction betting.AdjustmentDirection) string {
	if direction == betting.AdjustmentPayoutSent {
		return "A payout"
	}
	return "A payment"
}
