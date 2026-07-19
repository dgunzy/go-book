package bettingpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/dgunzy/go-book/internal/events"
	"github.com/dgunzy/go-book/internal/eventspg"
	"github.com/jackc/pgx/v5"
)

// CloseMarket transitions an open market to closed. It is idempotent: a
// market already closed is a no-op. No ledger writes happen here.
func (s Store) CloseMarket(ctx context.Context, marketID, actor string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	market, err := loadMarketForUpdate(ctx, tx, marketID)
	if err != nil {
		return err
	}
	if market.State == betting.MarketClosed {
		return tx.Commit(ctx)
	}
	if !market.State.CanTransitionTo(betting.MarketClosed) {
		return fmt.Errorf("%w: market %s state %s cannot close", ErrMarketNotSettleable, marketID, market.State)
	}

	tag, err := tx.Exec(ctx, `UPDATE markets SET state = 'closed', updated_at = now() WHERE id = $1::uuid AND state = 'open'`, marketID)
	if err != nil {
		return fmt.Errorf("close market %s: %w", marketID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("close market %s: market was not open", marketID)
	}

	eventID, err := betting.NewEventID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"market_id": marketID})
	if err != nil {
		return fmt.Errorf("marshal MarketClosed payload: %w", err)
	}
	envelope := events.Envelope{
		ID:               string(eventID),
		AggregateType:    "market",
		AggregateID:      marketID,
		AggregateVersion: 1,
		Type:             events.MarketClosed,
		Payload:          payload,
		OccurredAt:       time.Now().UTC(),
	}
	if err := eventspg.Publish(ctx, tx, envelope, maxOutboxAttempts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SettleMarketRequest grades a closed or settlement-pending market. Outcome
// maps every selection ID on the market to its settlement result.
type SettleMarketRequest struct {
	MarketID         string
	Outcome          map[string]betting.SettlementResult
	ActorUserID      string
	Reason           string
	VerifiedResultID string
}

// VoidMarketRequest refunds every accepted wager on a market and moves it to
// voided.
type VoidMarketRequest struct {
	MarketID    string
	ActorUserID string
	Reason      string
}

// SettleReport summarizes one market settlement or void, whether freshly
// computed or replayed from an existing idempotent row.
type SettleReport struct {
	MarketID           string
	SettlementID       string
	Version            int
	WinCount           int
	LossCount          int
	PushCount          int
	VoidCount          int
	TotalStakeCents    int64
	TotalProfitCents   int64
	TotalReturnedCents int64
}

// SettleMarket grades every accepted wager on a market and persists the
// result atomically: market_settlement_outcomes, one ledger transaction and
// wager_settlements row per settled wager, wager state updates, the market
// state update to settled, and MarketSettled/WagerSettled outbox events.
//
// Idempotency: the market row is locked FOR UPDATE for the whole operation,
// which serializes concurrent settlement attempts. The settlement version is
// 1 + the highest existing version for the market, and
// market_settlements(market_id, idempotency_key) is unique, so a duplicate
// settlement command (same version) is a DO-NOTHING insert; the existing
// outcome rows are then verified to match the requested outcome and the
// stored report is returned rather than re-grading.
func (s Store) SettleMarket(ctx context.Context, req SettleMarketRequest) (SettleReport, error) {
	return s.settle(ctx, req.MarketID, "graded", req.Outcome, req.ActorUserID, req.Reason, req.VerifiedResultID)
}

// VoidMarket refunds every accepted wager and moves an open, closed, or
// settlement-pending market to voided. It shares SettleMarket's persistence
// and idempotency path with settlement_type "voided" and a nil outcome.
func (s Store) VoidMarket(ctx context.Context, req VoidMarketRequest) (SettleReport, error) {
	return s.settle(ctx, req.MarketID, "voided", nil, req.ActorUserID, req.Reason, "")
}

func (s Store) settle(ctx context.Context, marketID, settlementType string, outcome map[string]betting.SettlementResult, actorUserID, reason, verifiedResultID string) (SettleReport, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return SettleReport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	market, err := loadMarketForUpdate(ctx, tx, marketID)
	if err != nil {
		return SettleReport{}, err
	}
	if err := validateSettleableState(market.State, settlementType); err != nil {
		return SettleReport{}, err
	}

	version, err := nextSettlementVersion(ctx, tx, marketID)
	if err != nil {
		return SettleReport{}, fmt.Errorf("compute next settlement version for market %s: %w", marketID, err)
	}
	idempotencyKey := fmt.Sprintf("market:%s:settlement:v%d", marketID, version)

	settlementID, err := betting.NewEventID()
	if err != nil {
		return SettleReport{}, err
	}
	insertedID, existed, err := insertMarketSettlement(ctx, tx, marketID, string(settlementID), version, settlementType, verifiedResultID, actorUserID, reason, idempotencyKey)
	if err != nil {
		return SettleReport{}, err
	}
	if existed {
		if settlementType == "graded" {
			if err := verifyOutcomeMatches(ctx, tx, insertedID, outcome); err != nil {
				return SettleReport{}, err
			}
		}
		report, err := loadExistingSettleReport(ctx, tx, insertedID, marketID, version)
		if err != nil {
			return SettleReport{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SettleReport{}, fmt.Errorf("commit idempotent market settlement: %w", err)
		}
		return report, nil
	}

	selections, _, err := loadSelections(ctx, tx, marketID)
	if err != nil {
		return SettleReport{}, err
	}
	wagers, err := loadWagersForMarketForUpdate(ctx, tx, marketID)
	if err != nil {
		return SettleReport{}, err
	}

	refs := make(map[betting.ID]betting.SettlementAccountRefs)
	wagerSettlementIDs := make(map[betting.ID]betting.ID)
	wagerEventIDs := make(map[betting.ID]betting.ID)
	for _, wager := range wagers {
		if wager.State != betting.WagerAccepted {
			continue
		}
		userAccountID, err := ensureUserAccount(ctx, tx, string(wager.UserID), wager.FundingAccountType, wager.Stake.Currency)
		if err != nil {
			return SettleReport{}, err
		}
		escrowAccountID, err := ensureSystemAccount(ctx, tx, "wager_escrow", wager.Stake.Currency)
		if err != nil {
			return SettleReport{}, err
		}
		houseAccountID, err := ensureSystemAccount(ctx, tx, "house_clearing", wager.Stake.Currency)
		if err != nil {
			return SettleReport{}, err
		}
		refs[wager.ID] = betting.SettlementAccountRefs{
			UserFundingAccountID:   userAccountID,
			EscrowAccountID:        escrowAccountID,
			HouseClearingAccountID: houseAccountID,
		}
		wagerSettlementID, err := betting.NewEventID()
		if err != nil {
			return SettleReport{}, err
		}
		wagerEventID, err := betting.NewEventID()
		if err != nil {
			return SettleReport{}, err
		}
		wagerSettlementIDs[wager.ID] = wagerSettlementID
		wagerEventIDs[wager.ID] = wagerEventID
	}

	marketEventID, err := betting.NewEventID()
	if err != nil {
		return SettleReport{}, err
	}
	now := time.Now()

	var result betting.SettleMarketResult
	if settlementType == "graded" {
		marketOutcome := make(betting.MarketOutcome, len(outcome))
		for id, r := range outcome {
			marketOutcome[betting.ID(id)] = r
		}
		result, err = betting.SettleMarket(betting.SettleMarketCommand{
			Market:             market,
			Selections:         selections,
			Outcome:            marketOutcome,
			Wagers:             wagers,
			Refs:               refs,
			WagerSettlementIDs: wagerSettlementIDs,
			WagerEventIDs:      wagerEventIDs,
			SettlementID:       settlementID,
			Version:            version,
			Actor:              betting.ID(actorUserID),
			OccurredAt:         now,
			MarketEventID:      marketEventID,
		})
	} else {
		result, err = betting.VoidMarket(betting.VoidMarketCommand{
			Market:             market,
			Wagers:             wagers,
			Refs:               refs,
			WagerSettlementIDs: wagerSettlementIDs,
			WagerEventIDs:      wagerEventIDs,
			SettlementID:       settlementID,
			Version:            version,
			Actor:              betting.ID(actorUserID),
			Reason:             reason,
			OccurredAt:         now,
			MarketEventID:      marketEventID,
		})
	}
	if err != nil {
		return SettleReport{}, err
	}

	if settlementType == "graded" {
		for id, r := range outcome {
			if _, err := tx.Exec(ctx, `
				INSERT INTO market_settlement_outcomes (market_settlement_id, market_id, selection_id, outcome)
				VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`, insertedID, marketID, id, string(r)); err != nil {
				return SettleReport{}, fmt.Errorf("insert market settlement outcome: %w", err)
			}
		}
	}

	report := SettleReport{MarketID: marketID, SettlementID: insertedID, Version: version}
	for i, settlement := range result.Settlements {
		transactionID, err := insertLedgerTransaction(ctx, tx, settlement.Transaction)
		if err != nil {
			return SettleReport{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO wager_settlements (id, wager_id, market_settlement_id, result, stake_cents, profit_cents, returned_cents, ledger_transaction_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::uuid)`,
			settlement.ID, settlement.WagerID, insertedID, string(settlement.Result),
			settlement.Stake.Cents, settlement.Profit.Cents, settlement.Returned.Cents, transactionID); err != nil {
			return SettleReport{}, fmt.Errorf("insert wager settlement: %w", err)
		}

		nextState := "settled"
		if settlement.Result == betting.ResultVoid {
			nextState = "voided"
		}
		if _, err := tx.Exec(ctx, `UPDATE wagers SET state = $2 WHERE id = $1::uuid`, settlement.WagerID, nextState); err != nil {
			return SettleReport{}, fmt.Errorf("update wager %s state: %w", settlement.WagerID, err)
		}

		if i < len(result.WagerEvents) {
			if err := eventspg.Publish(ctx, tx, result.WagerEvents[i], maxOutboxAttempts); err != nil {
				return SettleReport{}, err
			}
		}

		switch settlement.Result {
		case betting.ResultWin:
			report.WinCount++
		case betting.ResultLoss:
			report.LossCount++
		case betting.ResultPush:
			report.PushCount++
		case betting.ResultVoid:
			report.VoidCount++
		}
		report.TotalStakeCents += settlement.Stake.Cents
		report.TotalProfitCents += settlement.Profit.Cents
		report.TotalReturnedCents += settlement.Returned.Cents
	}

	if _, err := tx.Exec(ctx, `UPDATE markets SET state = $2, updated_at = now() WHERE id = $1::uuid`, marketID, string(result.Market.State)); err != nil {
		return SettleReport{}, fmt.Errorf("update market %s state: %w", marketID, err)
	}
	if err := eventspg.Publish(ctx, tx, result.MarketEvent, maxOutboxAttempts); err != nil {
		return SettleReport{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return SettleReport{}, fmt.Errorf("commit market settlement: %w", err)
	}
	return report, nil
}

func validateSettleableState(state betting.MarketState, settlementType string) error {
	if settlementType == "graded" {
		if state != betting.MarketClosed && state != betting.MarketSettlementPending {
			return fmt.Errorf("%w: state %s", ErrMarketNotSettleable, state)
		}
		return nil
	}
	switch state {
	case betting.MarketOpen, betting.MarketClosed, betting.MarketSettlementPending:
		return nil
	default:
		return fmt.Errorf("%w: state %s", ErrMarketNotSettleable, state)
	}
}

func nextSettlementVersion(ctx context.Context, tx pgx.Tx, marketID string) (int, error) {
	var version int
	err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) + 1 FROM market_settlements WHERE market_id = $1::uuid`, marketID).Scan(&version)
	return version, err
}

// insertMarketSettlement inserts a market_settlements row, mirroring the
// insert-with-ON-CONFLICT-DO-NOTHING-then-verify idempotency pattern used
// throughout legacybook. existed reports whether the idempotency key was
// already present, in which case id is the pre-existing row's ID.
func insertMarketSettlement(ctx context.Context, tx pgx.Tx, marketID, settlementID string, version int, settlementType, verifiedResultID, actorUserID, reason, idempotencyKey string) (id string, existed bool, err error) {
	// settled_by is a real user reference; a system actor like
	// "system:settlement-worker" is not a UUID and must be left NULL rather
	// than passed into the ::uuid cast, which the verified_result_id branch
	// of the CHECK constraint already allows for.
	settledByUUID := ""
	if isUUID(actorUserID) {
		settledByUUID = actorUserID
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO market_settlements (id, market_id, version, settlement_type, verified_result_id, settled_by, reason, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, nullif($5, '')::uuid, nullif($6, '')::uuid, nullif($7, ''), $8)
		ON CONFLICT (market_id, idempotency_key) DO NOTHING
		RETURNING id::text`,
		settlementID, marketID, version, settlementType, verifiedResultID, settledByUUID, reason, idempotencyKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM market_settlements WHERE market_id = $1::uuid AND idempotency_key = $2`,
			marketID, idempotencyKey).Scan(&id); err != nil {
			return "", false, fmt.Errorf("load existing market settlement: %w", err)
		}
		return id, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("insert market settlement: %w", err)
	}
	return id, false, nil
}

func verifyOutcomeMatches(ctx context.Context, tx pgx.Tx, settlementID string, outcome map[string]betting.SettlementResult) error {
	rows, err := tx.Query(ctx, `SELECT selection_id::text, outcome FROM market_settlement_outcomes WHERE market_settlement_id = $1::uuid`, settlementID)
	if err != nil {
		return fmt.Errorf("load existing market settlement outcomes: %w", err)
	}
	defer rows.Close()
	stored := make(map[string]string)
	for rows.Next() {
		var id, result string
		if err := rows.Scan(&id, &result); err != nil {
			return fmt.Errorf("scan market settlement outcome: %w", err)
		}
		stored[id] = result
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(stored) != len(outcome) {
		return fmt.Errorf("%w: existing settlement covers %d selections, requested %d", ErrIdempotencyConflict, len(stored), len(outcome))
	}
	for id, result := range outcome {
		if stored[id] != string(result) {
			return fmt.Errorf("%w: selection %s outcome does not match existing settlement", ErrIdempotencyConflict, id)
		}
	}
	return nil
}

func loadExistingSettleReport(ctx context.Context, tx pgx.Tx, settlementID, marketID string, version int) (SettleReport, error) {
	report := SettleReport{MarketID: marketID, SettlementID: settlementID, Version: version}
	rows, err := tx.Query(ctx, `
		SELECT result, stake_cents, profit_cents, returned_cents FROM wager_settlements WHERE market_settlement_id = $1::uuid`, settlementID)
	if err != nil {
		return SettleReport{}, fmt.Errorf("load existing wager settlements: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		var stake, profit, returned int64
		if err := rows.Scan(&result, &stake, &profit, &returned); err != nil {
			return SettleReport{}, fmt.Errorf("scan existing wager settlement: %w", err)
		}
		switch betting.SettlementResult(result) {
		case betting.ResultWin:
			report.WinCount++
		case betting.ResultLoss:
			report.LossCount++
		case betting.ResultPush:
			report.PushCount++
		case betting.ResultVoid:
			report.VoidCount++
		}
		report.TotalStakeCents += stake
		report.TotalProfitCents += profit
		report.TotalReturnedCents += returned
	}
	return report, rows.Err()
}
