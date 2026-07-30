package betting

import (
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
)

// Restriction bars one member from betting a market, or from one side of it.
// An empty SelectionID means the whole market: they cannot back anything on
// it. A set SelectionID bars only that outcome — the player a prop is about
// may be kept off one side of their own line while the rest stays open.
type Restriction struct {
	UserID      ID
	SelectionID ID
	Reason      string
}

// Restricts reports whether this restriction bars the given member from the
// given selection.
func (r Restriction) Restricts(userID, selectionID ID) bool {
	if r.UserID != userID {
		return false
	}
	return r.SelectionID == "" || r.SelectionID == selectionID
}

// PlaceWagerCommand is the pure input required to accept a new pending
// wager. The caller resolves the market, selection, and restriction list
// from storage; this function performs no I/O.
type PlaceWagerCommand struct {
	WagerID      ID
	UserID       ID
	Market       Market
	Selection    Selection
	Restrictions []Restriction
	// MaxStakeCents caps what one member may have riding on this market at
	// once, counting the wagers they already hold on it. Zero means no cap.
	MaxStakeCents int64
	// ExistingStakeCents is what this member already has on the market,
	// pending and accepted together.
	ExistingStakeCents int64
	// SelectionMaxStakeCents caps what one member may have on this side of
	// the market, and ExistingSelectionStakeCents is what they already have on
	// it. A lopsided prop often wants a tight limit on the long side and none
	// on the short one. Zero means no cap on the side.
	SelectionMaxStakeCents      int64
	ExistingSelectionStakeCents int64
	// TotalStakeCapCents caps what every member together may have on this side,
	// and ExistingTotalStakeCents is what the book already holds on it. The
	// per-member caps above bound one person; without this, ten people at the
	// per-member limit is ten times the liability the book agreed to. Zero
	// means no cap.
	TotalStakeCapCents      int64
	ExistingTotalStakeCents int64
	// MaxPayoutCents is the book-wide ceiling on what a single wager may
	// return in profit. It exists so a longshot price cannot turn a small
	// stake into a payout the book cannot cover. Zero means no ceiling.
	MaxPayoutCents int64
	// PlacedBy is the admin putting this wager on for the member. It is empty
	// when the member placed it themselves, which is the ordinary case.
	PlacedBy           ID
	FundingAccountType FundingAccountType
	Stake              ledger.Money
	IdempotencyKey     string
	Now                time.Time
}

