package oidcclient

import "testing"

func TestClaimsValidate(t *testing.T) {
	t.Parallel()

	if err := (Claims{Subject: "subject", Email: "member@example.com", EmailVerified: true}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	for _, claims := range []Claims{
		{Email: "member@example.com", EmailVerified: true},
		{Subject: "subject", Email: "member@example.com"},
		{Subject: "subject", EmailVerified: true},
	} {
		if err := claims.Validate(); err == nil {
			t.Fatalf("Validate(%+v) error = nil", claims)
		}
	}
}

func TestConstantTimeEqual(t *testing.T) {
	t.Parallel()

	if !constantTimeEqual("same-value", "same-value") {
		t.Fatal("equal values did not match")
	}
	for _, pair := range [][2]string{{"left", "right"}, {"short", "longer"}, {"", ""}} {
		if constantTimeEqual(pair[0], pair[1]) {
			t.Fatalf("constantTimeEqual(%q, %q) = true", pair[0], pair[1])
		}
	}
}
