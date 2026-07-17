package betting

import (
	"errors"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// IDs used across place_test.go must look like UUIDs because AcceptWager
// builds an events.Envelope whose AggregateID (the wager ID) is validated as
// a UUID.
const (
	testMarketID    ID = "00000000-0000-4000-8000-000000000001"
	testMatchID     ID = "00000000-0000-4000-8000-000000000002"
	testSelectionID ID = "00000000-0000-4000-8000-000000000003"
	testWagerID     ID = "00000000-0000-4000-8000-000000000004"
	testUserID      ID = "00000000-0000-4000-8000-000000000005"
	testAdminID     ID = "00000000-0000-4000-8000-000000000006"
	testEventID     ID = "00000000-0000-4000-8000-000000000007"
)

func openMarket() Market {
	closes := time.Date(2027, time.May, 12, 18, 0, 0, 0, time.UTC)
	return Market{
		ID: testMarketID, Type: MarketMatch, MatchID: testMatchID,
		Title: "Team A vs Team B", State: MarketOpen, Currency: ledger.CAD,
		OpensAt: closes.Add(-48 * time.Hour), ClosesAt: closes,
	}
}

func activeSelection(marketID ID) Selection {
	return Selection{
		ID: testSelectionID, MarketID: marketID, Key: "team-a-win",
		DisplayTerms: "Team A to win the match", OfferedAmericanOdds: 150, Active: true,
	}
}

func placeCommand() PlaceWagerCommand {
	market := openMarket()
	return PlaceWagerCommand{
		WagerID: testWagerID, UserID: testUserID, Market: market, Selection: activeSelection(market.ID),
		FundingAccountType: FundingUserCash, Stake: ledger.Money{Cents: 1000, Currency: ledger.CAD},
		IdempotencyKey: "place:wager-1", Now: market.OpensAt.Add(time.Hour),
	}
}

func TestPlaceWagerSuccess(t *testing.T) {
	t.Parallel()
	wager, err := PlaceWager(placeCommand())
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	if wager.State != WagerPending {
		t.Fatalf("wager state = %s, want pending", wager.State)
	}
	if wager.AcceptedOdds != 150 || wager.AcceptedTerms != "Team A to win the match" {
		t.Fatalf("wager did not snapshot selection terms/odds: %+v", wager)
	}
	if wager.PotentialProfit.Cents != 1500 {
		t.Fatalf("PotentialProfit = %d, want 1500", wager.PotentialProfit.Cents)
	}
}

func TestPlaceWagerValidationFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(PlaceWagerCommand) PlaceWagerCommand
		wantErr error
	}{
		{
			name:    "market closed",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Market.State = MarketClosed; return c },
			wantErr: ErrMarketNotOpen,
		},
		{
			name:    "before opens at",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Now = c.Market.OpensAt.Add(-time.Minute); return c },
			wantErr: ErrMarketNotOpen,
		},
		{
			name:    "after closes at",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Now = c.Market.ClosesAt.Add(time.Minute); return c },
			wantErr: ErrMarketNotOpen,
		},
		{
			name:    "inactive selection",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Selection.Active = false; return c },
			wantErr: ErrSelectionInactive,
		},
		{
			name: "selection belongs to a different market",
			mutate: func(c PlaceWagerCommand) PlaceWagerCommand {
				c.Selection.MarketID = "00000000-0000-4000-8000-0000000000ff"
				return c
			},
			wantErr: ErrSelectionMismatch,
		},
		{
			name: "restricted user",
			mutate: func(c PlaceWagerCommand) PlaceWagerCommand {
				c.RestrictedUsers = []ID{testUserID}
				return c
			},
			wantErr: ErrUserRestricted,
		},
		{
			name:    "zero stake",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Stake.Cents = 0; return c },
			wantErr: ErrInvalid,
		},
		{
			name:    "negative stake",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Stake.Cents = -500; return c },
			wantErr: ErrInvalid,
		},
		{
			name:    "missing idempotency key",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.IdempotencyKey = "  "; return c },
			wantErr: ErrInvalid,
		},
		{
			name:    "wrong currency stake",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.Stake.Currency = "USD"; return c },
			wantErr: ledger.ErrCurrencyMismatch,
		},
		{
			name:    "unsupported funding account",
			mutate:  func(c PlaceWagerCommand) PlaceWagerCommand { c.FundingAccountType = "bank"; return c },
			wantErr: ErrInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := PlaceWager(test.mutate(placeCommand()))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("PlaceWager() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestAcceptWagerProducesBalancedPostingsAndEvent(t *testing.T) {
	t.Parallel()
	wager, err := PlaceWager(placeCommand())
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	refs := AcceptanceAccountRefs{UserFundingAccountID: "user-1-cash", EscrowAccountID: "escrow"}
	occurredAt := time.Date(2027, time.May, 11, 12, 0, 0, 0, time.UTC)

	result, err := AcceptWager(wager, testAdminID, occurredAt, refs, testEventID)
	if err != nil {
		t.Fatalf("AcceptWager() error = %v", err)
	}
	if result.Wager.State != WagerAccepted {
		t.Fatalf("wager state = %s, want accepted", result.Wager.State)
	}
	if err := result.Transaction.Validate(); err != nil {
		t.Fatalf("acceptance transaction Validate() error = %v", err)
	}
	wantKey := "wager:" + string(testWagerID) + ":acceptance"
	if result.Transaction.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key = %q, want %q", result.Transaction.IdempotencyKey, wantKey)
	}
	if result.Transaction.SourceType != "wager" || result.Transaction.SourceID != string(testWagerID) {
		t.Fatalf("transaction source = %s/%s, want wager/%s", result.Transaction.SourceType, result.Transaction.SourceID, testWagerID)
	}
	if len(result.Transaction.Postings) != 2 {
		t.Fatalf("postings = %d, want 2", len(result.Transaction.Postings))
	}
	if err := result.Event.Validate(); err != nil {
		t.Fatalf("event Validate() error = %v", err)
	}
	if result.Event.AggregateID != string(testWagerID) || result.Event.AggregateType != "wager" {
		t.Fatalf("event aggregate = %s/%s, want %s/wager", result.Event.AggregateID, result.Event.AggregateType, testWagerID)
	}

	// Replaying accept on an already-accepted wager must fail with a
	// transition error, never move funds twice.
	if _, err := AcceptWager(result.Wager, testAdminID, occurredAt, refs, testEventID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("re-accept error = %v, want ErrInvalidTransition", err)
	}
}