// PlaceWager validates market/selection eligibility and returns a new
// pending Wager with an odds and terms snapshot taken from the selection at
// placement time.
func PlaceWager(command PlaceWagerCommand) (Wager, error) {
	if !validID(command.WagerID) || !validID(command.UserID) {
		return Wager{}, invalidf("wager placement requires a wager ID and user ID")
	}
	if err := command.Market.Validate(); err != nil {
		return Wager{}, err
	}
	if err := command.Selection.Validate(); err != nil {
		return Wager{}, err
	}
	if command.Selection.MarketID != command.Market.ID {
		return Wager{}, ErrSelectionMismatch
	}
	if command.Now.IsZero() {
		return Wager{}, invalidf("wager placement requires the current time")
	}

	now := command.Now.UTC()
	if command.Market.State != MarketOpen {
		return Wager{}, ErrMarketNotOpen
	}
	if !command.Market.OpensAt.IsZero() && now.Before(command.Market.OpensAt) {
		return Wager{}, ErrMarketNotOpen
	}
	if !now.Before(command.Market.ClosesAt) {
		return Wager{}, ErrMarketNotOpen
	}
	if !command.Selection.Active {
		return Wager{}, ErrSelectionInactive
	}
	for _, restriction := range command.Restrictions {
		if restriction.Restricts(command.UserID, command.Selection.ID) {
			return Wager{}, ErrUserRestricted
		}
	}
	if err := command.FundingAccountType.Validate(); err != nil {
		return Wager{}, err
	}
	if err := command.Stake.Validate(); err != nil {
		return Wager{}, err
	}
	if command.Stake.Cents <= 0 {
		return Wager{}, invalidf("stake must be greater than zero")
	}
	if command.Stake.Currency != command.Market.Currency {
		return Wager{}, ledger.ErrCurrencyMismatch
	}
	// A market's stake cap is a limit on the member, not on the bet: spreading
	// the money over several wagers must not get around it.
	if command.MaxStakeCents > 0 &&
		command.ExistingStakeCents+command.Stake.Cents > command.MaxStakeCents {
		return Wager{}, ErrStakeAboveLimit
	}
	// A side may carry its own, tighter limit. Both apply when both are set.
	if command.SelectionMaxStakeCents > 0 &&
		command.ExistingSelectionStakeCents+command.Stake.Cents > command.SelectionMaxStakeCents {
		return Wager{}, ErrStakeAboveLimit
	}
	// The book's own ceiling on this side. This is not the member's limit and
	// must not be reported as one: they may be nowhere near their own cap and
	// still be turned away because the side is full.
	if command.TotalStakeCapCents > 0 &&
		command.ExistingTotalStakeCents+command.Stake.Cents > command.TotalStakeCapCents {
		return Wager{}, ErrSideFull
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" {
		return Wager{}, invalidf("wager placement requires an idempotency key")
	}

	profit, err := command.Selection.OfferedAmericanOdds.Profit(command.Stake)
	if err != nil {
		return Wager{}, fmt.Errorf("compute potential profit: %w", err)
	}
	if profit.Cents <= 0 {
		return Wager{}, invalidf("stake is too small to win at least one cent at the offered odds")
	}
	// The payout ceiling is about what the book would owe, not what the member
	// puts up: a small stake at a long price can still be a large liability.
	if command.MaxPayoutCents > 0 && profit.Cents > command.MaxPayoutCents {
		return Wager{}, ErrPayoutAboveLimit
	}

	wager := Wager{
		ID:                 command.WagerID,
		UserID:             command.UserID,
		MarketID:           command.Market.ID,
		SelectionID:        command.Selection.ID,
		FundingAccountType: command.FundingAccountType,
		Stake:              command.Stake,
		AcceptedOdds:       command.Selection.OfferedAmericanOdds,
		AcceptedTerms:      command.Selection.DisplayTerms,
		PotentialProfit:    profit,
		State:              WagerPending,
		IdempotencyKey:     strings.TrimSpace(command.IdempotencyKey),
		PlacedBy:           command.PlacedBy,
		PlacedAt:           now,
	}
	if err := wager.Validate(); err != nil {
		return Wager{}, err
	}
	return wager, nil
}

// AcceptanceAccountRefs supplies the ledger account IDs the domain does not
// itself know. UserFundingAccountID must resolve to the account matching the
// wager's funding account type (cash or free play) for the wagering user.
type AcceptanceAccountRefs struct {
	UserFundingAccountID string
	EscrowAccountID      string
}

func (r AcceptanceAccountRefs) validate() error {
	if strings.TrimSpace(r.UserFundingAccountID) == "" || strings.TrimSpace(r.EscrowAccountID) == "" {
		return invalidf("wager acceptance requires user funding and escrow account references")
	}
	if r.UserFundingAccountID == r.EscrowAccountID {
		return invalidf("user funding and escrow accounts must be distinct")
	}
	return nil
}

// AcceptWagerResult bundles the updated wager with the balanced ledger
// transaction and domain event the caller must persist atomically alongside
// it.
type AcceptWagerResult struct {
	Wager       Wager
	Transaction ledger.Transaction
	Event       events.Envelope
}

