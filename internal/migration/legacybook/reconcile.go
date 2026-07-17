// Package legacybook reconciles the archived Cabot Book data, preserves its
// immutable financial history, and promotes verified identities and balances.
package legacybook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SourceSystem   = "legacy-cabot-book"
	SourceCurrency = "CAD"
)

type LegacyUser struct {
	ID              int64
	DisplayName     string
	Email           string
	Role            string
	Balance         string
	FreePlayBalance string
}

type LegacyTransaction struct {
	ID          int64
	UserID      int64
	Amount      string
	Type        string
	Description string
	OccurredAt  time.Time
}

type LegacyWager struct {
	ID          int64
	UserID      int64
	Amount      string
	Description string
	Odds        string
	PlacedAt    time.Time
	Result      string
	Approved    bool
}

type Source interface {
	Users(context.Context) ([]LegacyUser, error)
	Transactions(context.Context) ([]LegacyTransaction, error)
	Wagers(context.Context) ([]LegacyWager, error)
}

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityBlocking Severity = "blocking"
)

type Issue struct {
	Severity     Severity `json:"severity"`
	Code         string   `json:"code"`
	SourceTable  string   `json:"source_table"`
	SourceID     int64    `json:"source_id"`
	LegacyUserID int64    `json:"legacy_user_id,omitempty"`
	Message      string   `json:"message"`
}

type UserReconciliation struct {
	LegacyUserID         int64  `json:"legacy_user_id"`
	DisplayName          string `json:"display_name"`
	Email                string `json:"email,omitempty"`
	LegacyRole           string `json:"legacy_role"`
	TargetRole           string `json:"target_role,omitempty"`
	ClosingCashCents     int64  `json:"closing_cash_cents"`
	ClosingFreePlayCents int64  `json:"closing_free_play_cents"`
	TransactionNetCents  int64  `json:"transaction_net_cents"`
	DifferenceCents      int64  `json:"difference_cents"`
	TransactionCount     int    `json:"transaction_count"`
	Promotable           bool   `json:"promotable"`
}

type Report struct {
	Version                   int                  `json:"version"`
	SourceSystem              string               `json:"source_system"`
	UserCount                 int                  `json:"user_count"`
	TransactionCount          int                  `json:"transaction_count"`
	WagerCount                int                  `json:"wager_count"`
	ClosingCashTotalCents     int64                `json:"closing_cash_total_cents"`
	ClosingFreePlayTotalCents int64                `json:"closing_free_play_total_cents"`
	TransactionNetTotalCents  int64                `json:"transaction_net_total_cents"`
	DifferenceTotalCents      int64                `json:"difference_total_cents"`
	Users                     []UserReconciliation `json:"users"`
	Transactions              []TransactionRecord  `json:"transactions"`
	Wagers                    []WagerRecord        `json:"wagers"`
	Issues                    []Issue              `json:"issues"`
}

type TransactionRecord struct {
	SourceTransactionID int64     `json:"source_transaction_id"`
	LegacyUserID        int64     `json:"legacy_user_id"`
	Currency            string    `json:"currency"`
	AmountCents         int64     `json:"amount_cents"`
	TransactionType     string    `json:"transaction_type"`
	Description         string    `json:"description"`
	OccurredAt          time.Time `json:"occurred_at"`
}

type WagerRecord struct {
	SourceWagerID        int64     `json:"source_wager_id"`
	LegacyUserID         int64     `json:"legacy_user_id"`
	Currency             string    `json:"currency"`
	StakeCents           int64     `json:"stake_cents"`
	AcceptedTerms        string    `json:"accepted_terms"`
	AcceptedAmericanOdds int       `json:"accepted_american_odds"`
	PlacedAt             time.Time `json:"placed_at"`
	Result               string    `json:"result"`
	Approved             bool      `json:"approved"`
}

func (r Report) Promotable() bool {
	for _, issue := range r.Issues {
		if issue.Severity == SeverityBlocking {
			return false
		}
	}
	return true
}

