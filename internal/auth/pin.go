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

// VerifyPIN checks a PIN against a stored hash. Unknown formats fail closed.
func VerifyPIN(pin, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pin, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
