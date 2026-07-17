package identity

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSecretRoundTripHashAndRedaction(t *testing.T) {
	t.Parallel()

	secret, err := generateSecret(bytes.NewReader(bytes.Repeat([]byte{0x5a}, secretBytes)))
	if err != nil {
		t.Fatalf("generateSecret() error = %v", err)
	}
	if len(secret.Value()) != 43 {
		t.Fatalf("encoded length = %d, want 43", len(secret.Value()))
	}
	parsed, err := ParseSecret(secret.Value())
	if err != nil {
		t.Fatalf("ParseSecret() error = %v", err)
	}
	sessionHash := HashSessionToken(secret)
	csrfHash := HashCSRFSecret(secret)
	if !sessionHash.Equal(HashSessionToken(parsed)) || !VerifySessionToken(sessionHash, secret.Value()) ||
		!VerifyCSRFSecret(csrfHash, secret.Value()) {
		t.Fatal("secret hash did not round trip")
	}
	if sessionHash.Equal(csrfHash) || VerifyCSRFSecret(sessionHash, secret.Value()) || VerifySessionToken(csrfHash, secret.Value()) {
		t.Fatal("session and CSRF hash domains are not isolated")
	}
	if got := fmt.Sprintf("%v %#v", secret, secret); strings.Contains(got, secret.Value()) || got != "[REDACTED] identity.Secret([REDACTED])" {
		t.Fatalf("formatted secret = %q", got)
	}
	if got := fmt.Sprintf("%v %#v", sessionHash, sessionHash); strings.Contains(got, "5a") || got != "[REDACTED] identity.Digest([REDACTED])" {
		t.Fatalf("formatted digest = %q", got)
	}
}

func TestParseSecretRejectsMalformedValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "not-base64!", "YWJj", strings.Repeat("a", 44)} {
		if _, err := ParseSecret(value); err != ErrInvalidSecret {
			t.Errorf("ParseSecret(%q) error = %v, want ErrInvalidSecret", value, err)
		}
	}
}

func TestDigestBytesAreCopied(t *testing.T) {
	t.Parallel()

	raw := bytes.Repeat([]byte{0x33}, 32)
	digest, err := DigestFromBytes(raw)
	if err != nil {
		t.Fatalf("DigestFromBytes() error = %v", err)
	}
	raw[0] = 0
	copyOfDigest := digest.Bytes()
	copyOfDigest[1] = 0
	if digest[0] != 0x33 || digest[1] != 0x33 {
		t.Fatal("Digest aliases caller-owned memory")
	}
	if _, err := DigestFromBytes(make([]byte, 31)); err != ErrInvalidSecret {
		t.Fatalf("short digest error = %v", err)
	}
}
