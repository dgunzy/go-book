package legacybook

import (
	"context"
	"errors"
	"fmt"
)

type UserInput struct {
	LegacyUserID         int64
	DisplayName          string
	Email                string
	Role                 string
	Currency             string
	BalanceCents         int64
	FreePlayBalanceCents int64
	TransactionNetCents  int64
	DifferenceCents      int64
	ActorUserID          string
}

type OpeningBalanceInput struct {
	LegacyUserID        int64
	UserID              string
	AccountType         string
	Currency            string
	AmountCents         int64
	TransactionNetCents int64
	DifferenceCents     int64
	IdempotencyKey      string
	Reason              string
	ActorUserID         string
}

// SeasonSettlementInput records that a legacy closing balance was settled in
// cash outside the application after the season ended. The settlement posts the
// exact inverse of the opening balance so the account provably returns to zero.
type SeasonSettlementInput struct {
	LegacyUserID   int64
	UserID         string
	AccountType    string
	Currency       string
	SettledCents   int64
	IdempotencyKey string
	Reason         string
	ActorUserID    string
}

// Repository methods must be idempotent. Existing rows keyed by the same
// migration batch/source user or idempotency key must be verified, not changed.
type Repository interface {
	EnsureApprovedUser(context.Context, string, UserInput) (string, error)
	EnsureOpeningBalance(context.Context, string, OpeningBalanceInput) error
	EnsureSeasonSettlement(context.Context, string, SeasonSettlementInput) error
	EnsureLegacyTransaction(context.Context, string, string, TransactionRecord) error
	EnsureLegacyWager(context.Context, string, string, WagerRecord) error
	CompletePromotion(context.Context, string, CompletionInput) error
}

type Store interface {
	WithinTransaction(context.Context, func(Repository) error) error
}

type PromoteOptions struct {
	BatchID       string
	Currency      string
	ActorUserID   string
	SourceVersion string
}

type PromotionResult struct {
	Users            int `json:"users"`
	Memberships      int `json:"memberships"`
	CashBalances     int `json:"cash_balances"`
	FreePlayBalances int `json:"free_play_balances"`
	SettledBalances  int `json:"settled_balances"`
	Transactions     int `json:"transactions"`
	Wagers           int `json:"wagers"`
}

type CompletionInput struct {
	ActorUserID              string
	SourceChecksum           string
	Users                    int
	SettledBalances          int
	Transactions             int
	Wagers                   int
	ClosingCashTotalCents    int64
	TransactionNetTotalCents int64
	DifferenceTotalCents     int64
}

