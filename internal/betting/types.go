// Package betting contains the pure domain model for Cabot Cup betting
// markets, selections, and wagers. It deliberately has no SQL, HTTP, or
// identity-provider dependencies; it may depend on internal/ledger and
// internal/events.
package betting

import (
	"regexp"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// ID is an opaque identifier supplied by the caller (a UUID in production).
type ID string

func validID(id ID) bool { return strings.TrimSpace(string(id)) != "" }

var selectionKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

type MarketType string

const (
	MarketMatch  MarketType = "match"
	MarketFuture MarketType = "future"
	MarketProp   MarketType = "prop"
)

func (t MarketType) Validate() error {
	switch t {
	case MarketMatch, MarketFuture, MarketProp:
		return nil
	default:
		return invalidf("market type %q is not supported", t)
	}
}

// MarketState mirrors the markets.state CHECK constraint. CanTransitionTo
// enforces only the documented forward transitions; the settlement and void
// commands in settle.go are explicit corrections that bypass it, matching the
// pattern used by AdminOverride and CorrectResult in internal/competition.
type MarketState string

const (
	MarketDraft             MarketState = "draft"
	MarketOpen              MarketState = "open"
	MarketClosed            MarketState = "closed"
	MarketSettlementPending MarketState = "settlement_pending"
	MarketSettled           MarketState = "settled"
	MarketVoided            MarketState = "voided"
	MarketCancelled         MarketState = "cancelled"
)

func (s MarketState) Validate() error {
	switch s {
	case MarketDraft, MarketOpen, MarketClosed, MarketSettlementPending, MarketSettled, MarketVoided, MarketCancelled:
		return nil
	default:
		return invalidf("market state %q is not supported", s)
	}
}

var marketTransitions = map[MarketState]map[MarketState]bool{
	MarketDraft:             {MarketOpen: true, MarketCancelled: true},
	MarketOpen:              {MarketClosed: true, MarketCancelled: true},
	MarketClosed:            {MarketVoided: true, MarketSettlementPending: true},
	MarketSettlementPending: {MarketSettled: true, MarketVoided: true},
}

func (s MarketState) CanTransitionTo(to MarketState) bool {
	return marketTransitions[s][to]
}

// WagerState mirrors the wagers.state CHECK constraint.
type WagerState string

const (
	WagerPending  WagerState = "pending"
	WagerAccepted WagerState = "accepted"
	WagerRejected WagerState = "rejected"
	WagerSettled  WagerState = "settled"
	WagerVoided   WagerState = "voided"
)

func (s WagerState) Validate() error {
	switch s {
	case WagerPending, WagerAccepted, WagerRejected, WagerSettled, WagerVoided:
		return nil
	default:
		return invalidf("wager state %q is not supported", s)
	}
}

var wagerTransitions = map[WagerState]map[WagerState]bool{
	WagerPending:  {WagerAccepted: true, WagerRejected: true},
	WagerAccepted: {WagerSettled: true, WagerVoided: true},
}

func (s WagerState) CanTransitionTo(to WagerState) bool {
	return wagerTransitions[s][to]
}

// SettlementResult mirrors the result columns on market_settlement_outcomes
// and wager_settlements.
type SettlementResult string

const (
	ResultWin  SettlementResult = "win"
	ResultLoss SettlementResult = "loss"
	ResultPush SettlementResult = "push"
	ResultVoid SettlementResult = "void"
)

func (r SettlementResult) Validate() error {
	switch r {
	case ResultWin, ResultLoss, ResultPush, ResultVoid:
		return nil
	default:
		return invalidf("settlement result %q is not supported", r)
	}
}

// FundingAccountType mirrors wagers.funding_account_type.
type FundingAccountType string

const (
	FundingUserCash     FundingAccountType = "user_cash"
	FundingUserFreePlay FundingAccountType = "user_free_play"
)

func (f FundingAccountType) Validate() error {
	switch f {
	case FundingUserCash, FundingUserFreePlay:
		return nil
	default:
		return invalidf("funding account type %q is not supported", f)
	}
}

// Market is a bettable event grouping. MatchID is required for match markets
// and forbidden otherwise, mirroring the markets table CHECK constraint.
// OpensAt is optional; a zero value means the market has no explicit open
// bound and is only gated by ClosesAt and State.
type Market struct {
	ID       ID
	Type     MarketType
	MatchID  ID
	Title    string
	State    MarketState
	Currency ledger.Currency
	OpensAt  time.Time
	ClosesAt time.Time
}

func (m Market) Validate() error {
	if !validID(m.ID) {
		return invalidf("market requires an ID")
	}
	if err := m.Type.Validate(); err != nil {
		return err
	}
	if m.Type == MarketMatch && !validID(m.MatchID) {
		return invalidf("match markets require a match ID")
	}
	if m.Type != MarketMatch && validID(m.MatchID) {
		return invalidf("non-match markets cannot reference a match ID")
	}
	title := strings.TrimSpace(m.Title)
	if title == "" || len(title) > 200 {
		return invalidf("market title must be between 1 and 200 characters")
	}
	if err := m.State.Validate(); err != nil {
		return err
	}
	if err := m.Currency.Validate(); err != nil {
		return err
	}
	if m.ClosesAt.IsZero() {
		return invalidf("market requires a close time")
	}
	if !m.OpensAt.IsZero() && !m.ClosesAt.After(m.OpensAt) {
		return invalidf("market close time must be after its open time")
	}
	return nil
}

// Selection is one bettable outcome offered within a market.
type Selection struct {
	ID                  ID
	MarketID            ID
	Key                 string
	DisplayTerms        string
	OfferedAmericanOdds ledger.AmericanOdds
	SemanticResultKey   string
	Active              bool
}

func (s Selection) Validate() error {
	if !validID(s.ID) || !validID(s.MarketID) {
		return invalidf("selection requires an ID and market ID")
	}
	if !selectionKeyPattern.MatchString(s.Key) {
		return invalidf("selection key %q is not valid", s.Key)
	}
	terms := strings.TrimSpace(s.DisplayTerms)
	if terms == "" || len(terms) > 500 {
		return invalidf("selection display terms must be between 1 and 500 characters")
	}
	if err := s.OfferedAmericanOdds.Validate(); err != nil {
		return err
	}
	return nil
}

// Wager records one member's accepted or pending bet. AcceptedOdds,
// AcceptedTerms, and PotentialProfit are immutable snapshots taken at
// placement time; later edits to the market or selection must never change
// them.
type Wager struct {
	ID                 ID
	UserID             ID
	MarketID           ID
	SelectionID        ID
	FundingAccountType FundingAccountType
	Stake              ledger.Money
	AcceptedOdds       ledger.AmericanOdds
	AcceptedTerms      string
	PotentialProfit    ledger.Money
	State              WagerState
	IdempotencyKey     string
	PlacedAt           time.Time
}

func (w Wager) Validate() error {
	if !validID(w.ID) || !validID(w.UserID) || !validID(w.MarketID) || !validID(w.SelectionID) {
		return invalidf("wager requires ID, user, market, and selection references")
	}
	if err := w.FundingAccountType.Validate(); err != nil {
		return err
	}
	if err := w.Stake.Validate(); err != nil {
		return err
	}
	if w.Stake.Cents <= 0 {
		return invalidf("wager stake must be greater than zero")
	}
	if err := w.AcceptedOdds.Validate(); err != nil {
		return err
	}
	terms := strings.TrimSpace(w.AcceptedTerms)
	if terms == "" || len(terms) > 1000 {
		return invalidf("accepted terms must be between 1 and 1000 characters")
	}
	if err := w.PotentialProfit.Validate(); err != nil {
		return err
	}
	if w.PotentialProfit.Cents < 0 {
		return invalidf("potential profit cannot be negative")
	}
	if err := w.State.Validate(); err != nil {
		return err
	}
	key := strings.TrimSpace(w.IdempotencyKey)
	if key == "" || len(key) > 200 {
		return invalidf("idempotency key must be between 1 and 200 characters")
	}
	return nil
}
