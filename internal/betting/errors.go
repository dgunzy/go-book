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

	ErrOddsMoved           = errors.New("the offered line moved since the wager was placed")
	ErrMarketNotOpen       = errors.New("market is not open for wagers")
	ErrSelectionInactive   = errors.New("selection is not active")
	ErrSelectionMismatch   = errors.New("selection does not belong to the market")
	ErrUserRestricted      = errors.New("user is restricted from this market")
	ErrIncompleteOutcome   = errors.New("settlement outcome does not cover every selection exactly once")
	ErrWagerMarketMismatch = errors.New("wager does not belong to the market being settled")
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
