package secrets

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// keyRelPath is the data key's location under the data root: its own
// secrets/ directory (0700), shared with nothing any collector, backup or
// sync enumerates — the same custody convention as
// internal/fiscal/tse_credential_store.go's fiscal/ directory. It is a
// FILE, not a database row, which is what keeps it out of
// internal/db/backup.go's `VACUUM INTO` snapshot by construction
// (TestSecretsKeyExcludedFromBackupSnapshot in internal/db pins that).
var keyRelPath = []string{"secrets", "plugin_settings_key.bin"}

// fetchRetryFloor is the negative-cache window after a failed replica fetch:
// a Load inside it fails fast with ErrNoKeyYet and does not touch the
// network. This backs a WASM host call a plugin can invoke at unbounded
// frequency, so "try the primary again" must be bounded to one attempt per
// window, not one per call.
const fetchRetryFloor = 30 * time.Second

// ErrNoKeyYet is Load's answer on a replica that has no local key file and
// could not (yet) fetch one from its primary — unreachable, not yet
// upgraded, inside the retry floor, or a fetch already in flight. Callers
// treat a sealed value as not configured until it clears (ADR-0082's
// failure policy); a write of a secret setting fails rather than storing
// plaintext.
var ErrNoKeyYet = errors.New("secrets: encryption key not available yet")

// KeyStore holds the shop-scoped data key on disk and in memory.
//
// Lifecycle is generate-on-first-use, which is where it differs from the
// Save/Load-only fiscal credential store it otherwise mirrors: a primary or
// standalone till mints a random key the first time anything needs one; a
// replica asks its primary (via fetch) once, persists the answer, and from
// then on reads the file exactly like a primary. The zero value is not
// usable — construct via NewKeyStore (production, under paths.Data) or
// NewKeyStoreAt (tests).
type KeyStore struct {
	path string
	// fetch obtains the key from this till's primary. nil means "this till
	// never fetches" (pure standalone, tests). A non-nil fetch may DECLINE
	// by returning (nil, nil): "not a replica right now" — the store then
	// self-generates exactly as with fetch == nil. That lets the production
	// wiring pass one closure that reads sync.primary_url at call time,
	// so a till enrolled as a replica AFTER boot still fetches rather than
	// minting a key its primary does not share.
	fetch func(ctx context.Context) ([]byte, error)
	now   func() time.Time // injectable clock for the retry floor (tests)

	mu       sync.Mutex
	key      []byte    // cached after the first successful Load
	lastFail time.Time // zero until a fetch fails; re-zeroed on success
	fetching bool      // a fetch is in flight on another goroutine
}

// NewKeyStore returns the store at the stable production path,
// paths.Data("secrets", "plugin_settings_key.bin") — never cwd-relative, so
// the key survives a self-update (ADR-0003). fetch is nil for a till that
// should always self-generate; production passes the replica fetch closure
// (see KeyStore.fetch for the decline convention).
func NewKeyStore(fetch func(ctx context.Context) ([]byte, error)) *KeyStore {
	return NewKeyStoreAt(paths.Data(keyRelPath...), fetch)
}

// NewKeyStoreAt returns a store rooted at an explicit path — the test seam
// (same convention as fiscal.NewSigningDeviceCredentialStoreAt).
func NewKeyStoreAt(path string, fetch func(ctx context.Context) ([]byte, error)) *KeyStore {
	return &KeyStore{path: path, fetch: fetch, now: time.Now}
}

// Path returns where the key lives on disk.
func (ks *KeyStore) Path() string { return ks.path }

