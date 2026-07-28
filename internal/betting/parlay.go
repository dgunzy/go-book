package betting

import (
	"strings"
	"time"

	"github.com/dgunzy/go-book/internal/ledger"
)

// ParlayLeg is one selection inside a parlay, with the price and terms
// snapshotted when it was placed. A later line move never changes it, the
// same rule single wagers follow.
type ParlayLeg struct {
	MarketID      ID
	SelectionID   ID
	AcceptedOdds  ledger.AmericanOdds
	AcceptedTerms string
	MarketTitle   string
	// Result is empty until the leg's market settles.
	Result SettlementResult
}

// Parlay is one stake riding on several match results at once. It pays only
// if every leg wins; any losing leg loses the whole bet.
type Parlay struct {
	ID                 ID
	UserID             ID
	FundingAccountType FundingAccountType
	Stake              ledger.Money
	// Price is the combined decimal price the parlay was struck at, juice
	// included.
	Price           ParlayPrice
	PotentialProfit ledger.Money
	Legs            []ParlayLeg
	State           WagerState
	IdempotencyKey  string
	PlacedBy        ID
	PlacedAt        time.Time
}

// PlaceParlayCommand is the pure input for striking a parlay. The caller
// resolves every market, selection and restriction from storage first; this
// function does no I/O.
type PlaceParlayCommand struct {
	ParlayID ID
	UserID   ID
	// Markets and Selections are parallel to each other, one pair per leg, in
	// the order the member picked them.
	Markets      []Market
	Selections   []Selection
	Restrictions []Restriction
	// JuiceBasisPoints is the book's parlay margin per leg beyond the first.
	JuiceBasisPoints int64
	// MaxPayoutCents is the same book-wide ceiling a single wager answers to.
	// A parlay is exactly where a small stake turns into a large liability, so
	// it matters more here than anywhere else.
	MaxPayoutCents     int64
	PlacedBy           ID
	FundingAccountType FundingAccountType
	Stake              ledger.Money
	IdempotencyKey     string
	Now                time.Time
}

// PlaceParlay validates every leg and returns a pending Parlay priced at the
// combined odds.
//
// Only match markets can be parlayed. Everything else on the book is a prop
// or a future whose outcome may be entangled with a match result — "most
// total points" and "to win the cup" move together — and a book that cannot
// see the correlation between two legs is a book that writes free money. A
// head-to-head result is self-contained, so multiplying two of them is honest.
func PlaceParlay(command PlaceParlayCommand) (Parlay, error) {
	if !validID(command.ParlayID) || !validID(command.UserID) {
		return Parlay{}, invalidf("a parlay needs a parlay ID and user ID")
	}
	if len(command.Markets) != len(command.Selections) {
		return Parlay{}, invalidf("every parlay leg needs both its market and its selection")
	}
	if len(command.Markets) < MinParlayLegs {
		return Parlay{}, ErrParlayTooFewLegs
	}
	if len(command.Markets) > MaxParlayLegs {
		return Parlay{}, ErrParlayTooManyLegs
	}
	if command.Now.IsZero() {
		return Parlay{}, invalidf("placing a parlay requires the current time")
	}
	if err := command.FundingAccountType.Validate(); err != nil {
		return Parlay{}, err
	}
	if err := command.Stake.Validate(); err != nil {
		return Parlay{}, err
	}
	if command.Stake.Cents <= 0 {
		return Parlay{}, invalidf("stake must be greater than zero")
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" {
		return Parlay{}, invalidf("placing a parlay requires an idempotency key")
	}

	now := command.Now.UTC()
	seenMarkets := make(map[ID]bool, len(command.Markets))
	legs := make([]ParlayLeg, 0, len(command.Markets))
	legOdds := make([]ledger.AmericanOdds, 0, len(command.Markets))

	for i, market := range command.Markets {
		selection := command.Selections[i]
		if err := market.Validate(); err != nil {
			return Parlay{}, err
		}
		if err := selection.Validate(); err != nil {
			return Parlay{}, err
		}
		if selection.MarketID != market.ID {
			return Parlay{}, ErrSelectionMismatch
		}
		if market.Type != MarketMatch {
			return Parlay{}, ErrParlayMarketNotEligible
		}
		// Two legs from one market are the same event twice: back both sides
		// and the parlay can never win, back one side twice and it is just a
		// bigger single bet wearing a parlay's price.
		if seenMarkets[market.ID] {
			return Parlay{}, ErrParlayDuplicateMarket
		}
		seenMarkets[market.ID] = true

		if market.State != MarketOpen {
			return Parlay{}, ErrMarketNotOpen
		}
		if !market.OpensAt.IsZero() && now.Before(market.OpensAt) {
			return Parlay{}, ErrMarketNotOpen
		}
		if !now.Before(market.ClosesAt) {
			return Parlay{}, ErrMarketNotOpen
		}
		if !selection.Active {
			return Parlay{}, ErrSelectionInactive
		}
		for _, restriction := range command.Restrictions {
			if restriction.Restricts(command.UserID, selection.ID) {
				return Parlay{}, ErrUserRestricted
			}
		}
		if command.Stake.Currency != market.Currency {
			return Parlay{}, ledger.ErrCurrencyMismatch
		}

		legs = append(legs, ParlayLeg{
			MarketID:      market.ID,
			SelectionID:   selection.ID,
			AcceptedOdds:  selection.OfferedAmericanOdds,
			AcceptedTerms: selection.DisplayTerms,
			MarketTitle:   market.Title,
		})
		legOdds = append(legOdds, selection.OfferedAmericanOdds)
	}

	price, err := ParlayPriceFor(legOdds, command.JuiceBasisPoints)
	if err != nil {
		return Parlay{}, err
	}
	profit, err := price.Profit(command.Stake)
	if err != nil {
		return Parlay{}, err
	}
	if profit.Cents <= 0 {
		return Parlay{}, invalidf("stake is too small to win at least one cent at these odds")
	}
	// The payout ceiling binds a parlay exactly as it binds a single bet.
	if command.MaxPayoutCents > 0 && profit.Cents > command.MaxPayoutCents {
		return Parlay{}, ErrPayoutAboveLimit
	}

	return Parlay{
		ID:                 command.ParlayID,
		UserID:             command.UserID,
		FundingAccountType: command.FundingAccountType,
		Stake:              command.Stake,
		Price:              price,
		PotentialProfit:    profit,
		Legs:               legs,
		State:              WagerPending,
		IdempotencyKey:     strings.TrimSpace(command.IdempotencyKey),
		PlacedBy:           command.PlacedBy,
		PlacedAt:           now,
	}, nil
}