func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func Reconcile(ctx context.Context, source Source) (Report, error) {
	if source == nil {
		return Report{}, errors.New("legacy book source is required")
	}
	users, err := source.Users(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load legacy users: %w", err)
	}
	transactions, err := source.Transactions(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load legacy transactions: %w", err)
	}
	wagers, err := source.Wagers(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load legacy wagers: %w", err)
	}

	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	sort.Slice(transactions, func(i, j int) bool { return transactions[i].ID < transactions[j].ID })
	sort.Slice(wagers, func(i, j int) bool { return wagers[i].ID < wagers[j].ID })
	report := Report{Version: 1, SourceSystem: SourceSystem, UserCount: len(users), TransactionCount: len(transactions), WagerCount: len(wagers)}
	byID := make(map[int64]int, len(users))
	seenTransactions := make(map[int64]struct{}, len(transactions))
	seenWagers := make(map[int64]struct{}, len(wagers))

	for _, user := range users {
		entry := UserReconciliation{
			LegacyUserID: user.ID,
			DisplayName:  strings.TrimSpace(user.DisplayName),
			Email:        strings.ToLower(strings.TrimSpace(user.Email)),
			LegacyRole:   strings.ToLower(strings.TrimSpace(user.Role)),
			Promotable:   true,
		}
		if user.ID <= 0 {
			addUserIssue(&report, &entry, "invalid_user_id", "Users", user.ID, "legacy user ID must be positive")
		}
		if entry.DisplayName == "" {
			addUserIssue(&report, &entry, "missing_display_name", "Users", user.ID, "display name is required")
		}
		entry.TargetRole, err = MapRole(entry.LegacyRole)
		if err != nil {
			addUserIssue(&report, &entry, "unsupported_role", "Users", user.ID, err.Error())
		}
		entry.ClosingCashCents, err = ParseCents(user.Balance)
		if err != nil {
			addUserIssue(&report, &entry, "invalid_cash_amount", "Users", user.ID, err.Error())
		}
		entry.ClosingFreePlayCents, err = ParseCents(user.FreePlayBalance)
		if err != nil {
			addUserIssue(&report, &entry, "invalid_free_play_amount", "Users", user.ID, err.Error())
		}
		if _, exists := byID[user.ID]; exists {
			addUserIssue(&report, &entry, "duplicate_user_id", "Users", user.ID, "legacy user ID is duplicated")
		}
		report.Users = append(report.Users, entry)
		byID[user.ID] = len(report.Users) - 1
	}

	for _, transaction := range transactions {
		entryIndex, exists := byID[transaction.UserID]
		if !exists {
			report.Issues = append(report.Issues, Issue{Severity: SeverityBlocking, Code: "orphan_transaction", SourceTable: "Transactions", SourceID: transaction.ID, LegacyUserID: transaction.UserID, Message: "transaction references an unknown user"})
			continue
		}
		entry := &report.Users[entryIndex]
		if _, duplicate := seenTransactions[transaction.ID]; duplicate {
			addUserIssue(&report, entry, "duplicate_transaction_id", "Transactions", transaction.ID, "legacy transaction ID is duplicated")
			continue
		}
		seenTransactions[transaction.ID] = struct{}{}
		amount, parseErr := ParseCents(transaction.Amount)
		if parseErr != nil {
			addUserIssue(&report, entry, "invalid_transaction_amount", "Transactions", transaction.ID, parseErr.Error())
			continue
		}
		typeName := strings.ToLower(strings.TrimSpace(transaction.Type))
		if (typeName == "credit" && amount <= 0) || (typeName == "debit" && amount >= 0) || (typeName != "credit" && typeName != "debit") {
			addUserIssue(&report, entry, "transaction_sign_mismatch", "Transactions", transaction.ID, fmt.Sprintf("type %q is inconsistent with signed amount", transaction.Type))
			continue
		}
		if (amount > 0 && entry.TransactionNetCents > 9223372036854775807-amount) || (amount < 0 && entry.TransactionNetCents < -9223372036854775807-1-amount) {
			addUserIssue(&report, entry, "transaction_total_overflow", "Transactions", transaction.ID, "transaction total exceeds int64 cents")
			continue
		}
		entry.TransactionNetCents += amount
		entry.TransactionCount++
		if transaction.ID <= 0 {
			addUserIssue(&report, entry, "invalid_transaction_id", "Transactions", transaction.ID, "legacy transaction ID must be positive")
			continue
		}
		if transaction.OccurredAt.IsZero() {
			addUserIssue(&report, entry, "invalid_transaction_time", "Transactions", transaction.ID, "transaction timestamp is required")
			continue
		}
		report.Transactions = append(report.Transactions, TransactionRecord{SourceTransactionID: transaction.ID, LegacyUserID: transaction.UserID, Currency: SourceCurrency, AmountCents: amount, TransactionType: typeName, Description: transaction.Description, OccurredAt: transaction.OccurredAt.UTC()})
	}

	for _, wager := range wagers {
		entryIndex, exists := byID[wager.UserID]
		if !exists {
			report.Issues = append(report.Issues, Issue{Severity: SeverityBlocking, Code: "orphan_wager", SourceTable: "UserBets", SourceID: wager.ID, LegacyUserID: wager.UserID, Message: "wager references an unknown user"})
			continue
		}
		entry := &report.Users[entryIndex]
		if _, duplicate := seenWagers[wager.ID]; duplicate {
			addUserIssue(&report, entry, "duplicate_wager_id", "UserBets", wager.ID, "legacy wager ID is duplicated")
			continue
		}
		seenWagers[wager.ID] = struct{}{}
		stake, parseErr := ParseCents(wager.Amount)
		if parseErr != nil || stake <= 0 {
			message := "wager stake must be positive whole cents"
			if parseErr != nil {
				message = parseErr.Error()
			}
			addUserIssue(&report, entry, "invalid_wager_stake", "UserBets", wager.ID, message)
			continue
		}
		odds, parseErr := parseAmericanOdds(wager.Odds)
		if parseErr != nil {
			addUserIssue(&report, entry, "invalid_wager_odds", "UserBets", wager.ID, parseErr.Error())
			continue
		}
		result, parseErr := mapWagerResult(wager.Result)
		if parseErr != nil {
			addUserIssue(&report, entry, "invalid_wager_result", "UserBets", wager.ID, parseErr.Error())
			continue
		}
		terms := strings.TrimSpace(wager.Description)
		if wager.ID <= 0 || terms == "" || wager.PlacedAt.IsZero() {
			addUserIssue(&report, entry, "invalid_wager_record", "UserBets", wager.ID, "wager ID, accepted terms, and placement timestamp are required")
			continue
		}
		report.Wagers = append(report.Wagers, WagerRecord{SourceWagerID: wager.ID, LegacyUserID: wager.UserID, Currency: SourceCurrency, StakeCents: stake, AcceptedTerms: terms, AcceptedAmericanOdds: odds, PlacedAt: wager.PlacedAt.UTC(), Result: result, Approved: wager.Approved})
	}

	for i := range report.Users {
		entry := &report.Users[i]
		entry.DifferenceCents = entry.ClosingCashCents - entry.TransactionNetCents
		report.ClosingCashTotalCents += entry.ClosingCashCents
		report.ClosingFreePlayTotalCents += entry.ClosingFreePlayCents
		report.TransactionNetTotalCents += entry.TransactionNetCents
		if entry.DifferenceCents != 0 {
			report.Issues = append(report.Issues, Issue{Severity: SeverityWarning, Code: "closing_balance_difference", SourceTable: "Users", SourceID: entry.LegacyUserID, LegacyUserID: entry.LegacyUserID, Message: fmt.Sprintf("closing cash balance differs from transaction net by %d cents", entry.DifferenceCents)})
		}
	}
	report.DifferenceTotalCents = report.ClosingCashTotalCents - report.TransactionNetTotalCents
	sort.SliceStable(report.Issues, func(i, j int) bool {
		if report.Issues[i].LegacyUserID != report.Issues[j].LegacyUserID {
			return report.Issues[i].LegacyUserID < report.Issues[j].LegacyUserID
		}
		if report.Issues[i].SourceTable != report.Issues[j].SourceTable {
			return report.Issues[i].SourceTable < report.Issues[j].SourceTable
		}
		return report.Issues[i].SourceID < report.Issues[j].SourceID
	})
	return report, nil
}

