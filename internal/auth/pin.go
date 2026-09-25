// Package auth implements operator PIN login and sessions for the till
// (docs: architecture/pos-auth.md). Everything is local and offline.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A short numeric PIN cannot survive an offline brute force at any hash cost;
// the real defence is the login rate limiter. 100k iterations keeps a PIN
// check fast enough for till hardware (Raspberry Pi).
const (
	pinIterations = 100_000
	pinSaltBytes  = 16
	pinKeyBytes   = 32
)

var ErrBadPINFormat = errors.New("pin must be 4-8 digits")

// ValidatePINFormat enforces the 4-8 digit contract.
func ValidatePINFormat(pin string) error {
	if len(pin) < 4 || len(pin) > 8 {
		return ErrBadPINFormat
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return ErrBadPINFormat
		}
	}
	return nil
}

// HashPIN derives a PBKDF2-SHA256 hash in the stored format
// pbkdf2$sha256$<iter>$<salt-b64>$<hash-b64>.
func HashPIN(pin string) (string, error) {
	if err := ValidatePINFormat(pin); err != nil {
		return "", err
	}
	salt := make([]byte, pinSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, pin, salt, pinIterations, pinKeyBytes)
	if err != nil {
		return "", fmt.Errorf("derive: %w", err)
	}
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s",
		pinIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// ErrBadPINHash is returned by ValidatePINHash for anything that is not a
// well-formed HashPIN string.
var ErrBadPINHash = errors.New("pin hash is not a valid pbkdf2$sha256 hash")

// maxPINIterations caps the iteration count a stored hash may carry. PIN
// login verifies the entered PIN against EVERY active user's hash, so one
// hash with an absurd count would stall every sign-in on the till.
const maxPINIterations = 10 * pinIterations

// parsedPINHash is a decoded pbkdf2$sha256$<iter>$<salt>$<hash> string.
type parsedPINHash struct {
	iter int
	salt []byte
	key  []byte
}

// parsePINHash decodes a stored hash. ok=false for any unknown or malformed
// format (fail closed).
func parsePINHash(stored string) (parsedPINHash, bool) {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return parsedPINHash{}, false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter < 1 {
		return parsedPINHash{}, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return parsedPINHash{}, false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(key) == 0 {
		return parsedPINHash{}, false
	}
	return parsedPINHash{iter: iter, salt: salt, key: key}, true
}

// ValidatePINHash reports whether stored is a hash this till can store for
// an operator: the HashPIN format, a sane iteration count, a non-trivial
// salt and a full-length key. The main till applies it to a hash an
// additional till writes through (ADR-0115 §1) -- it stores exactly what
// it receives, so a malformed value would lock that operator out of every
// till. The value is never included in the error.
func ValidatePINHash(stored string) error {
	p, ok := parsePINHash(stored)
	if !ok || p.iter > maxPINIterations || len(p.salt) < 8 || len(p.key) != pinKeyBytes {
		return ErrBadPINHash
	}
	return nil
}

// VerifyPIN checks a PIN against a stored hash. Unknown formats fail closed.
func VerifyPIN(pin, stored string) bool {
	p, ok := parsePINHash(stored)
	if !ok {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pin, p.salt, p.iter, len(p.key))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, p.key) == 1
}
