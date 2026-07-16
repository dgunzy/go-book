package competition

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid             = errors.New("invalid competition data")
	ErrInvalidTransition   = errors.New("invalid state transition")
	ErrUnauthorized        = errors.New("actor is not authorized")
	ErrOwnSubmission       = errors.New("submitter cannot confirm or dispute their own result")
	ErrNotOpposingSide     = errors.New("actor is not on the opposing side")
	ErrReasonRequired      = errors.New("reason is required")
	ErrNotFound            = errors.New("competition record not found")
	ErrAlreadyExists       = errors.New("competition record already exists")
	ErrIdempotencyConflict = errors.New("idempotency key was reused for a different command")
	ErrResultUnchanged     = errors.New("corrected result is unchanged")
)

// TransitionError reports a rejected operation without losing the aggregate's
// current state. Callers can use errors.Is(err, ErrInvalidTransition).
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
