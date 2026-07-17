package identity

import (
	"encoding/hex"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var providerPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

type ID string

func (id ID) Valid() bool {
	value := string(id)
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

type Provider string

func ParseProvider(value string) (Provider, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !providerPattern.MatchString(value) {
		return "", fmt.Errorf("%w: provider must match %s", ErrInvalidIdentity, providerPattern)
	}
	return Provider(value), nil
}

func (p Provider) Validate() error {
	parsed, err := ParseProvider(string(p))
	if err != nil || parsed != p {
		return fmt.Errorf("%w: provider is not normalized", ErrInvalidIdentity)
	}
	return nil
}

func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || utf8.RuneCountInString(value) > 320 {
		return "", fmt.Errorf("%w: email is required and limited to 320 characters", ErrInvalidIdentity)
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return "", fmt.Errorf("%w: email is malformed", ErrInvalidIdentity)
	}
	return value, nil
}

// VerifiedIdentity is produced only after an OIDC adapter has validated the
// issuer, audience, signature, nonce, state, redirect URI, and email claim.
// Established identities are always resolved by (Provider, Subject). A verified
// email may be used once to link a new provider subject to a separately approved
// active account; subsequent sign-ins never resolve that identity by email.
type VerifiedIdentity struct {
	Provider      Provider
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type IdentityKey struct {
	Provider Provider
	Subject  string
}

func (i VerifiedIdentity) Key() IdentityKey {
	return IdentityKey{Provider: i.Provider, Subject: i.Subject}
}

func (k IdentityKey) Validate() error {
	if err := k.Provider.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(k.Subject) == "" || utf8.RuneCountInString(k.Subject) > 255 {
		return fmt.Errorf("%w: subject is required and limited to 255 characters", ErrInvalidIdentity)
	}
	return nil
}

// AuthIdentity is the persisted provider link. Email is profile metadata only;
// it is never a lookup key for linking an authentication callback to a user.
type AuthIdentity struct {
	ID                  ID
	UserID              ID
	Provider            Provider
	Subject             string
	Email               string
	EmailVerified       bool
	CreatedAt           time.Time
	LastAuthenticatedAt time.Time
}

func (i AuthIdentity) Validate() error {
	if !i.ID.Valid() || !i.UserID.Valid() || i.CreatedAt.IsZero() {
		return fmt.Errorf("%w: auth identity IDs and creation time are required", ErrInvalidIdentity)
	}
	if err := (IdentityKey{Provider: i.Provider, Subject: i.Subject}).Validate(); err != nil {
		return err
	}
	if i.Email != "" {
		email, err := NormalizeEmail(i.Email)
		if err != nil || email != i.Email {
			return fmt.Errorf("%w: auth identity email is not normalized", ErrInvalidIdentity)
		}
	}
	if i.EmailVerified && i.Email == "" {
		return fmt.Errorf("%w: verified auth identity requires an email", ErrInvalidIdentity)
	}
	if !i.LastAuthenticatedAt.IsZero() && i.LastAuthenticatedAt.Before(i.CreatedAt) {
		return fmt.Errorf("%w: last authentication precedes creation", ErrInvalidIdentity)
	}
	return nil
}

func (i VerifiedIdentity) Validate() error {
	if err := i.Key().Validate(); err != nil {
		return err
	}
	email, err := NormalizeEmail(i.Email)
	if err != nil || email != i.Email || !i.EmailVerified {
		return fmt.Errorf("%w: a normalized, verified email is required", ErrInvalidIdentity)
	}
	name := strings.TrimSpace(i.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > 120 || name != i.DisplayName {
		return fmt.Errorf("%w: display name must be normalized and limited to 120 characters", ErrInvalidIdentity)
	}
	return nil
}

type UserStatus string

const (
	UserInvited   UserStatus = "invited"
	UserActive    UserStatus = "active"
	UserSuspended UserStatus = "suspended"
	UserDisabled  UserStatus = "disabled"
)

func (s UserStatus) Validate() error {
	switch s {
	case UserInvited, UserActive, UserSuspended, UserDisabled:
		return nil
	default:
		return fmt.Errorf("%w: unknown user status %q", ErrInvalidPrincipal, s)
	}
}

type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

func (r Role) Validate() error {
	switch r {
	case RoleMember, RoleAdmin, RoleOwner:
		return nil
	default:
		return fmt.Errorf("%w: unknown role %q", ErrInvalidPrincipal, r)
	}
}

type User struct {
	ID          ID
	DisplayName string
	Email       string
	Status      UserStatus
}

func (u User) Validate() error {
	if !u.ID.Valid() {
		return fmt.Errorf("%w: user ID must be a UUID", ErrInvalidPrincipal)
	}
	name := strings.TrimSpace(u.DisplayName)
	if name == "" || name != u.DisplayName || utf8.RuneCountInString(name) > 120 {
		return fmt.Errorf("%w: display name must be normalized and limited to 120 characters", ErrInvalidPrincipal)
	}
	if u.Email != "" {
		email, err := NormalizeEmail(u.Email)
		if err != nil || email != u.Email {
			return fmt.Errorf("%w: user email is not normalized", ErrInvalidPrincipal)
		}
	}
	return u.Status.Validate()
}

type Membership struct {
	ID        ID
	UserID    ID
	Role      Role
	GrantedAt time.Time
	RevokedAt time.Time
}

func (m Membership) Validate() error {
	if !m.ID.Valid() || !m.UserID.Valid() || m.GrantedAt.IsZero() {
		return fmt.Errorf("%w: membership IDs and grant time are required", ErrInvalidPrincipal)
	}
	if err := m.Role.Validate(); err != nil {
		return err
	}
	if !m.RevokedAt.IsZero() && m.RevokedAt.Before(m.GrantedAt) {
		return fmt.Errorf("%w: membership revocation precedes its grant", ErrInvalidPrincipal)
	}
	return nil
}

func (m Membership) Active() bool { return m.RevokedAt.IsZero() }

type Principal struct {
	User       User
	Membership Membership
}

func (p Principal) Validate() error {
	if err := p.User.Validate(); err != nil {
		return err
	}
	if err := p.Membership.Validate(); err != nil {
		return err
	}
	if p.Membership.UserID != p.User.ID {
		return fmt.Errorf("%w: membership belongs to another user", ErrInvalidPrincipal)
	}
	if p.User.Status != UserActive || !p.Membership.Active() {
		return ErrSignInNotAllowed
	}
	return nil
}
