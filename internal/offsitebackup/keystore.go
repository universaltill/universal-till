package offsitebackup

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/universaltill/universal-till/internal/paths"
)

// The shop's backup key (design note section 14, K1). The till generates one
// random 32-byte key per shop (the DEK) and keeps it in a small file next to
// the database, NOT in SQLite: so it is never inside a snapshot, an uploaded
// backup, a backup copy or a restore. The cloud holds a Key-Vault-wrapped
// escrow copy that is released only through the owner's approval on my.
// The file is deleted when the till leaves its shop. Never logged.

const (
	keyFileName = "offsite-backup.key"
	keyFileV    = 1
	// fpLabel is the domain separator of the key fingerprint.
	fpLabel = "ut-till-backup/v1/fp"
	// hkdfInfoPrefix starts the HKDF info string; the store id follows.
	hkdfInfoPrefix = "ut-till-backup/v1/"
)

// ErrNoKey means this till holds no usable key for its shop.
var ErrNoKey = errors.New("offsitebackup: this till holds no backup key")

// KeyStore reads and writes the key file. The zero value uses
// <dataDir>/keys; tests set Dir.
type KeyStore struct{ Dir string }

var keyMu sync.Mutex

type keyFile struct {
	V          int    `json:"v"`
	StoreID    string `json:"store_id"`
	Generation int    `json:"generation"`
	DEKB64     string `json:"dek_b64"`
	// Older holds the generations this till knew before the current one, so a
	// backup made under one of them can still be restored here.
	Older []olderKey `json:"older,omitempty"`
}

type olderKey struct {
	Generation int    `json:"generation"`
	DEKB64     string `json:"dek_b64"`
}

// OlderKey is a previous generation's key.
type OlderKey struct {
	Generation int
	DEK        []byte
}

func (k KeyStore) dir() string {
	if k.Dir != "" {
		return k.Dir
	}
	return paths.Data("keys")
}

func (k KeyStore) path() string { return filepath.Join(k.dir(), keyFileName) }

// Load returns the key and its generation for storeID. A file that is
// missing, unreadable or malformed is ErrNoKey; a file written for another
// shop (or an empty storeID) is ErrNoKey too. Load never deletes the file.
func (k KeyStore) Load(storeID string) ([]byte, int, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	return k.load(storeID)
}

func (k KeyStore) loadFile(storeID string) (keyFile, error) {
	raw, err := os.ReadFile(k.path())
	if err != nil {
		return keyFile{}, ErrNoKey
	}
	var f keyFile
	if json.Unmarshal(raw, &f) != nil || f.V != keyFileV {
		return keyFile{}, ErrNoKey
	}
	// A read NEVER deletes the key file: an empty store id (a transient
	// settings error) or another shop's id is just "no key for you". Only the
	// explicit leave-shop hook (Delete) removes the file.
	if storeID == "" || f.StoreID != storeID {
		return keyFile{}, ErrNoKey
	}
	return f, nil
}

func (k KeyStore) load(storeID string) ([]byte, int, error) {
	f, err := k.loadFile(storeID)
	if err != nil {
		return nil, 0, err
	}
	dek, err := base64.StdEncoding.DecodeString(f.DEKB64)
	if err != nil || len(dek) != KeySize || f.Generation < 1 {
		return nil, 0, ErrNoKey
	}
	return dek, f.Generation, nil
}

// Older returns the previous generations this till still knows, newest first.
func (k KeyStore) Older(storeID string) []OlderKey {
	keyMu.Lock()
	defer keyMu.Unlock()
	f, err := k.loadFile(storeID)
	if err != nil {
		return nil
	}
	var out []OlderKey
	for _, o := range f.Older {
		if d, err := base64.StdEncoding.DecodeString(o.DEKB64); err == nil && len(d) == KeySize && o.Generation >= 1 {
			out = append(out, OlderKey{o.Generation, d})
		}
	}
	slices.SortFunc(out, func(a, b OlderKey) int { return b.Generation - a.Generation })
	return out
}

