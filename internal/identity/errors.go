package identity

import "errors"

var (
	ErrInvalidIdentity    = errors.New("invalid identity")
	ErrInvalidPrincipal   = errors.New("invalid principal")
	ErrInvalidSession     = errors.New("invalid session")
	ErrInvalidSecret      = errors.New("invalid secret")
	ErrSignInNotAllowed   = errors.New("sign-in not allowed")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrUnauthorized       = errors.New("not authorized")
	ErrSessionExpired     = errors.New("session expired")
	ErrSessionRevoked     = errors.New("session revoked")
	ErrInvalidCSRF        = errors.New("invalid CSRF token")
	ErrSecretGeneration   = errors.New("could not generate secret")
	ErrRepositoryContract = errors.New("identity repository returned invalid data")
)