// AcceptWager moves a pending wager to accepted. The stake moves from the
// user's funding account to a shared escrow account in one balanced
// transaction. Actor is the approving admin's user ID (or a system actor for
// auto-approval). The wager always fills at the odds snapshotted when it was
// placed, even if the line has since moved: whether a stale price is still
// worth taking is the approving admin's call, made from the live line shown in
// the review queue.
func AcceptWager(wager Wager, actor ID, occurredAt time.Time, refs AcceptanceAccountRefs, eventID ID) (AcceptWagerResult, error) {
	if err := wager.Validate(); err != nil {
		return AcceptWagerResult{}, err
	}
	if !wager.State.CanTransitionTo(WagerAccepted) {
		return AcceptWagerResult{}, transitionErr("accept wager", string(wager.State))
	}
	if !validID(actor) {
		return AcceptWagerResult{}, ErrUnauthorized
	}
	if err := refs.validate(); err != nil {
		return AcceptWagerResult{}, err
	}
	if occurredAt.IsZero() {
		return AcceptWagerResult{}, invalidf("wager acceptance requires an occurrence time")
	}
	if !validID(eventID) {
		return AcceptWagerResult{}, invalidf("wager acceptance requires an event ID")
	}

	at := occurredAt.UTC()
	negatedStake, err := wager.Stake.Negate()
	if err != nil {
		return AcceptWagerResult{}, err
	}

	txn := ledger.Transaction{
		Type:           ledger.TransactionWagerAcceptance,
		Currency:       wager.Stake.Currency,
		IdempotencyKey: fmt.Sprintf("wager:%s:acceptance", wager.ID),
		Actor:          string(actor),
		SourceType:     "wager",
		SourceID:       string(wager.ID),
		Postings: []ledger.Posting{
			{AccountID: refs.UserFundingAccountID, Amount: negatedStake},
			{AccountID: refs.EscrowAccountID, Amount: wager.Stake},
		},
	}
	if err := txn.Validate(); err != nil {
		return AcceptWagerResult{}, fmt.Errorf("build acceptance transaction: %w", err)
	}

	payload := wagerAcceptedPayload{
		WagerID:              string(wager.ID),
		UserID:               string(wager.UserID),
		MarketID:             string(wager.MarketID),
		SelectionID:          string(wager.SelectionID),
		StakeCents:           wager.Stake.Cents,
		Currency:             string(wager.Stake.Currency),
		AcceptedAmericanOdds: int32(wager.AcceptedOdds),
		PotentialProfitCents: wager.PotentialProfit.Cents,
	}
	envelope, err := buildEnvelope(eventID, wager.ID, "wager", 1, events.WagerAccepted, at, payload)
	if err != nil {
		return AcceptWagerResult{}, err
	}

	wager.State = WagerAccepted
	return AcceptWagerResult{Wager: wager, Transaction: txn, Event: envelope}, nil
}

// RejectWager moves a pending wager to rejected. No funds ever moved, so no
// ledger transaction is produced.
func RejectWager(wager Wager, actor ID, reason string) (Wager, error) {
	if err := wager.Validate(); err != nil {
		return Wager{}, err
	}
	if !wager.State.CanTransitionTo(WagerRejected) {
		return Wager{}, transitionErr("reject wager", string(wager.State))
	}
	if !validID(actor) {
		return Wager{}, ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Wager{}, ErrReasonRequired
	}
	wager.State = WagerRejected
	return wager, nil
}

// CancelWagerReason is recorded as the rejection reason when a member pulls
// their own pending wager. A cancellation is a rejection the bettor asked for:
// the wager never reached escrow, so no funds move either way.
const CancelWagerReason = "Cancelled by the member before acceptance"

// CancelWager lets the member who placed a wager withdraw it while it is still
// pending — for instance when the line moved against them while they waited
// for the book. Only the wager's own owner may cancel it, and only before the
// book accepts it: once accepted the stake is in escrow and only the book can
// unwind it.
func CancelWager(wager Wager, actor ID) (Wager, error) {
	if err := wager.Validate(); err != nil {
		return Wager{}, err
	}
	if !validID(actor) || actor != wager.UserID {
		return Wager{}, ErrUnauthorized
	}
	if !wager.State.CanTransitionTo(WagerRejected) {
		return Wager{}, transitionErr("cancel wager", string(wager.State))
	}
	wager.State = WagerRejected
	return wager, nil
}