// Save writes the key atomically (temp file, then rename), dir 0700, file 0600.
func (k KeyStore) Save(storeID string, generation int, dek []byte) error {
	keyMu.Lock()
	defer keyMu.Unlock()
	return k.save(storeID, generation, dek)
}

func (k KeyStore) save(storeID string, generation int, dek []byte) error {
	if storeID == "" || len(dek) != KeySize || generation < 1 {
		return errors.New("offsitebackup: refusing to save an invalid key")
	}
	if err := os.MkdirAll(k.dir(), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(k.dir(), 0o700)
	// Keep what an existing file for this shop knows: a new current key moves
	// the old one to Older; the highest generation is always the current.
	nf := keyFile{V: keyFileV, StoreID: storeID, Generation: generation, DEKB64: base64.StdEncoding.EncodeToString(dek)}
	if old, err := k.loadFile(storeID); err == nil {
		known := append([]olderKey{{old.Generation, old.DEKB64}}, old.Older...)
		for _, o := range known {
			switch {
			case o.Generation == generation:
			case o.Generation > generation:
				// A lower key arrived than the one held: keep the higher as current.
				nf.Older = append(nf.Older, olderKey{generation, nf.DEKB64})
				nf.Generation, nf.DEKB64 = o.Generation, o.DEKB64
				generation = o.Generation
			default:
				nf.Older = append(nf.Older, o)
			}
		}
		nf.Older = dedupeOlder(nf.Older, nf.Generation)
	}
	b, err := json.Marshal(nf)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(k.dir(), "offsite-backup-*.tmp") // created 0600
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	if serr := tmp.Sync(); werr == nil {
		werr = serr
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, k.path())
	}
	if werr != nil {
		_ = os.Remove(name)
		return werr
	}
	return nil
}

// Generate makes a fresh generation-1 key from crypto/rand and saves it. It
// never overwrites an existing key.
func (k KeyStore) Generate(storeID string) ([]byte, int, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if _, _, err := k.load(storeID); err == nil {
		return nil, 0, errors.New("offsitebackup: a backup key already exists")
	}
	dek := make([]byte, KeySize)
	if _, err := rand.Read(dek); err != nil {
		return nil, 0, err
	}
	if err := k.save(storeID, 1, dek); err != nil {
		return nil, 0, err
	}
	return dek, 1, nil
}

// Delete removes the key file (idempotent). The till calls it when it leaves
// its shop.
func (k KeyStore) Delete() {
	keyMu.Lock()
	defer keyMu.Unlock()
	_ = os.Remove(k.path())
}

// DeleteForStore removes the key file only if it belongs to storeID (the shop
// this till has left). A file of another shop is kept.
func (k KeyStore) DeleteForStore(storeID string) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if storeID == "" {
		return
	}
	if _, err := k.loadFile(storeID); err == nil {
		_ = os.Remove(k.path())
	}
}

// Fingerprint is hex(SHA-256("ut-till-backup/v1/fp" || dek))[0:32]. Both
// sides compute it; it identifies a key without revealing it.
func Fingerprint(dek []byte) string {
	h := sha256.New()
	h.Write([]byte(fpLabel))
	h.Write(dek)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// DeriveKey is the per-backup key: HKDF-SHA256(IKM = dek, salt = backup id
// (16 raw bytes), info = "ut-till-backup/v1/" || store id, L = 32).
func DeriveKey(dek []byte, backupID [16]byte, storeID string) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, fmt.Errorf("offsitebackup: backup key must be %d bytes", KeySize)
	}
	return hkdf.Key(sha256.New, dek, backupID[:], hkdfInfoPrefix+storeID, KeySize)
}

func dedupeOlder(in []olderKey, current int) []olderKey {
	seen := map[int]bool{current: true}
	var out []olderKey
	for _, o := range in {
		if !seen[o.Generation] {
			seen[o.Generation] = true
			out = append(out, o)
		}
	}
	return out
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
