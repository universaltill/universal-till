// Package directivekey holds the main till's X25519 directive key: the key
// ut-cloud seals owner-set PINs to, so a PIN crosses the cloud without the
// cloud keeping anything it can open (ADR-0115 amendment 2026-09-25; ut-docs
// reference/till-user-directives.md §1-§2).
//
// Custody mirrors internal/secrets/keystore.go: 32 raw private-key bytes in
// its own file under paths.Data("secrets", ...), directory 0700, file 0600
// (no-ops on Windows, where the data directory's ACL applies), written
// temp-file-then-rename. It is a file, never a database row, which keeps it
// out of the backup snapshot by construction
// (TestSecretsKeyExcludedFromBackupSnapshot in internal/db pins that).
//
// The key is never rotated in place: a new one appears only when the file
// is gone. A file that exists but cannot be parsed is logged (never its
// bytes), left untouched, and the till reports no key; PIN directives then
// fail with ErrKeyGone.
package directivekey

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/hpke"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// relPath is the key's location under the data root.
var relPath = []string{"secrets", "directive-x25519.key"}

// Info is the HPKE info string of the till-user PIN seal (contract §2).
const Info = "universaltill/till-user-pin/v1"

// keySize is the raw X25519 private-key length.
const keySize = 32

// ErrKeyGone is the owner-readable failure for a PIN this till cannot open:
// no key, another key's kid, or a ciphertext that does not open under this
// key for this directive (contract §4, shown verbatim by the cloud).
var ErrKeyGone = errors.New("This PIN was sent to a till key that no longer exists. Set the PIN again.")

// ErrMalformed is a pin_sealed value that is not the v1 wire form.
var ErrMalformed = errors.New("bad pin_sealed")

// Store is the directive key file. Safe for concurrent use.
type Store struct {
	path string

	mu     sync.Mutex
	warned bool // an unusable key file was logged once already

	reportWarned atomic.Bool // a load/create failure in Report was logged once
}

// New returns the store at the production path,
// paths.Data("secrets", "directive-x25519.key"), resolved now.
func New() *Store { return NewAt(paths.Data(relPath...)) }

// NewAt returns a store at an explicit path (tests).
func NewAt(path string) *Store { return &Store{path: path} }

// Path is where the key lives on disk.
func (s *Store) Path() string { return s.path }

// AAD is the seal's additional data: store external id, directive id,
// directive type and user id, joined by "\n" (contract §2).
func AAD(storeExternalID, directiveID, directiveType, userID string) string {
	return storeExternalID + "\n" + directiveID + "\n" + directiveType + "\n" + userID
}

// KID is the key id: the first 16 lowercase hex chars of SHA-256 over the
// 32 raw public-key bytes.
func KID(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])[:16]
}

// load reads the key file: (key, true, nil) for a usable key, (nil, false,
// nil) when there is no file, an error for anything else (unreadable, wrong
// length). Caller holds s.mu.
func (s *Store) load() (*ecdh.PrivateKey, bool, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, s.unusable(fmt.Sprintf("unreadable (%v)", errors.Unwrap(err)))
	}
	if len(raw) != keySize {
		return nil, false, s.unusable(fmt.Sprintf("%d bytes, want %d", len(raw), keySize))
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, false, s.unusable("not an X25519 key")
	}
	return k, true, nil
}

// unusable logs (once per store, never the key's bytes) and returns the
// error for an existing key file that cannot be used.
func (s *Store) unusable(why string) error {
	if !s.warned {
		s.warned = true
		logging.L().Warnf("directive key file %s is unusable (%s): this till reports no directive key and cannot open PINs set from the cloud; the file is left in place", s.path, why)
	}
	return fmt.Errorf("%w: %s: %s", errUnusable, s.path, why)
}

// errUnusable marks an existing key file that cannot be used (already
// logged once by unusable).
var errUnusable = errors.New("directive key file is unusable")

// LoadOrCreate returns the key, generating and persisting one when there is
// no file. An existing file it cannot parse is an error and is never
// overwritten.
func (s *Store) LoadOrCreate() (*ecdh.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, found, err := s.load()
	if err != nil {
		return nil, err
	}
	if found {
		return k, nil
	}
	k, err = ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("directive key: generate: %w", err)
	}
	if err := s.persist(k.Bytes()); err != nil {
		return nil, err
	}
	logging.L().Infof("directive key created (kid %s)", KID(k.PublicKey().Bytes()))
	return k, nil
}

func (s *Store) persist(raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("directive key: create dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("directive key: write: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("directive key: commit: %w", err)
	}
	return nil
}

// Report is the device-record form {"kid", "public_key"} (public_key
// base64url without padding, 32 bytes), creating the key on first use. nil
// when the key cannot be loaded or created (already logged): the till then
// reports no key.
func (s *Store) Report() map[string]string {
	k, err := s.LoadOrCreate()
	if err != nil {
		if !errors.Is(err, errUnusable) && s.reportWarned.CompareAndSwap(false, true) {
			// Once per store: Report runs every check-in, and a read-only
			// data dir would otherwise log on every tick (review minor-3).
			logging.L().Warnf("directive key not reported: %v", err)
		}
		return nil
	}
	pub := k.PublicKey().Bytes()
	return map[string]string{
		"kid":        KID(pub),
		"public_key": base64.RawURLEncoding.EncodeToString(pub),
	}
}

// OpenPIN opens a pin_sealed value (v1.<kid>.<b64url enc>.<b64url ct>) for
// the given AAD and returns the PIN's plaintext. It never creates a key:
// no key, an unusable key file, another kid or a ciphertext that does not
// open all return ErrKeyGone. A value that is not the v1 wire form returns
// ErrMalformed. Neither error carries any part of the input.
func (s *Store) OpenPIN(pinSealed, aad string) (string, error) {
	parts := strings.Split(pinSealed, ".")
	if len(parts) != 4 || parts[0] != "v1" || len(parts[1]) != 16 {
		return "", ErrMalformed
	}
	enc, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(enc) != hpke.EncSize {
		return "", ErrMalformed
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(ct) == 0 {
		return "", ErrMalformed
	}
	s.mu.Lock()
	k, found, err := s.load()
	s.mu.Unlock()
	if err != nil || !found {
		return "", ErrKeyGone
	}
	if KID(k.PublicKey().Bytes()) != parts[1] {
		return "", ErrKeyGone
	}
	pt, err := hpke.Open(k, enc, []byte(Info), []byte(aad), ct)
	if err != nil {
		return "", ErrKeyGone
	}
	return string(pt), nil
}
