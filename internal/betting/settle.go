package betting

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/ledger"
)

// MarketOutcome maps every selection ID in a market to its settlement
// result. Full coverage of the market's selections is required exactly once
// each; props may assign more than one winning selection, so no
// single-winner rule is enforced here.
type MarketOutcome map[ID]SettlementResult

func (o MarketOutcome) validate(selections []Selection) error {
	if len(o) != len(selections) {
		return ErrIncompleteOutcome
	}
	for _, selection := range selections {
		result, ok := o[selection.ID]
		if !ok {
			return fmt.Errorf("%w: selection %s missing", ErrIncompleteOutcome, selection.ID)
		}
		if err := result.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// SettlementAccountRefs supplies the ledger accounts needed to settle one
// wager. HouseClearingAccountID is only required for win and loss results.
type SettlementAccountRefs struct {
	UserFundingAccountID   string
	EscrowAccountID        string
	HouseClearingAccountID string
}

func (r SettlementAccountRefs) validate(result SettlementResult) error {
	if strings.TrimSpace(r.UserFundingAccountID) == "" || strings.TrimSpace(r.EscrowAccountID) == "" {
		return invalidf("settlement requires user funding and escrow account references")
	}
	if (result == ResultWin || result == ResultLoss) && strings.TrimSpace(r.HouseClearingAccountID) == "" {
		return invalidf("win and loss settlement requires a house clearing account reference")
	}
	return nil
}

// WagerSettlement is the settlement outcome recorded for one accepted
// wager, mirroring the wager_settlements table. Returned/Profit satisfy the
// same equations as the returned_cents CHECK constraint: win returns
// stake+profit, loss returns nothing, push/void return the stake with zero
// profit.
type WagerSettlement struct {
	ID                 ID
	WagerID            ID
	MarketSettlementID ID
	Result             SettlementResult
	Stake              ledger.Money
	Profit             ledger.Money
	Returned           ledger.Money
	Transaction        ledger.Transaction
}

// SettleMarketCommand is the pure input to grade a market. The caller loads
// the market, its full selection list, every wager to consider, and the
// ledger account references; this function performs no I/O and is
// deterministic: identical input produces identical idempotency keys and
// amounts, so duplicate delivery of the same command cannot double-pay.
type SettleMarketCommand struct {
	Market     Market
	Selections []Selection
	Outcome    MarketOutcome
	Wagers     []Wager
	// Refs, WagerSettlementIDs, and WagerEventIDs are keyed by wager ID and
	// must contain an entry for every accepted wager on the market.
	Refs               map[ID]SettlementAccountRefs
	WagerSettlementIDs map[ID]ID
	WagerEventIDs      map[ID]ID
	SettlementID       ID // market_settlements row ID, used as ledger SourceID
	Version            int
	Actor              ID
	OccurredAt         time.Time
	MarketEventID      ID
}

// SettleMarketResult bundles everything the caller must persist atomically:
// the updated market and wager states, one WagerSettlement (with its
// balanced ledger transaction) per settled wager, and the domain events to
// append to the outbox.
type SettleMarketResult struct {
	Market      Market
	Wagers      []Wager
	Settlements []WagerSettlement
	MarketEvent events.Envelope
	WagerEvents []events.Envelope
}

// SettleMarket grades every accepted wager on a closed or settlement-pending
// market and moves it to settled. It is an explicit correction-style
// command: like VoidMarket, its allowed source states are checked directly
// rather than through MarketState.CanTransitionTo, because closed markets
// settle in one step.
func SettleMarket(command SettleMarketCommand) (SettleMarketResult, error) {
	if err := command.Market.Validate(); err != nil {
		return SettleMarketResult{}, err
	}
	if command.Market.State != MarketClosed && command.Market.State != MarketSettlementPending {
		return SettleMarketResult{}, transitionErr("settle market", string(command.Market.State))
	}
	if command.Version <= 0 {
		return SettleMarketResult{}, invalidf("settlement version must be positive")
	}
	if !validID(command.SettlementID) || !validID(command.Actor) || !validID(command.MarketEventID) {
		return SettleMarketResult{}, invalidf("market settlement requires settlement, actor, and event IDs")
	}
	if command.OccurredAt.IsZero() {
		return SettleMarketResult{}, invalidf("market settlement requires an occurrence time")
	}
	for _, selection := range command.Selections {
		if selection.MarketID != command.Market.ID {
			return SettleMarketResult{}, ErrSelectionMismatch
		}
	}
	if err := command.Outcome.validate(command.Selections); err != nil {
		return SettleMarketResult{}, err
	}

	at := command.OccurredAt.UTC()
	result := SettleMarketResult{Market: command.Market}
	result.Market.State = MarketSettled

	acceptedWagers, err := splitAcceptedWagers(command.Market.ID, command.Wagers, &result)
	if err != nil {
		return SettleMarketResult{}, err
	}

	var counts [4]int
	var totalStake, totalProfit, totalReturned int64
	for _, wager := range acceptedWagers {
		outcome, ok := command.Outcome[wager.SelectionID]
		if !ok {
			return SettleMarketResult{}, fmt.Errorf("%w: wager %s selection has no outcome", ErrIncompleteOutcome, wager.ID)
		}
		refs, ok := command.Refs[wager.ID]
		if !ok {
			return SettleMarketResult{}, fmt.Errorf("%w: missing account references for wager %s", ErrInvalid, wager.ID)
		}
		settlementID := command.WagerSettlementIDs[wager.ID]
		eventID := command.WagerEventIDs[wager.ID]
		if !validID(settlementID) || !validID(eventID) {
			return SettleMarketResult{}, fmt.Errorf("%w: missing settlement or event ID for wager %s", ErrInvalid, wager.ID)
		}

		settlement, updatedWager, envelope, err := settleWager(wager, outcome, refs, command.SettlementID, settlementID, command.Version, command.Actor, at, eventID)
		if err != nil {
			return SettleMarketResult{}, err
		}

		result.Settlements = append(result.Settlements, settlement)
		result.Wagers = append(result.Wagers, updatedWager)
		result.WagerEvents = append(result.WagerEvents, envelope)

		counts[resultIndex(outcome)]++
		totalStake += settlement.Stake.Cents
		totalProfit += settlement.Profit.Cents
		totalReturned += settlement.Returned.Cents
	}

	payload := marketSettledPayload{
		MarketID: string(command.Market.ID), Version: command.Version,
		WinCount: counts[0], LossCount: counts[1], PushCount: counts[2], VoidCount: counts[3],
		TotalStakeCents: totalStake, TotalProfitCents: totalProfit, TotalReturnedCents: totalReturned,
		Currency: string(command.Market.Currency),
	}
	envelope, err := buildEnvelope(command.MarketEventID, command.Market.ID, "market", int64(command.Version), events.MarketSettled, at, payload)
	if err != nil {
		return SettleMarketResult{}, err
	}
	result.MarketEvent = envelope
	return result, nil
}

// VoidMarketCommand is the pure input to void a market and refund every
// accepted wager.
type VoidMarketCommand struct {
	Market             Market
	Wagers             []Wager
	Refs               map[ID]SettlementAccountRefs
	WagerSettlementIDs map[ID]ID
	WagerEventIDs      map[ID]ID
	SettlementID       ID
	Version            int
	Actor              ID
	Reason             string
	OccurredAt         time.Time
	MarketEventID      ID
}

// VoidMarket refunds every accepted wager and moves the market to voided.
// Unlike SettleMarket, an open market may also be voided (for example, a
// market opened in error) so its allowed source states are wider than
// SettleMarket's; both bypass MarketState.CanTransitionTo as explicit
// correction commands.
func VoidMarket(command VoidMarketCommand) (SettleMarketResult, error) {
	if err := command.Market.Validate(); err != nil {
		return SettleMarketResult{}, err
	}
	switch command.Market.State {
	case MarketOpen, MarketClosed, MarketSettlementPending:
	default:
		return SettleMarketResult{}, transitionErr("void market", string(command.Market.State))
	}
	if strings.TrimSpace(command.Reason) == "" {
		return SettleMarketResult{}, ErrReasonRequired
	}
	if command.Version <= 0 {
		return SettleMarketResult{}, invalidf("settlement version must be positive")
	}
	if !validID(command.SettlementID) || !validID(command.Actor) || !validID(command.MarketEventID) {
		return SettleMarketResult{}, invalidf("market void requires settlement, actor, and event IDs")
	}
	if command.OccurredAt.IsZero() {
		return SettleMarketResult{}, invalidf("market void requires an occurrence time")
	}

	at := command.OccurredAt.UTC()
	result := SettleMarketResult{Market: command.Market}
	result.Market.State = MarketVoided

	acceptedWagers, err := splitAcceptedWagers(command.Market.ID, command.Wagers, &result)
	if err != nil {
		return SettleMarketResult{}, err
	}

	var totalStake int64
	for _, wager := range acceptedWagers {
		refs, ok := command.Refs[wager.ID]
		if !ok {
			return SettleMarketResult{}, fmt.Errorf("%w: missing account references for wager %s", ErrInvalid, wager.ID)
		}
		settlementID := command.WagerSettlementIDs[wager.ID]
		eventID := command.WagerEventIDs[wager.ID]
		if !validID(settlementID) || !validID(eventID) {
			return SettleMarketResult{}, fmt.Errorf("%w: missing settlement or event ID for wager %s", ErrInvalid, wager.ID)
		}

		settlement, updatedWager, envelope, err := settleWager(wager, ResultVoid, refs, command.SettlementID, settlementID, command.Version, command.Actor, at, eventID)
		if err != nil {
			return SettleMarketResult{}, err
		}
		result.Settlements = append(result.Settlements, settlement)
		result.Wagers = append(result.Wagers, updatedWager)
		result.WagerEvents = append(result.WagerEvents, envelope)
		totalStake += settlement.Stake.Cents
	}

	payload := marketSettledPayload{
		MarketID: string(command.Market.ID), Version: command.Version,
		VoidCount:          len(acceptedWagers),
		TotalStakeCents:    totalStake,
		TotalReturnedCents: totalStake,
		Currency:           string(command.Market.Currency),
	}
	envelope, err := buildEnvelope(command.MarketEventID, command.Market.ID, "market", int64(command.Version), events.MarketSettled, at, payload)
	if err != nil {
		return SettleMarketResult{}, err
	}
	result.MarketEvent = envelope
	return result, nil
}

// splitAcceptedWagers validates that every supplied wager belongs to the
// market, appends non-accepted wagers to result.Wagers unchanged, and
// returns the accepted wagers in a deterministic (ID-sorted) order so that
// repeated settlement runs produce byte-identical event slices.
func splitAcceptedWagers(marketID ID, wagers []Wager, result *SettleMarketResult) ([]Wager, error) {
	accepted := make([]Wager, 0, len(wagers))
	for _, wager := range wagers {
		if wager.MarketID != marketID {
			return nil, ErrWagerMarketMismatch
		}
		if wager.State != WagerAccepted {
			result.Wagers = append(result.Wagers, wager)
			continue
		}
		accepted = append(accepted, wager)
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID < accepted[j].ID })
	return accepted, nil
}

func resultIndex(r SettlementResult) int {
	switch r {
	case ResultWin:
		return 0
	case ResultLoss:
		return 1
	case ResultPush:
		return 2
	default:
		return 3
	}
}

// settleWager computes the ledger transaction, wager settlement record,
// updated wager state, and WagerSettled envelope for one accepted wager
// graded with the given result. The idempotency key
// "wager:{wagerID}:settlement:v{version}" is a pure function of its inputs,
// so the same (wager, version) pair always produces the same key and
// amounts and duplicate delivery cannot double-pay.
func settleWager(wager Wager, result SettlementResult, refs SettlementAccountRefs, marketSettlementID, wagerSettlementID ID, version int, actor ID, at time.Time, eventID ID) (WagerSettlement, Wager, events.Envelope, error) {
	if err := refs.validate(result); err != nil {
		return WagerSettlement{}, Wager{}, events.Envelope{}, err
	}

	nextState := WagerSettled
	if result == ResultVoid {
		nextState = WagerVoided
	}
	if !wager.State.CanTransitionTo(nextState) {
		return WagerSettlement{}, Wager{}, events.Envelope{}, transitionErr("settle wager", string(wager.State))
	}

	currency := wager.Stake.Currency
	negatedStake, err := wager.Stake.Negate()
	if err != nil {
		return WagerSettlement{}, Wager{}, events.Envelope{}, err
	}

	var (
		profit, returned ledger.Money
		postings         []ledger.Posting
		txnType          ledger.TransactionType
	)
	switch result {
	case ResultWin:
		txnType = ledger.TransactionWagerWin
		profit, err = wager.AcceptedOdds.Profit(wager.Stake)
		if err != nil {
			return WagerSettlement{}, Wager{}, events.Envelope{}, err
		}
		returned, err = wager.Stake.Add(profit)
		if err != nil {
			return WagerSettlement{}, Wager{}, events.Envelope{}, err
		}
		if profit.Cents == 0 {
			// A legacy or minimal stake can round to zero profit at heavy
			// negative odds. A zero posting is illegal, so the win degrades to
			// a stake-only return; returned = stake + 0 still satisfies the
			// wager_settlements win equation.
			postings = []ledger.Posting{
				{AccountID: refs.EscrowAccountID, Amount: negatedStake},
				{AccountID: refs.UserFundingAccountID, Amount: returned},
			}
			break
		}
		negatedProfit, err := profit.Negate()
		if err != nil {
			return WagerSettlement{}, Wager{}, events.Envelope{}, err
		}
		postings = []ledger.Posting{
			{AccountID: refs.EscrowAccountID, Amount: negatedStake},
			{AccountID: refs.HouseClearingAccountID, Amount: negatedProfit},
			{AccountID: refs.UserFundingAccountID, Amount: returned},
		}
	case ResultLoss:
		txnType = ledger.TransactionWagerLoss
		profit = ledger.Money{Cents: 0, Currency: currency}
		returned = ledger.Money{Cents: 0, Currency: currency}
		postings = []ledger.Posting{
			{AccountID: refs.EscrowAccountID, Amount: negatedStake},
			{AccountID: refs.HouseClearingAccountID, Amount: wager.Stake},
		}
	case ResultPush, ResultVoid:
		txnType = ledger.TransactionWagerRefund
		profit = ledger.Money{Cents: 0, Currency: currency}
		returned = wager.Stake
		postings = []ledger.Posting{
			{AccountID: refs.EscrowAccountID, Amount: negatedStake},
			{AccountID: refs.UserFundingAccountID, Amount: wager.Stake},
		}
	default:
		return WagerSettlement{}, Wager{}, events.Envelope{}, invalidf("settlement result %q is not supported", result)
	}

	txn := ledger.Transaction{
		Type:           txnType,
		Currency:       currency,
		IdempotencyKey: fmt.Sprintf("wager:%s:settlement:v%d", wager.ID, version),
		Actor:          string(actor),
		SourceType:     "market_settlement",
		SourceID:       string(marketSettlementID),
		Postings:       postings,
	}
	if err := txn.Validate(); err != nil {
		return WagerSettlement{}, Wager{}, events.Envelope{}, fmt.Errorf("build settlement transaction for wager %s: %w", wager.ID, err)
	}

	settledWager := wager
	settledWager.State = nextState

	settlement := WagerSettlement{
		ID:                 wagerSettlementID,
		WagerID:            wager.ID,
		MarketSettlementID: marketSettlementID,
		Result:             result,
		Stake:              wager.Stake,
		Profit:             profit,
		Returned:           returned,
		Transaction:        txn,
	}

	payload := wagerSettledPayload{
		WagerID:       string(wager.ID),
		UserID:        string(wager.UserID),
		Result:        string(result),
		StakeCents:    wager.Stake.Cents,
		ProfitCents:   profit.Cents,
		ReturnedCents: returned.Cents,
		Currency:      string(currency),
	}
	// The settlement version is the aggregate version: a corrected
	// re-settlement (version 2) must not collide with the version-1 outbox
	// row's uniqueness and be silently dropped on publish.
	envelope, err := buildEnvelope(eventID, wager.ID, "wager", int64(version), events.WagerSettled, at, payload)
	if err != nil {
		return WagerSettlement{}, Wager{}, events.Envelope{}, err
	}

	return settlement, settledWager, envelope, nil
}