func Promote(ctx context.Context, store Store, report Report, options PromoteOptions) (PromotionResult, error) {
	if store == nil {
		return PromotionResult{}, errors.New("legacy book promotion store is required")
	}
	if report.SourceSystem != SourceSystem || report.Version != 1 {
		return PromotionResult{}, errors.New("unsupported reconciliation report")
	}
	if !report.Promotable() {
		return PromotionResult{}, errors.New("reconciliation report contains blocking issues")
	}
	if err := validatePromotionReport(report); err != nil {
		return PromotionResult{}, fmt.Errorf("invalid reconciliation report: %w", err)
	}
	if options.BatchID == "" {
		return PromotionResult{}, errors.New("migration batch ID is required")
	}
	if options.ActorUserID == "" {
		return PromotionResult{}, errors.New("migration actor user ID is required")
	}
	if len(options.SourceVersion) != 64 {
		return PromotionResult{}, errors.New("approved 64-character source version is required")
	}
	if options.Currency == "" {
		options.Currency = "CAD"
	}
	if len(options.Currency) != 3 || options.Currency != "CAD" {
		return PromotionResult{}, fmt.Errorf("unsupported migration currency %q", options.Currency)
	}

	result := PromotionResult{}
	err := store.WithinTransaction(ctx, func(repo Repository) error {
		userIDs := make(map[int64]string, len(report.Users))
		for _, user := range report.Users {
			if !user.Promotable {
				return fmt.Errorf("legacy user %d is not promotable", user.LegacyUserID)
			}
			userID, err := repo.EnsureApprovedUser(ctx, options.BatchID, UserInput{LegacyUserID: user.LegacyUserID, DisplayName: user.DisplayName, Email: user.Email, Role: user.TargetRole, Currency: options.Currency, BalanceCents: user.ClosingCashCents, FreePlayBalanceCents: user.ClosingFreePlayCents, TransactionNetCents: user.TransactionNetCents, DifferenceCents: user.DifferenceCents, ActorUserID: options.ActorUserID})
			if err != nil {
				return fmt.Errorf("promote legacy user %d: %w", user.LegacyUserID, err)
			}
			result.Users++
			result.Memberships++
			userIDs[user.LegacyUserID] = userID
			balances := []struct {
				accountType string
				amount      int64
				net         int64
				diff        int64
			}{
				{"user_cash", user.ClosingCashCents, user.TransactionNetCents, user.DifferenceCents},
				{"user_free_play", user.ClosingFreePlayCents, 0, user.ClosingFreePlayCents},
			}
			for _, balance := range balances {
				if balance.amount == 0 {
					continue
				}
				key := fmt.Sprintf("legacy-book:%s:user:%d:%s", options.BatchID, user.LegacyUserID, balance.accountType)
				reason := fmt.Sprintf("Legacy Cabot Book opening balance; transaction net=%d cents; explicit migration difference=%d cents", balance.net, balance.diff)
				err = repo.EnsureOpeningBalance(ctx, options.BatchID, OpeningBalanceInput{LegacyUserID: user.LegacyUserID, UserID: userID, AccountType: balance.accountType, Currency: options.Currency, AmountCents: balance.amount, TransactionNetCents: balance.net, DifferenceCents: balance.diff, IdempotencyKey: key, Reason: reason, ActorUserID: options.ActorUserID})
				if err != nil {
					return fmt.Errorf("promote %s balance for legacy user %d: %w", balance.accountType, user.LegacyUserID, err)
				}
				if balance.accountType == "user_cash" {
					result.CashBalances++
				} else {
					result.FreePlayBalances++
				}

				settlementKey := fmt.Sprintf("legacy-book:%s:settlement:%d:%s", options.BatchID, user.LegacyUserID, balance.accountType)
				settlementReason := fmt.Sprintf(
					"2025 Cabot Book season closed: %s balance of %d cents was settled in full outside the application after the season ended; account reset to zero so the 2026 season starts at 0",
					balance.accountType, balance.amount,
				)
				err = repo.EnsureSeasonSettlement(ctx, options.BatchID, SeasonSettlementInput{
					LegacyUserID: user.LegacyUserID, UserID: userID, AccountType: balance.accountType,
					Currency: options.Currency, SettledCents: balance.amount,
					IdempotencyKey: settlementKey, Reason: settlementReason, ActorUserID: options.ActorUserID,
				})
				if err != nil {
					return fmt.Errorf("settle %s balance for legacy user %d: %w", balance.accountType, user.LegacyUserID, err)
				}
				result.SettledBalances++
			}
		}
		for _, transaction := range report.Transactions {
			userID := userIDs[transaction.LegacyUserID]
			if userID == "" {
				return fmt.Errorf("transaction %d has no promoted user", transaction.SourceTransactionID)
			}
			if err := repo.EnsureLegacyTransaction(ctx, options.BatchID, userID, transaction); err != nil {
				return fmt.Errorf("promote legacy transaction %d: %w", transaction.SourceTransactionID, err)
			}
			result.Transactions++
		}
		for _, wager := range report.Wagers {
			userID := userIDs[wager.LegacyUserID]
			if userID == "" {
				return fmt.Errorf("wager %d has no promoted user", wager.SourceWagerID)
			}
			if err := repo.EnsureLegacyWager(ctx, options.BatchID, userID, wager); err != nil {
				return fmt.Errorf("promote legacy wager %d: %w", wager.SourceWagerID, err)
			}
			result.Wagers++
		}
		if err := repo.CompletePromotion(ctx, options.BatchID, CompletionInput{
			ActorUserID: options.ActorUserID, SourceChecksum: options.SourceVersion,
			Users: report.UserCount, SettledBalances: result.SettledBalances,
			Transactions: report.TransactionCount, Wagers: report.WagerCount,
			ClosingCashTotalCents:    report.ClosingCashTotalCents,
			TransactionNetTotalCents: report.TransactionNetTotalCents,
			DifferenceTotalCents:     report.DifferenceTotalCents,
		}); err != nil {
			return fmt.Errorf("complete legacy promotion: %w", err)
		}
		return nil
	})
	if err != nil {
		return PromotionResult{}, err
	}
	return result, nil
}

