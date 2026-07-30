package bettingpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/ledger"
	"github.com/jackc/pgx/v5"
)

// PlaceParlayRequest places one stake across several match results. Legs are
// given in the order the member picked them; the store resolves each market and
// selection and hands the pure domain the rest.
type PlaceParlayRequest struct {
	ParlayID string
	UserID   string
	// Legs are market/selection pairs. A market may appear only once: correlated
	// legs are how a book gets picked off, and the schema enforces it too.
	Legs               []ParlayLegRequest
	FundingAccountType betting.FundingAccountType
	StakeCents         int64
	Currency           ledger.Currency
	IdempotencyKey     string
	PlacedByUserID     string
}

// ParlayLegRequest is one leg of a parlay as submitted.
type ParlayLegRequest struct {
	MarketID    string
	SelectionID string
}

// ParlayRow is a parlay as the book reads it back.
type ParlayRow struct {
	ID                 string
	UserID             string
	MemberName         string
	FundingAccountType betting.FundingAccountType
	StakeCents         int64
	Currency           ledger.Currency
	AcceptedOdds       ledger.AmericanOdds
	PotentialProfit    int64
	State              betting.WagerState
	PlacedAt           time.Time
	Legs               []ParlayLegRow
}

// ParlayLegRow is one leg with its snapshot and, once its market has settled,
// its result.
type ParlayLegRow struct {
	MarketID      string
	SelectionID   string
	MarketTitle   string
	AcceptedTerms string
	AcceptedOdds  ledger.AmericanOdds
	Result        betting.SettlementResult
}

// Resolved reports whether this leg's market has already settled.
func (l ParlayLegRow) Resolved() bool { return l.Result != "" }

