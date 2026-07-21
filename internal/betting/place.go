package betting

import (
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
)

// PlaceWagerCommand is the pure input required to accept a new pending
// wager. The caller resolves the market, selection, and restriction list
// from storage; this function performs no I/O.
type PlaceWagerCommand struct {
	WagerID            ID
	UserID             ID
	Market             Market
	Selection          Selection
	RestrictedUsers    []ID
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
	for _, restricted := range command.RestrictedUsers {
		if restricted == command.UserID {
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
// auto-approval). currentOdds is the line currently offered for the wager's
// selection; if it differs from the odds snapshotted when the wager was
// placed, the line moved while the wager was pending and ErrOddsMoved is
// returned so the caller can invalidate the stale bet rather than accept it at
// a price no longer on offer.
func AcceptWager(wager Wager, actor ID, occurredAt time.Time, refs AcceptanceAccountRefs, currentOdds ledger.AmericanOdds, eventID ID) (AcceptWagerResult, error) {
	if err := wager.Validate(); err != nil {
		return AcceptWagerResult{}, err
	}
	if !wager.State.CanTransitionTo(WagerAccepted) {
		return AcceptWagerResult{}, transitionErr("accept wager", string(wager.State))
	}
	if !validID(actor) {
		return AcceptWagerResult{}, ErrUnauthorized
	}
	if wager.AcceptedOdds != currentOdds {
		return AcceptWagerResult{}, ErrOddsMoved
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