// ClearLocalKeyFile removes the on-disk key at the canonical production
// path (paths.Data(...)), if one exists. It is a no-op, not an error, when
// no file is present.
//
// This is the replica-join hook: a till that already generated its own
// standalone key (e.g. it had a secret plugin setting configured before
// ever joining a shop) keeps that file untouched by a snapshot restore —
// the key lives outside the SQLite DB by design (see the package doc), so
// swapping in the primary's DB does not disturb it. internal/db's
// ApplyReplicaIdentity calls this as part of applying a shop identity so
// the next Load — via the fetch closure registered at startup — fetches
// the shop's own key from the primary (GET /api/sync/secrets-key) instead
// of continuing to seal/open everything under the wrong, till-local key.
//
// Free function rather than a KeyStore method: ApplyReplicaIdentity runs
// early in startup, before secrets.SetDefault registers the process-wide
// store (internal/app.Run), so there is no KeyStore instance to call this
// on yet — only the well-known canonical path.
func ClearLocalKeyFile() error {
	path := paths.Data(keyRelPath...)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets: clear local key file: %w", err)
	}
	// persist() writes path+".tmp" then renames it into place; a crash
	// between those two steps can leave a plaintext-key .tmp file behind
	// (independent-review finding, ut-docs#1745). Clearing the key is
	// supposed to destroy it — leaving that sibling on disk would defeat
	// the point, so it goes too, whether or not the "real" file existed.
	if err := os.Remove(path + ".tmp"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets: clear local key file: remove .tmp sibling: %w", err)
	}
	return nil
}

// Exists reports whether a key file is stored locally.
func (ks *KeyStore) Exists() bool {
	fi, err := os.Stat(ks.path)
	return err == nil && !fi.IsDir()
}

// Load returns a copy of the 32-byte data key, obtaining it on first use:
//
//   - cached in memory from an earlier Load → returned;
//   - key file present → read, length-validated, cached. A wrong-length
//     file is an error (never silently used, never overwritten — it is
//     left in place for diagnosis);
//   - no file, no fetch (or fetch declines) → 32 random bytes are
//     generated, persisted, cached;
//   - no file, fetch present → ONE fetch attempt, unless one failed inside
//     the last fetchRetryFloor or is in flight right now, in which case
//     ErrNoKeyYet is returned immediately. A successful fetch is persisted
//     so it never repeats.
//
// The file write is write-tmp-then-rename with a 0700 directory and 0600
// file, exactly as the fiscal credential store does and for the same
// reason: Path() only ever observes "absent" or "fully written".
func (ks *KeyStore) Load(ctx context.Context) ([]byte, error) {
	ks.mu.Lock()
	if ks.key != nil {
		k := copyKey(ks.key)
		ks.mu.Unlock()
		return k, nil
	}
	key, found, err := ks.readFile()
	if err != nil {
		ks.mu.Unlock()
		return nil, err
	}
	if found {
		ks.key = key
		ks.mu.Unlock()
		return copyKey(key), nil
	}
	if ks.fetch == nil {
		defer ks.mu.Unlock()
		return ks.generateLocked()
	}
	if ks.fetching {
		ks.mu.Unlock()
		return nil, fmt.Errorf("%w: fetch from primary in progress", ErrNoKeyYet)
	}
	if !ks.lastFail.IsZero() {
		if since := ks.now().Sub(ks.lastFail); since < fetchRetryFloor {
			ks.mu.Unlock()
			return nil, fmt.Errorf("%w: last fetch from primary failed %s ago, retrying after %s", ErrNoKeyYet, since.Round(time.Second), fetchRetryFloor)
		}
	}
	ks.fetching = true
	ks.mu.Unlock()

	// The network call runs WITHOUT the lock so concurrent callers fail
	// fast (above) instead of queueing behind it.
	fetched, fetchErr := ks.fetch(ctx)

	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.fetching = false
	if fetchErr != nil {
		ks.lastFail = ks.now()
		logging.L().Warnf("secrets: could not fetch plugin-settings key from primary: %v (retrying after %s)", fetchErr, fetchRetryFloor)
		return nil, fmt.Errorf("%w: %v", ErrNoKeyYet, fetchErr)
	}
	if fetched == nil {
		// Declined: not a replica. Same path as fetch == nil.
		return ks.generateLocked()
	}
	if len(fetched) != KeySize {
		ks.lastFail = ks.now()
		return nil, fmt.Errorf("%w: primary returned a %d-byte key, want %d", ErrNoKeyYet, len(fetched), KeySize)
	}
	if err := ks.persist(fetched); err != nil {
		// ut-docs#1747: a persist failure after a SUCCESSFUL fetch (e.g. a
		// wedged disk) must arm the same negative-cache floor as a failed
		// fetch — otherwise every settings_get host call turns into a fresh
		// HTTP round-trip to the primary instead of being rate-limited like
		// a fetch failure already is. The key itself was never cached
		// (ks.key stays nil), so the next Load still correctly reports no
		// key; it just doesn't hammer the network to find that out again
		// inside the floor.
		ks.lastFail = ks.now()
		return nil, err
	}
	ks.key = copyKey(fetched)
	ks.lastFail = time.Time{}
	return copyKey(fetched), nil
}