// ParlayOutcome is how a parlay graded once every leg is in.
type ParlayOutcome struct {
	Result SettlementResult
	// Price is what the parlay actually pays. It is the placed price unless
	// legs pushed or voided out, in which case the parlay reprices on the
	// legs that are left.
	Price   ParlayPrice
	Stake   ledger.Money
	Profit  ledger.Money
	Returns ledger.Money
}

// GradeParlay decides a parlay once every leg has a result.
//
// Any losing leg loses the bet outright. Legs that push or void drop out and
// the parlay reprices on what remains, which is how books have always treated
// a postponed or tied leg: a three-leg parlay with one push pays as the
// two-leg parlay it became. If every leg drops out, the stake comes back.
//
// A parlay reduced to a single surviving leg pays that leg's own price with
// no parlay juice, because it is no longer a parlay.
func GradeParlay(parlay Parlay, juiceBasisPoints int64) (ParlayOutcome, error) {
	if len(parlay.Legs) == 0 {
		return ParlayOutcome{}, invalidf("a parlay has no legs to grade")
	}
	surviving := make([]ledger.AmericanOdds, 0, len(parlay.Legs))
	for _, leg := range parlay.Legs {
		switch leg.Result {
		case ResultWin:
			surviving = append(surviving, leg.AcceptedOdds)
		case ResultLoss:
			// One dead leg kills the whole bet; nothing else matters.
			return ParlayOutcome{
				Result: ResultLoss, Price: parlay.Price, Stake: parlay.Stake,
				Profit:  ledger.Money{Currency: parlay.Stake.Currency},
				Returns: ledger.Money{Currency: parlay.Stake.Currency},
			}, nil
		case ResultPush, ResultVoid:
			// Drops out of the parlay entirely.
		default:
			return ParlayOutcome{}, invalidf("parlay leg %s has no result yet", leg.SelectionID)
		}
	}

	// Everything pushed or voided: the bet never really happened.
	if len(surviving) == 0 {
		return ParlayOutcome{
			Result: ResultPush, Price: ParlayPrice(decimalScale), Stake: parlay.Stake,
			Profit:  ledger.Money{Currency: parlay.Stake.Currency},
			Returns: parlay.Stake,
		}, nil
	}

	price, err := survivingPrice(surviving, juiceBasisPoints)
	if err != nil {
		return ParlayOutcome{}, err
	}
	profit, err := price.Profit(parlay.Stake)
	if err != nil {
		return ParlayOutcome{}, err
	}
	returns, err := parlay.Stake.Add(profit)
	if err != nil {
		return ParlayOutcome{}, err
	}
	return ParlayOutcome{Result: ResultWin, Price: price, Stake: parlay.Stake, Profit: profit, Returns: returns}, nil
}

// survivingPrice prices whatever is left of a parlay after pushes drop out.
// One surviving leg is no longer a parlay and carries no parlay juice.
func survivingPrice(surviving []ledger.AmericanOdds, juiceBasisPoints int64) (ParlayPrice, error) {
	if len(surviving) == 1 {
		profit, err := surviving[0].Profit(ledger.Money{Cents: decimalScale, Currency: ledger.CAD})
		if err != nil {
			return 0, err
		}
		return ParlayPrice(decimalScale + profit.Cents), nil
	}
	return ParlayPriceFor(surviving, juiceBasisPoints)
}
