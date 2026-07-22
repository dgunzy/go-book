package bettingpg

import "errors"

var (
	// ErrInsufficientFunds is returned by AcceptWager when the user's locked
	// funding account balance is less than the wager stake.
	ErrInsufficientFunds = errors.New("bettingpg: insufficient funds to accept wager")
	// ErrMarketNotSettleable is returned when SettleMarket or VoidMarket is
	// asked to grade a market outside its allowed source states.
	ErrMarketNotSettleable = errors.New("bettingpg: market is not in a settleable state")
	// ErrMarketNotOpenable is returned when OpenMarket is asked to open a
	// market that is not in draft state.
	ErrMarketNotOpenable = errors.New("bettingpg: market cannot be opened from its current state")
	// ErrMatchMarketExists is returned when a second non-terminal winner
	// market is created for the same match.
	ErrMatchMarketExists = errors.New("bettingpg: match already has an active market")
	// ErrIdempotencyConflict is returned when a repeated request with the
	// same idempotency key describes a different command than the one
	// already recorded.
	ErrIdempotencyConflict = errors.New("bettingpg: idempotency key was reused for a different request")
)