// generateLocked mints a fresh key and persists it. Caller holds ks.mu.
func (ks *KeyStore) generateLocked() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	if err := ks.persist(key); err != nil {
		return nil, err
	}
	ks.key = key
	return copyKey(key), nil
}

// readFile returns (key, true, nil) when a well-formed key file exists,
// (nil, false, nil) when none does, and an error for anything else —
// including a file of the wrong length, which is corruption, not absence.
func (ks *KeyStore) readFile() ([]byte, bool, error) {
	data, err := os.ReadFile(ks.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("secrets: read key file: %w", err)
	}
	if len(data) != KeySize {
		return nil, false, fmt.Errorf("secrets: key file %s is %d bytes, want %d — refusing to use or overwrite it", ks.path, len(data), KeySize)
	}
	return data, true, nil
}

func (ks *KeyStore) persist(key []byte) error {
	if err := os.MkdirAll(filepath.Dir(ks.path), 0o700); err != nil {
		return fmt.Errorf("secrets: create key dir: %w", err)
	}
	tmp := ks.path + ".tmp"
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := os.Rename(tmp, ks.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: commit key file: %w", err)
	}
	return nil
}

func copyKey(k []byte) []byte {
	out := make([]byte, len(k))
	copy(out, k)
	return out
}

// defaultStore is the process-wide KeyStore, registered once at startup by
// internal/app (before pages.Init wires any route that can reach it). Same
// shape as httpx's defaultLocale atomic — a live-settable package singleton.
var defaultStore atomic.Pointer[KeyStore]

// SetDefault registers the process-wide store (nil unregisters — tests).
func SetDefault(ks *KeyStore) { defaultStore.Store(ks) }

// Default returns the registered store, or nil before SetDefault ran.
func Default() *KeyStore { return defaultStore.Load() }

// ErrNoStore is returned by SealWithDefault/OpenWithDefault when no store
// has been registered — a wiring bug, surfaced loudly rather than by
// storing plaintext.
var ErrNoStore = errors.New("secrets: no key store registered (SetDefault not called)")

// SealWithDefault seals plaintext under the registered store's key.
func SealWithDefault(ctx context.Context, plaintext []byte) (string, error) {
	ks := Default()
	if ks == nil {
		return "", ErrNoStore
	}
	key, err := ks.Load(ctx)
	if err != nil {
		return "", err
	}
	return Seal(key, plaintext)
}

// OpenWithDefault opens sealed under the registered store's key. A value
// without Prefix returns ErrNotSealed WITHOUT touching the store (legacy
// plaintext needs no key); a missing store or key surfaces as ErrNoStore /
// ErrNoKeyYet respectively.
func OpenWithDefault(ctx context.Context, sealed string) ([]byte, error) {
	if !IsSealed(sealed) {
		return nil, ErrNotSealed
	}
	ks := Default()
	if ks == nil {
		return nil, ErrNoStore
	}
	key, err := ks.Load(ctx)
	if err != nil {
		return nil, err
	}
	return Open(key, sealed)
}
