// Package secrets is the till's at-rest encryption primitive for plugin
// secret settings (ADR-0082, ut-docs#1739): AES-256-GCM envelope sealing of
// a plugin_settings.value_json payload under one shop-scoped data key that
// lives in its own file OUTSIDE the SQLite database (keystore.go), so a
// `VACUUM INTO` backup (internal/db/backup.go), a copied .db file, or a
// SELECT over the table never yields a usable Stripe/SumUp credential.
//
// Layering: this package depends on nothing but internal/paths and
// internal/logging, so both internal/data (the plugin-settings repository,
// which does the mechanical seal/open on every write/read) and
// internal/plugins (manifest `type: "secret"` and the key-name heuristic
// the settings page masks on) can import it without a cycle — internal/plugins
// already imports internal/data, which is why the heuristic could not live
// there.
//
// Not built here, on purpose (ADR-0082 non-goals): OS keychain backing
// (Secret Service / DPAPI / Android Keystore) — KeyStore is small enough to
// grow a second backend later without touching Seal/Open.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Prefix marks a sealed value in plugin_settings.value_json. A value without
// it is legacy plaintext (written before ADR-0082) and is returned as-is by
// the repository until its next write re-seals it; the "v1" segment is the
// format version so a future key rotation / cipher change can coexist with
// rows written under this one.
const Prefix = "sealed:v1:"

// KeySize is the AES-256 key length KeyStore generates, fetches and accepts.
const KeySize = 32

var (
	// ErrNotSealed is Open's answer to a value that does not carry Prefix —
	// legacy plaintext, NOT a failure: the caller returns it unchanged.
	ErrNotSealed = errors.New("secrets: value is not sealed")
	// ErrOpen wraps every failure to open a value that DOES carry Prefix
	// (wrong key, truncated, tampered, bad base64). ADR-0082's failure
	// policy: the caller treats the setting as not configured, logs, and
	// never surfaces the ciphertext.
	ErrOpen = errors.New("secrets: cannot open sealed value")
)

// IsSealed reports whether v carries the sealed-value prefix.
func IsSealed(v string) bool { return strings.HasPrefix(v, Prefix) }

// Seal encrypts plaintext under key (AES-256-GCM, fresh random 12-byte
// nonce) and returns Prefix + base64(nonce || ciphertext || tag).
func Seal(key, plaintext []byte) (string, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	out := aead.Seal(nonce, nonce, plaintext, nil)
	return Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open reverses Seal. A value without Prefix returns ErrNotSealed (legacy
// plaintext — not an error condition for the caller); anything else that
// fails to decode or authenticate returns an error wrapping ErrOpen, and
// never a partial/garbage plaintext.
func Open(key []byte, sealed string) ([]byte, error) {
	if !IsSealed(sealed) {
		return nil, ErrNotSealed
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, Prefix))
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %v", ErrOpen, err)
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("%w: payload too short (%d bytes)", ErrOpen, len(raw))
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		// cipher.AEAD.Open's own error is deliberately unspecific; ours is
		// too — a caller must not be able to tell "wrong key" from
		// "tampered" (and nothing in this codebase needs to).
		return nil, fmt.Errorf("%w: authentication failed", ErrOpen)
	}
	return pt, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm: %w", err)
	}
	return aead, nil
}

// IsSecretSettingKey reports whether a plugin setting holds a credential by
// its key name alone — the heuristic that (a) masks the field on the plugin
// settings page (rendered as a password input, value never sent to the
// page) and (b) seals the value at rest (ADR-0082) even when the plugin's
// manifest predates `type: "secret"` and declares nothing. Covers api keys,
// tokens, secrets, passwords, private keys and the connector auth value;
// ut-plugin-payment-stripe's stripe_secret_key and ut-plugin-payment-sumup's
// sumup_api_key both match here, which is what makes ADR-0082 effective
// without a manifest change in those repos (ut-docs#1737 is the follow-up
// that declares them explicitly anyway).
//
// Moved here from internal/pages (where it was unexported) so internal/data
// can apply the same rule on write; the page keeps calling it via
// plugins.IsSecretSettingKey.
func IsSecretSettingKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"secret", "token", "password", "passwd", "api_key", "apikey", "auth_value", "private_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return strings.HasSuffix(k, "_key") || k == "key"
}