func TestAcceptWagerRequiresActorAndAccountRefs(t *testing.T) {
	t.Parallel()
	wager, err := PlaceWager(placeCommand())
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}
	occurredAt := time.Date(2027, time.May, 11, 12, 0, 0, 0, time.UTC)
	refs := AcceptanceAccountRefs{UserFundingAccountID: "user-1-cash", EscrowAccountID: "escrow"}

	if _, err := AcceptWager(wager, "", occurredAt, refs, testEventID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing actor error = %v, want ErrUnauthorized", err)
	}
	badRefs := AcceptanceAccountRefs{}
	if _, err := AcceptWager(wager, testAdminID, occurredAt, badRefs, testEventID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing refs error = %v, want ErrInvalid", err)
	}
	sameAccountRefs := AcceptanceAccountRefs{UserFundingAccountID: "acct", EscrowAccountID: "acct"}
	if _, err := AcceptWager(wager, testAdminID, occurredAt, sameAccountRefs, testEventID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same account refs error = %v, want ErrInvalid", err)
	}
}

func TestRejectWagerRequiresReasonAndPendingState(t *testing.T) {
	t.Parallel()
	wager, err := PlaceWager(placeCommand())
	if err != nil {
		t.Fatalf("PlaceWager() error = %v", err)
	}

	if _, err := RejectWager(wager, testAdminID, "  "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("blank reason error = %v, want ErrReasonRequired", err)
	}

	rejected, err := RejectWager(wager, testAdminID, "duplicate wager")
	if err != nil {
		t.Fatalf("RejectWager() error = %v", err)
	}
	if rejected.State != WagerRejected {
		t.Fatalf("wager state = %s, want rejected", rejected.State)
	}

	if _, err := RejectWager(rejected, testAdminID, "again"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("re-reject error = %v, want ErrInvalidTransition", err)
	}
}

func TestPlaceWagerRejectsStakeThatCannotWinACent(t *testing.T) {
	t.Parallel()
	command := placeCommand()
	command.Selection.OfferedAmericanOdds = -1000
	command.Stake = ledger.Money{Cents: 4, Currency: ledger.CAD}
	if _, err := PlaceWager(command); err == nil {
		t.Fatal("PlaceWager() accepted a stake whose potential profit rounds to zero")
	}
}