func validatePromotionReport(report Report) error {
	if report.UserCount != len(report.Users) || report.TransactionCount != len(report.Transactions) || report.WagerCount != len(report.Wagers) {
		return errors.New("source counts do not match normalized records")
	}
	users := make(map[int64]UserReconciliation, len(report.Users))
	var cashTotal, freePlayTotal int64
	for _, user := range report.Users {
		if _, duplicate := users[user.LegacyUserID]; duplicate {
			return fmt.Errorf("duplicate user %d", user.LegacyUserID)
		}
		if user.DifferenceCents != user.ClosingCashCents-user.TransactionNetCents {
			return fmt.Errorf("user %d reconciliation difference is inconsistent", user.LegacyUserID)
		}
		users[user.LegacyUserID] = user
		cashTotal += user.ClosingCashCents
		freePlayTotal += user.ClosingFreePlayCents
	}
	totals := make(map[int64]int64, len(users))
	counts := make(map[int64]int, len(users))
	seenTransactions := make(map[int64]struct{}, len(report.Transactions))
	for _, transaction := range report.Transactions {
		if transaction.Currency != "CAD" {
			return fmt.Errorf("transaction %d has unsupported currency %q", transaction.SourceTransactionID, transaction.Currency)
		}
		if _, ok := users[transaction.LegacyUserID]; !ok {
			return fmt.Errorf("transaction %d references missing user", transaction.SourceTransactionID)
		}
		if _, duplicate := seenTransactions[transaction.SourceTransactionID]; duplicate {
			return fmt.Errorf("duplicate transaction %d", transaction.SourceTransactionID)
		}
		seenTransactions[transaction.SourceTransactionID] = struct{}{}
		totals[transaction.LegacyUserID] += transaction.AmountCents
		counts[transaction.LegacyUserID]++
	}
	var netTotal int64
	for userID, user := range users {
		if totals[userID] != user.TransactionNetCents || counts[userID] != user.TransactionCount {
			return fmt.Errorf("user %d transaction reconciliation is inconsistent", userID)
		}
		netTotal += user.TransactionNetCents
	}
	seenWagers := make(map[int64]struct{}, len(report.Wagers))
	for _, wager := range report.Wagers {
		if wager.Currency != "CAD" {
			return fmt.Errorf("wager %d has unsupported currency %q", wager.SourceWagerID, wager.Currency)
		}
		if _, ok := users[wager.LegacyUserID]; !ok {
			return fmt.Errorf("wager %d references missing user", wager.SourceWagerID)
		}
		if _, duplicate := seenWagers[wager.SourceWagerID]; duplicate {
			return fmt.Errorf("duplicate wager %d", wager.SourceWagerID)
		}
		seenWagers[wager.SourceWagerID] = struct{}{}
	}
	if cashTotal != report.ClosingCashTotalCents || freePlayTotal != report.ClosingFreePlayTotalCents || netTotal != report.TransactionNetTotalCents || cashTotal-netTotal != report.DifferenceTotalCents {
		return errors.New("report financial totals are inconsistent")
	}
	return nil
}