func parseAmericanOdds(value string) (int, error) {
	odds64, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || (odds64 > -100 && odds64 < 100) {
		return 0, fmt.Errorf("odds %q are not valid integral American odds", value)
	}
	return int(odds64), nil
}

func mapWagerResult(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "win":
		return "win", nil
	case "lose", "loss":
		return "loss", nil
	case "tie", "push":
		return "push", nil
	default:
		return "", fmt.Errorf("unsupported wager result %q", value)
	}
}

func addUserIssue(report *Report, user *UserReconciliation, code, table string, sourceID int64, message string) {
	user.Promotable = false
	report.Issues = append(report.Issues, Issue{Severity: SeverityBlocking, Code: code, SourceTable: table, SourceID: sourceID, LegacyUserID: user.LegacyUserID, Message: message})
}

func MapRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "member":
		return "member", nil
	case "admin":
		return "admin", nil
	case "root", "owner":
		return "owner", nil
	default:
		return "", fmt.Errorf("unsupported legacy role %q", role)
	}
}

// ParseCents converts a base-10 amount to integer cents without rounding.
// Values with non-zero digits beyond two decimal places are rejected.
func ParseCents(value string) (int64, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, errors.New("amount is empty")
	}
	sign := int64(1)
	if raw[0] == '-' || raw[0] == '+' {
		if raw[0] == '-' {
			sign = -1
		}
		raw = raw[1:]
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("amount %q is not a base-10 decimal", value)
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, fmt.Errorf("amount %q is not a base-10 decimal", value)
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	for _, digit := range fraction {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("amount %q is not a base-10 decimal", value)
		}
	}
	if len(fraction) > 2 && strings.Trim(fraction[2:], "0") != "" {
		return 0, fmt.Errorf("amount %q contains fractional cents", value)
	}
	if len(fraction) < 2 {
		fraction += strings.Repeat("0", 2-len(fraction))
	}
	if len(fraction) > 2 {
		fraction = fraction[:2]
	}
	frac := int64(0)
	if fraction != "" {
		frac, _ = strconv.ParseInt(fraction, 10, 64)
	}
	if whole > (9223372036854775807-frac)/100 {
		return 0, fmt.Errorf("amount %q exceeds int64 cents", value)
	}
	return sign * (whole*100 + frac), nil
}