// PlaceParlay validates every leg, prices the combination, and stores a pending
// parlay. Nothing is debited yet: like a single wager, the stake moves only when
// the parlay is accepted.
func (s Store) PlaceParlay(ctx context.Context, req PlaceParlayRequest) (ParlayRow, error) {
	if !isUUID(req.ParlayID) || !isUUID(req.UserID) {
		return ParlayRow{}, fmt.Errorf("%w: a parlay needs a parlay ID and user ID", betting.ErrInvalid)
	}
	if len(req.Legs) == 0 {
		return ParlayRow{}, betting.ErrParlayTooFewLegs
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return ParlayRow{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	markets := make([]betting.Market, 0, len(req.Legs))
	selections := make([]betting.Selection, 0, len(req.Legs))
	restrictions := make([]betting.Restriction, 0)
	titles := make(map[string]string, len(req.Legs))
	for _, leg := range req.Legs {
		if !isUUID(leg.MarketID) || !isUUID(leg.SelectionID) {
			return ParlayRow{}, fmt.Errorf("%w: every leg needs a market and selection", betting.ErrInvalid)
		}
		market, err := loadMarket(ctx, tx, leg.MarketID)
		if err != nil {
			return ParlayRow{}, err
		}
		selection, err := loadSelection(ctx, tx, leg.MarketID, leg.SelectionID)
		if err != nil {
			return ParlayRow{}, err
		}
		legRestrictions, err := loadRestrictions(ctx, tx, leg.MarketID)
		if err != nil {
			return ParlayRow{}, err
		}
		markets = append(markets, market)
		selections = append(selections, selection)
		restrictions = append(restrictions, legRestrictions...)
		titles[leg.MarketID] = market.Title
	}

	stake, err := ledger.NewMoney(req.StakeCents, req.Currency)
	if err != nil {
		return ParlayRow{}, fmt.Errorf("build parlay stake: %w", err)
	}

	parlay, err := betting.PlaceParlay(betting.PlaceParlayCommand{
		ParlayID:           betting.ID(req.ParlayID),
		UserID:             betting.ID(req.UserID),
		Markets:            markets,
		Selections:         selections,
		Restrictions:       restrictions,
		JuiceBasisPoints:   s.parlayJuice(),
		MaxPayoutCents:     s.maxPayout(),
		PlacedBy:           betting.ID(req.PlacedByUserID),
		FundingAccountType: req.FundingAccountType,
		Stake:              stake,
		IdempotencyKey:     req.IdempotencyKey,
		Now:                time.Now(),
	})
	if err != nil {
		return ParlayRow{}, err
	}

	// ON CONFLICT DO NOTHING makes a repeated submission idempotent the way a
	// single wager is; the existing row is then read back and compared.
	var inserted bool
	if err := tx.QueryRow(ctx, `
		INSERT INTO parlays (id, user_id, funding_account_type, stake_cents, currency,
			accepted_american_odds, potential_profit_cents, state, idempotency_key, placed_by, placed_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, nullif($10, '')::uuid, $11)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING true`,
		parlay.ID, parlay.UserID, string(parlay.FundingAccountType), parlay.Stake.Cents,
		string(parlay.Stake.Currency), int32(parlay.AcceptedOdds), parlay.PotentialProfit.Cents,
		string(parlay.State), parlay.IdempotencyKey, string(parlay.PlacedBy), parlay.PlacedAt,
	).Scan(&inserted); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return ParlayRow{}, fmt.Errorf("insert parlay: %w", err)
		}
		existing, err := loadParlayByIdempotency(ctx, tx, string(parlay.UserID), parlay.IdempotencyKey)
		if err != nil {
			return ParlayRow{}, err
		}
		if err := verifyParlayMatches(existing, parlay); err != nil {
			return ParlayRow{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ParlayRow{}, fmt.Errorf("commit idempotent parlay: %w", err)
		}
		return existing, nil
	}

	for index, leg := range parlay.Legs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO parlay_legs (parlay_id, leg_index, market_id, selection_id,
				accepted_american_odds, accepted_terms)
			VALUES ($1::uuid, $2, $3::uuid, $4::uuid, $5, $6)`,
			parlay.ID, index, leg.MarketID, leg.SelectionID, int32(leg.AcceptedOdds), leg.AcceptedTerms); err != nil {
			return ParlayRow{}, fmt.Errorf("insert parlay leg %d: %w", index, err)
		}
	}

	row, err := loadParlay(ctx, tx, string(parlay.ID))
	if err != nil {
		return ParlayRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ParlayRow{}, fmt.Errorf("commit parlay: %w", err)
	}
	return row, nil
}

// parlayJuice is the book's per-extra-leg margin.
func (s Store) parlayJuice() int64 { return betting.DefaultParlayJuiceBasisPoints }

// AcceptParlay approves a pending parlay and moves its stake into escrow. It
// mirrors AcceptWager: the parlay row is locked first so a repeat is idempotent,
// and the funding account is locked so two acceptances cannot both see a balance
// that covers the stake.
func (s Store) AcceptParlay(ctx context.Context, parlayID, actorUserID string) (ParlayRow, error) {
	if !isUUID(parlayID) {
		return ParlayRow{}, fmt.Errorf("%w: parlay %s", betting.ErrNotFound, parlayID)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return ParlayRow{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	state, userID, fundingType, stakeCents, currency, err := loadParlayForUpdate(ctx, tx, parlayID)
	if err != nil {
		return ParlayRow{}, err
	}
	if state == string(betting.WagerAccepted) {
		row, err := loadParlay(ctx, tx, parlayID)
		if err != nil {
			return ParlayRow{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ParlayRow{}, fmt.Errorf("commit idempotent parlay acceptance: %w", err)
		}
		return row, nil
	}
	if state != string(betting.WagerPending) {
		return ParlayRow{}, fmt.Errorf("%w: parlay is %s", betting.ErrInvalidTransition, state)
	}

	// A leg whose market has already been decided can never grade this parlay
	// fairly, so the stake must not go into escrow behind a known result.
	var decided int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM parlay_legs l JOIN markets m ON m.id = l.market_id
		WHERE l.parlay_id = $1::uuid AND m.state IN ('settled', 'voided', 'cancelled', 'closed_no_action')`,
		parlayID).Scan(&decided); err != nil {
		return ParlayRow{}, fmt.Errorf("count decided legs: %w", err)
	}
	if decided > 0 {
		return ParlayRow{}, fmt.Errorf("%w: %d leg market(s) already decided", ErrMarketDecided, decided)
	}

	stake, err := ledger.NewMoney(stakeCents, currency)
	if err != nil {
		return ParlayRow{}, err
	}
	userAccountID, err := ensureUserAccount(ctx, tx, userID, fundingType, currency)
	if err != nil {
		return ParlayRow{}, err
	}
	escrowAccountID, err := ensureSystemAccount(ctx, tx, "wager_escrow", currency)
	if err != nil {
		return ParlayRow{}, err
	}
	if _, err := ensureSystemAccount(ctx, tx, "house_clearing", currency); err != nil {
		return ParlayRow{}, err
	}
	if err := lockAccount(ctx, tx, userAccountID); err != nil {
		return ParlayRow{}, err
	}
	balance, err := accountBalance(ctx, tx, userAccountID)
	if err != nil {
		return ParlayRow{}, err
	}
	creditLimit, err := creditLimitForUser(ctx, tx, userID)
	if err != nil {
		return ParlayRow{}, err
	}
	if balance+creditLimit < stake.Cents {
		return ParlayRow{}, ErrInsufficientFunds
	}

	acceptedAt := time.Now().UTC()
	negatedStake, err := stake.Negate()
	if err != nil {
		return ParlayRow{}, err
	}
	// A parlay is economically a wager, so it reuses the wager transaction
	// types rather than inventing parlay-specific ones that reconciliation and
	// the ledger CHECK constraints would not recognise. SourceType tells the
	// two apart on the record.
	transaction := ledger.Transaction{
		Type:           ledger.TransactionWagerAcceptance,
		Currency:       stake.Currency,
		IdempotencyKey: "parlay:" + parlayID + ":acceptance",
		Actor:          actorOrSystem(actorUserID),
		SourceType:     "parlay",
		SourceID:       parlayID,
		Postings: []ledger.Posting{
			{AccountID: userAccountID, Amount: negatedStake},
			{AccountID: escrowAccountID, Amount: stake},
		},
	}
	if err := transaction.Validate(); err != nil {
		return ParlayRow{}, fmt.Errorf("build parlay acceptance transaction: %w", err)
	}
	transactionID, err := insertLedgerTransaction(ctx, tx, transaction)
	if err != nil {
		return ParlayRow{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE parlays SET state = 'accepted', accepted_at = $2,
			accepted_by = nullif($3, '')::uuid, acceptance_ledger_transaction_id = $4::uuid
		WHERE id = $1::uuid`,
		parlayID, acceptedAt, actorUserIDOrEmpty(actorUserID), transactionID); err != nil {
		return ParlayRow{}, fmt.Errorf("accept parlay: %w", err)
	}

	row, err := loadParlay(ctx, tx, parlayID)
	if err != nil {
		return ParlayRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ParlayRow{}, fmt.Errorf("commit parlay acceptance: %w", err)
	}
	return row, nil
}

// actorOrSystem names the ledger actor. The ledger requires one even when the
// book approved automatically.
func actorOrSystem(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return AutoApproveActor
	}
	return actor
}

// actorUserIDOrEmpty drops a non-UUID actor such as the auto-approve marker so
// accepted_by stays NULL rather than failing the cast.
func actorUserIDOrEmpty(actor string) string {
	if isUUID(actor) {
		return actor
	}
	return ""
}

// RejectParlay refuses a pending parlay. Nothing has been debited, so there is
// nothing to give back.
func (s Store) RejectParlay(ctx context.Context, parlayID, actorUserID, reason string) error {
	if !isUUID(parlayID) {
		return fmt.Errorf("%w: parlay %s", betting.ErrNotFound, parlayID)
	}
	if !isUUID(actorUserID) {
		return betting.ErrUnauthorized
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return betting.ErrReasonRequired
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	state, _, _, _, _, err := loadParlayForUpdate(ctx, tx, parlayID)
	if err != nil {
		return err
	}
	if state != string(betting.WagerPending) {
		return fmt.Errorf("%w: parlay is %s", betting.ErrInvalidTransition, state)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE parlays SET state = 'rejected', rejected_at = now(),
			rejected_by = $2::uuid, rejection_reason = $3
		WHERE id = $1::uuid`, parlayID, actorUserID, reason); err != nil {
		return fmt.Errorf("reject parlay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit parlay rejection: %w", err)
	}
	return nil
}

// resolveParlayLegsTx marks every open leg on a market with the result its
// selection graded to, then settles any parlay whose last leg has just come in.
// It runs inside the market settlement transaction so a parlay can never be
// left holding a leg on a market the book considers finished.
func resolveParlayLegsTx(ctx context.Context, tx pgx.Tx, marketID string, outcome map[string]betting.SettlementResult, voided bool, juice int64) error {
	rows, err := tx.Query(ctx, `
		SELECT l.parlay_id::text, l.selection_id::text
		FROM parlay_legs l
		JOIN parlays p ON p.id = l.parlay_id
		WHERE l.market_id = $1::uuid AND l.result IS NULL AND p.state = 'accepted'
		FOR UPDATE OF l`, marketID)
	if err != nil {
		return fmt.Errorf("load open parlay legs: %w", err)
	}
	type openLeg struct{ parlayID, selectionID string }
	var legs []openLeg
	for rows.Next() {
		var leg openLeg
		if err := rows.Scan(&leg.parlayID, &leg.selectionID); err != nil {
			rows.Close()
			return fmt.Errorf("scan open parlay leg: %w", err)
		}
		legs = append(legs, leg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	affected := make(map[string]struct{}, len(legs))
	for _, leg := range legs {
		result := betting.ResultVoid
		if !voided {
			graded, ok := outcome[leg.selectionID]
			if !ok {
				return fmt.Errorf("%w: parlay leg selection %s has no outcome", betting.ErrIncompleteOutcome, leg.selectionID)
			}
			result = graded
		}
		if _, err := tx.Exec(ctx, `
			UPDATE parlay_legs SET result = $3, resolved_at = now()
			WHERE parlay_id = $1::uuid AND selection_id = $2::uuid AND result IS NULL`,
			leg.parlayID, leg.selectionID, string(result)); err != nil {
			return fmt.Errorf("resolve parlay leg: %w", err)
		}
		affected[leg.parlayID] = struct{}{}
	}

	for parlayID := range affected {
		if err := settleParlayIfCompleteTx(ctx, tx, parlayID, juice); err != nil {
			return err
		}
	}
	return nil
}

// settleParlayIfCompleteTx grades and pays a parlay once none of its legs is
// still open. A parlay with a losing leg is decided immediately rather than
// waiting on the rest: the bet is already dead and holding the stake in escrow
// past that point serves nobody.
func settleParlayIfCompleteTx(ctx context.Context, tx pgx.Tx, parlayID string, juice int64) error {
	parlay, err := loadParlayForGrading(ctx, tx, parlayID)
	if err != nil {
		return err
	}

	open := 0
	lost := false
	for _, leg := range parlay.Legs {
		switch leg.Result {
		case "":
			open++
		case betting.ResultLoss:
			lost = true
		}
	}
	if open > 0 && !lost {
		return nil
	}
	// A dead parlay grades on what is known; unresolved legs cannot revive it.
	if lost {
		for i := range parlay.Legs {
			if parlay.Legs[i].Result == "" {
				parlay.Legs[i].Result = betting.ResultVoid
			}
		}
	}

	outcome, err := betting.GradeParlay(parlay, juice)
	if err != nil {
		return err
	}

	currency := parlay.Stake.Currency
	userAccountID, err := ensureUserAccount(ctx, tx, string(parlay.UserID), parlay.FundingAccountType, currency)
	if err != nil {
		return err
	}
	escrowAccountID, err := ensureSystemAccount(ctx, tx, "wager_escrow", currency)
	if err != nil {
		return err
	}
	houseAccountID, err := ensureSystemAccount(ctx, tx, "house_clearing", currency)
	if err != nil {
		return err
	}

	negatedStake, err := outcome.Stake.Negate()
	if err != nil {
		return err
	}
	var postings []ledger.Posting
	transactionType := ledger.TransactionWagerRefund
	switch outcome.Result {
	case betting.ResultWin:
		negatedProfit, err := outcome.Profit.Negate()
		if err != nil {
			return err
		}
		// The stake comes back out of escrow and the profit comes from the
		// house, which is how a winning single wager pays too. Both land on the
		// member as one posting: the ledger refuses to post an account twice in
		// the same transaction, and their balance only moves by the total.
		transactionType = ledger.TransactionWagerWin
		postings = []ledger.Posting{
			{AccountID: escrowAccountID, Amount: negatedStake},
			{AccountID: houseAccountID, Amount: negatedProfit},
			{AccountID: userAccountID, Amount: outcome.Returns},
		}
	case betting.ResultLoss:
		transactionType = ledger.TransactionWagerLoss
		postings = []ledger.Posting{
			{AccountID: escrowAccountID, Amount: negatedStake},
			{AccountID: houseAccountID, Amount: outcome.Stake},
		}
	default: // push or void: the stake simply comes back
		postings = []ledger.Posting{
			{AccountID: escrowAccountID, Amount: negatedStake},
			{AccountID: userAccountID, Amount: outcome.Stake},
		}
	}

	transaction := ledger.Transaction{
		Type:           transactionType,
		Currency:       currency,
		IdempotencyKey: "parlay:" + parlayID + ":settlement",
		Actor:          "system:parlay-settlement",
		SourceType:     "parlay",
		SourceID:       parlayID,
		Postings:       postings,
	}
	if err := transaction.Validate(); err != nil {
		return fmt.Errorf("build parlay settlement transaction: %w", err)
	}
	transactionID, err := insertLedgerTransaction(ctx, tx, transaction)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO parlay_settlements (parlay_id, result, settled_american_odds, stake_cents,
			profit_cents, returned_cents, ledger_transaction_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid)
		ON CONFLICT (parlay_id) DO NOTHING`,
		parlayID, string(outcome.Result), int32(outcome.Odds), outcome.Stake.Cents,
		outcome.Profit.Cents, outcome.Returns.Cents, transactionID); err != nil {
		return fmt.Errorf("insert parlay settlement: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE parlays SET state = 'settled' WHERE id = $1::uuid`, parlayID); err != nil {
		return fmt.Errorf("mark parlay settled: %w", err)
	}
	return nil
}
