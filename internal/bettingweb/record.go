package bettingweb

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dgunzy/go-book/internal/betting"

	"github.com/dgunzy/go-book/internal/bettingpg"
	"github.com/dgunzy/go-book/internal/ledger"
)

// redirectAdminWagerRecord is where the record page's actions return to.
const redirectAdminWagerRecord = "/admin/wagers/record"

// closeVerdict says how the price a member took compares with where the line
// finished. It is the book's read on whether a bettor is beating it.
type closeVerdict string

const (
	// verdictPending means the market is still taking action, so there is no
	// closing line to judge against yet.
	verdictPending closeVerdict = "pending"
	// verdictBeat means the member took a better price than the close: the
	// bet looks sharp regardless of how it landed.
	verdictBeat closeVerdict = "beat"
	// verdictBehind means the line finished better than the price they took.
	verdictBehind closeVerdict = "behind"
	verdictLevel  closeVerdict = "level"
)

// wagerRecordView is one wager with its three prices and what they add up to.
type wagerRecordView struct {
	bettingpg.WagerRecordRow
	Verdict closeVerdict
	// ClosingProfit is what this stake would win at the closing line, and
	// Edge is the member's profit at the price they took less that figure:
	// positive means they got more than the close was paying.
	ClosingProfit ledger.Money
	Edge          ledger.Money
	// Drift is how far the line moved from open to close, in the member's
	// favour when positive.
	Drift ledger.Money
}

func (v wagerRecordView) BeatTheClose() bool { return v.Verdict == verdictBeat }
func (v wagerRecordView) BehindClose() bool  { return v.Verdict == verdictBehind }
func (v wagerRecordView) CloseIsFinal() bool { return v.Verdict != verdictPending }
func (v wagerRecordView) Settled() bool      { return v.Result != "" }

// CanVoid reports whether this wager can still be pulled back on its own. Only
// an accepted, ungraded wager can: a pending one is rejected instead, and a
// settled one has already paid.
func (v wagerRecordView) CanVoid() bool {
	return v.State == betting.WagerAccepted && v.Result == ""
}
func (v wagerRecordView) LineMovedOnOpen() bool {
	return v.OpeningOdds != v.ClosingOdds
}

func wagerRecordViews(rows []bettingpg.WagerRecordRow) []wagerRecordView {
	views := make([]wagerRecordView, 0, len(rows))
	for _, row := range rows {
		view := wagerRecordView{WagerRecordRow: row}
		if !row.MarketClosed() {
			// A line that is still moving is not a closing line, so the
			// comparison is deliberately withheld rather than guessed at.
			view.Verdict = verdictPending
			views = append(views, view)
			continue
		}
		switch compareLines(row.TakenOdds, row.ClosingOdds) {
		case lineToMember:
			view.Verdict = verdictBeat
		case lineToBook:
			view.Verdict = verdictBehind
		default:
			view.Verdict = verdictLevel
		}
		if closing, err := row.ClosingOdds.Profit(row.Stake); err == nil {
			view.ClosingProfit = closing
			view.Edge = difference(row.PotentialProfit, closing)
		}
		if opening, err := row.OpeningOdds.Profit(row.Stake); err == nil {
			if closing, err := row.ClosingOdds.Profit(row.Stake); err == nil {
				view.Drift = difference(closing, opening)
			}
		}
		views = append(views, view)
	}
	return views
}

// difference returns from less subtracted, or a zero amount when the
// subtraction cannot be represented.
func difference(from, subtracted ledger.Money) ledger.Money {
	negated, err := subtracted.Negate()
	if err != nil {
		return ledger.Money{Currency: from.Currency}
	}
	result, err := from.Add(negated)
	if err != nil {
		return ledger.Money{Currency: from.Currency}
	}
	return result
}

// adminWagerRecord shows every wager the book has taken with the line it
// opened at, the price the member took, and where it closed.
func (h *Handler) adminWagerRecord(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	rows, err := h.deps.Wagers.ListWagerRecord(r.Context(), 0)
	if err != nil {
		h.internalError(w)
		return
	}
	h.render(w, "admin_wager_record", pageData{
		Title: "Wager record", Current: "admin-wagers", Session: session,
		WagerRecord: wagerRecordViews(rows),
	})
}

// adminVoidWager cancels one accepted wager and returns its stake. It is the
// single-bet counterpart to voiding a market: everyone else's action stands.
func (h *Handler) adminVoidWager(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.checkedForm(w, r, session) {
		return
	}
	wagerID := r.PathValue("id")
	if !isUUID(wagerID) {
		h.failPost(w, r, session, http.StatusNotFound, "The requested wager was not found.", redirectAdminWagerRecord)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		h.failPost(w, r, session, http.StatusBadRequest,
			"Say why this wager is being voided (up to 500 characters) — the member sees it on their ledger.", redirectAdminWagerRecord)
		return
	}

	voided, err := h.deps.Wagers.VoidWager(r.Context(), wagerID, session.UserID, reason)
	if err != nil {
		status, text := storeErrorStatus(err)
		if errors.Is(err, betting.ErrInvalidTransition) {
			text = "Only an accepted wager can be voided. A pending one is rejected instead, and a settled one needs the market re-settled."
		}
		h.failPost(w, r, session, status, text, redirectAdminWagerRecord)
		return
	}
	h.completePost(w, r, redirectAdminWagerRecord, "Wager voided.",
		fmt.Sprintf("%s went back to the member. Every other wager on the market stands.", formatMoney(voided.Stake)))
}
