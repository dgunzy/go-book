package betting

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid             = errors.New("invalid betting data")
	ErrInvalidTransition   = errors.New("invalid state transition")
	ErrUnauthorized        = errors.New("actor is not authorized")
	ErrReasonRequired      = errors.New("reason is required")
	ErrNotFound            = errors.New("betting record not found")
	ErrAlreadyExists       = errors.New("betting record already exists")
	ErrIdempotencyConflict = errors.New("idempotency key was reused for a different command")

	ErrMarketNotOpen     = errors.New("market is not open for wagers")
	ErrSelectionInactive = errors.New("selection is not active")
	ErrSelectionMismatch = errors.New("selection does not belong to the market")
	ErrUserRestricted    = errors.New("user is restricted from this market")
	ErrStakeAboveLimit   = errors.New("stake would exceed this market's limit for one member")
	ErrPayoutAboveLimit  = errors.New("wager would win more than the book's maximum payout")
	// Parlay eligibility. Only head-to-head match markets can be combined:
	// props and futures move together with match results and with each other,
	// and a book that cannot see that correlation prices such a parlay wrong.
	ErrParlayTooFewLegs        = errors.New("a parlay needs at least two legs")
	ErrParlayTooManyLegs       = errors.New("a parlay has too many legs")
	ErrParlayMarketNotEligible = errors.New("only match markets can be parlayed")

	// ErrSideFull means the book has taken all it will take on this side. It is
	// deliberately distinct from ErrStakeAboveLimit: the member may be well
	// under their own limit, and telling them they are over it would be wrong.
	ErrSideFull              = errors.New("this side is full")
	ErrParlayDuplicateMarket = errors.New("a parlay cannot have two legs from the same match")
	ErrParlayTooShort        = errors.New("these legs combine to shorter than even money, which the book does not write as a parlay")
	ErrIncompleteOutcome     = errors.New("settlement outcome does not cover every selection exactly once")
	ErrWagerMarketMismatch   = errors.New("wager does not belong to the market being settled")
)

// TransitionError reports a rejected operation without losing the
// aggregate's current state. Callers can use errors.Is(err, ErrInvalidTransition).
type TransitionError struct {
	Operation string
	State     string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s is not allowed while state is %s", e.Operation, e.State)
}

func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func transitionErr(operation, state string) error {
	return &TransitionError{Operation: operation, State: state}
}
