package identity

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testUserID       ID = "11111111-1111-4111-8111-111111111111"
	testMembershipID ID = "22222222-2222-4222-8222-222222222222"
)

func TestIDValid(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		value ID
		want  bool
	}{
		{value: testUserID, want: true},
		{value: "not-a-uuid", want: false},
		{value: "11111111-1111-4111-8111-11111111111z", want: false},
	} {
		if got := test.value.Valid(); got != test.want {
			t.Errorf("ID(%q).Valid() = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestParseProviderNormalizesAndValidates(t *testing.T) {
	t.Parallel()

	provider, err := ParseProvider(" Google-OIDC ")
	if err != nil {
		t.Fatalf("ParseProvider() error = %v", err)
	}
	if provider != "google-oidc" {
		t.Fatalf("ParseProvider() = %q", provider)
	}

	for _, value := range []string{"g", "1google", "google oidc", strings.Repeat("a", 33)} {
		if _, err := ParseProvider(value); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("ParseProvider(%q) error = %v, want ErrInvalidIdentity", value, err)
		}
	}
}

func TestVerifiedIdentityValidation(t *testing.T) {
	t.Parallel()

	valid := validIdentity()
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*VerifiedIdentity)
	}{
		{name: "unnormalized provider", mutate: func(i *VerifiedIdentity) { i.Provider = "Google" }},
		{name: "blank subject", mutate: func(i *VerifiedIdentity) { i.Subject = "  " }},
		{name: "unnormalized email", mutate: func(i *VerifiedIdentity) { i.Email = "Member@Example.com" }},
		{name: "unverified email", mutate: func(i *VerifiedIdentity) { i.EmailVerified = false }},
		{name: "unnormalized name", mutate: func(i *VerifiedIdentity) { i.DisplayName = " Member " }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			identity := valid
			test.mutate(&identity)
			if err := identity.Validate(); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("Validate() error = %v, want ErrInvalidIdentity", err)
			}
		})
	}
}

func TestAuthIdentityValidationMatchesPersistedContract(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	identity := AuthIdentity{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", UserID: testUserID,
		Provider: "google", Subject: "provider-subject-123", Email: "member@example.com",
		EmailVerified: true, CreatedAt: createdAt, LastAuthenticatedAt: createdAt.Add(time.Hour),
	}
	if err := identity.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if key := (VerifiedIdentity{Provider: identity.Provider, Subject: identity.Subject}).Key(); key != (IdentityKey{Provider: "google", Subject: "provider-subject-123"}) {
		t.Fatalf("Key() = %#v", key)
	}

	withoutEmail := identity
	withoutEmail.Email = ""
	withoutEmail.EmailVerified = false
	if err := withoutEmail.Validate(); err != nil {
		t.Fatalf("schema permits an identity without profile email: %v", err)
	}
	withoutEmail.EmailVerified = true
	if err := withoutEmail.Validate(); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("verified identity without email error = %v", err)
	}
	identity.LastAuthenticatedAt = createdAt.Add(-time.Second)
	if err := identity.Validate(); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("backward authentication time error = %v", err)
	}
}

func TestPrincipalRequiresActiveUserAndMembership(t *testing.T) {
	t.Parallel()

	principal := validPrincipal(RoleMember)
	if err := principal.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	suspended := principal
	suspended.User.Status = UserSuspended
	if err := suspended.Validate(); !errors.Is(err, ErrSignInNotAllowed) {
		t.Fatalf("suspended Validate() error = %v", err)
	}

	revoked := principal
	revoked.Membership.RevokedAt = revoked.Membership.GrantedAt.Add(time.Hour)
	if err := revoked.Validate(); !errors.Is(err, ErrSignInNotAllowed) {
		t.Fatalf("revoked Validate() error = %v", err)
	}

	wrongUser := principal
	wrongUser.Membership.UserID = "33333333-3333-4333-8333-333333333333"
	if err := wrongUser.Validate(); !errors.Is(err, ErrInvalidPrincipal) {
		t.Fatalf("wrong user Validate() error = %v", err)
	}
}

func TestAuthorizeUsesExplicitPrivilegeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role       Role
		permission Permission
		allowed    bool
	}{
		{RoleMember, PermissionUseMemberArea, true},
		{RoleMember, PermissionConfirmResult, true},
		{RoleMember, PermissionManageResults, false},
		{RoleAdmin, PermissionManageResults, true},
		{RoleAdmin, PermissionIssueInvitation, true},
		{RoleAdmin, PermissionMakeFinancialCorrection, false},
		{RoleAdmin, PermissionManageRoles, false},
		{RoleOwner, PermissionMakeFinancialCorrection, true},
		{RoleOwner, PermissionManageRoles, true},
		{RoleOwner, Permission("future_permission"), false},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.role)+"/"+string(test.permission), func(t *testing.T) {
			t.Parallel()
			err := Authorize(validPrincipal(test.role), test.permission)
			if test.allowed && err != nil {
				t.Fatalf("Authorize() error = %v", err)
			}
			if !test.allowed && !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authorize() error = %v, want ErrUnauthorized", err)
			}
		})
	}

	inactive := validPrincipal(RoleOwner)
	inactive.User.Status = UserDisabled
	if err := Authorize(inactive, PermissionUseMemberArea); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("inactive Authorize() error = %v, want ErrUnauthenticated", err)
	}
}

func validIdentity() VerifiedIdentity {
	return VerifiedIdentity{
		Provider: "google", Subject: "provider-subject-123", Email: "member@example.com",
		EmailVerified: true, DisplayName: "Cabot Member",
	}
}

func validPrincipal(role Role) Principal {
	grantedAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	return Principal{
		User:       User{ID: testUserID, DisplayName: "Cabot Member", Email: "member@example.com", Status: UserActive},
		Membership: Membership{ID: testMembershipID, UserID: testUserID, Role: role, GrantedAt: grantedAt},
	}
}
