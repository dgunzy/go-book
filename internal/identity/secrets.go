package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
)

const secretBytes = 32

const (
	sessionTokenHashContext = "cabot-cup/session-token/v1\x00"
	csrfSecretHashContext   = "cabot-cup/csrf-secret/v1\x00"
)

// Secret is an opaque 256-bit bearer value. String and GoString deliberately
// redact it; Value is the explicit boundary used to set a cookie or form value.
type Secret struct {
	value string
}

func GenerateSecret() (Secret, error) { return generateSecret(rand.Reader) }

func generateSecret(random io.Reader) (Secret, error) {
	if random == nil {
		return Secret{}, ErrSecretGeneration
	}
	buffer := make([]byte, secretBytes)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return Secret{}, fmt.Errorf("%w: %v", ErrSecretGeneration, err)
	}
	return Secret{value: base64.RawURLEncoding.EncodeToString(buffer)}, nil
}

func ParseSecret(value string) (Secret, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != secretBytes || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return Secret{}, ErrInvalidSecret
	}
	return Secret{value: value}, nil
}

func (s Secret) Value() string    { return s.value }
func (s Secret) String() string   { return "[REDACTED]" }
func (s Secret) GoString() string { return "identity.Secret([REDACTED])" }
func (s Secret) IsZero() bool     { return s.value == "" }

func (s Secret) equal(other Secret) bool {
	return subtle.ConstantTimeCompare([]byte(s.value), []byte(other.value)) == 1
}

func HashSessionToken(secret Secret) Digest { return hashSecret(sessionTokenHashContext, secret) }
func HashCSRFSecret(secret Secret) Digest   { return hashSecret(csrfSecretHashContext, secret) }

func hashSecret(context string, secret Secret) Digest {
	hash := sha256.New()
	_, _ = hash.Write([]byte(context))
	_, _ = hash.Write([]byte(secret.value))
	var digest Digest
	copy(digest[:], hash.Sum(nil))
	return digest
}

type Digest [sha256.Size]byte

func DigestFromBytes(value []byte) (Digest, error) {
	if len(value) != sha256.Size {
		return Digest{}, ErrInvalidSecret
	}
	var digest Digest
	copy(digest[:], value)
	return digest, nil
}

func (d Digest) Bytes() []byte {
	result := make([]byte, len(d))
	copy(result, d[:])
	return result
}

func (d Digest) Equal(other Digest) bool {
	return subtle.ConstantTimeCompare(d[:], other[:]) == 1
}

func (d Digest) IsZero() bool {
	var zero Digest
	return d.Equal(zero)
}

func (d Digest) String() string   { return "[REDACTED]" }
func (d Digest) GoString() string { return "identity.Digest([REDACTED])" }

func VerifySessionToken(expected Digest, presented string) bool {
	return verifySecret(expected, presented, HashSessionToken)
}

func VerifyCSRFSecret(expected Digest, presented string) bool {
	return verifySecret(expected, presented, HashCSRFSecret)
}

func verifySecret(expected Digest, presented string, hash func(Secret) Digest) bool {
	secret, err := ParseSecret(presented)
	if err != nil {
		return false
	}
	return expected.Equal(hash(secret))
}
