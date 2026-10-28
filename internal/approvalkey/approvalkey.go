// Package approvalkey holds this till's Ed25519 approval key: the key a till
// signs a cross-shop move with, after an admin or manager approved it with
// their PIN on the till (ut-docs#5127, reference/design-5127-till-approval-
// for-moves.md §2-§3). The cloud pins the public key per device and refuses
// a move whose approval the pinned key did not sign, so a leaked device
// token alone cannot move a till.
//
// Custody copies internal/directivekey: a 32-byte Ed25519 seed in its own
// file under paths.Data("secrets", ...), directory 0700, file 0600 (no-ops
// on Windows, where the data directory's ACL applies), written
// temp-file-then-rename. It is a file, never a database row, which keeps it
// out of every .bkp backup by construction (TestApprovalKeyExcludedFrom...
// in internal/db pins that). It is never rotated in place: a new key
// appears only when the file is gone (new hardware, cleared app data), and
// the cloud then sees a conflict. A file that exists but cannot be parsed is
// logged once, never overwritten, and the till reports no key and cannot
// sign.
//
// The only signer exported for approvals is SignApproval, which builds the
// domain-separated bytes itself; the UI handler that signs must sit behind
// the step-up PIN check (UT-1b).
package approvalkey

import (
	"crypto/ed25519"
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

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// relPath is the key's location under the data root.
var relPath = []string{"secrets", "approval-ed25519.key"}

const (
	// approvalDomain is the first line of the signed move-approval bytes.
	approvalDomain = "universaltill/till-move-approval/v1"
	// popDomain is the first line of the proof-of-possession bytes.
	popDomain = "universaltill/approval-key/v1"

	// Decision values.
	Approve = "approve"
	Decline = "decline"

	seedSize = ed25519.SeedSize
)

var errUnusable = errors.New("approval key file is unusable")

// Store is the approval key file. Safe for concurrent use.
type Store struct {
	path string

	mu     sync.Mutex
	warned bool // an unusable key file was logged once already

	reportWarned atomic.Bool
	checked      atomic.Bool // the signing self-check ran once
}

// NewAt returns a store at an explicit path (tests).
func NewAt(path string) *Store { return &Store{path: path} }

// KID is the key id: the first 16 lowercase hex chars of SHA-256 over the
// 32 raw public-key bytes (the same rule as directivekey.KID).
func KID(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])[:16]
}

// load reads the seed: (key, true, nil) when usable, (nil, false, nil) when
// there is no file, an error for anything else. Caller holds s.mu.
func (s *Store) load() (ed25519.PrivateKey, bool, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, s.unusable(fmt.Sprintf("unreadable (%v)", errors.Unwrap(err)))
	}
	tightenMode(s.path, 0o600)
	if len(raw) != seedSize {
		return nil, false, s.unusable(fmt.Sprintf("%d bytes, want %d", len(raw), seedSize))
	}
	return ed25519.NewKeyFromSeed(raw), true, nil
}

func (s *Store) unusable(why string) error {
	if !s.warned {
		s.warned = true
		logging.L().Warnf("approval key file %s is unusable (%s): this till reports no approval key and cannot approve moves; the file is left in place", s.path, why)
	}
	return fmt.Errorf("%w: %s: %s", errUnusable, s.path, why)
}

// LoadOrCreate returns the key, generating and persisting one when there is
// no file. An existing file it cannot parse is an error and is never
// overwritten.
func (s *Store) LoadOrCreate() (ed25519.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, found, err := s.load()
	if err != nil {
		return nil, err
	}
	if found {
		return k, nil
	}
	seed := make([]byte, seedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("approval key: generate: %w", err)
	}
	if err := s.persist(seed); err != nil {
		return nil, err
	}
	k = ed25519.NewKeyFromSeed(seed)
	logging.L().Infof("approval key created (kid %s)", KID(k.Public().(ed25519.PublicKey)))
	return k, nil
}

func (s *Store) persist(seed []byte) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("approval key: create dir: %w", err)
	}
	tightenMode(dir, 0o700)
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("approval key: write: %w", err)
	}
	// Synced before the rename, and the directory after it: a power cut on a
	// till must not leave a short key file, which would read as "unusable"
	// and never be regenerated.
	_, werr := f.Write(seed)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("approval key: write: %w", werr)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("approval key: commit: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best effort: not supported on every platform
		_ = d.Close()
	}
	return nil
}

// tightenMode removes group/other access from a path created earlier with
// looser modes (best effort; a no-op on Windows, where ACLs apply).
func tightenMode(path string, want os.FileMode) {
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&^want != 0 {
		_ = os.Chmod(path, want)
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// PopMessage is the proof-of-possession bytes (design §2 item 3):
// "universaltill/approval-key/v1\n<device_id>\n<store_external_id>\n<public_key>"
// with public_key base64url, no padding.
func PopMessage(deviceID, storeExternalID, publicKeyB64URL string) []byte {
	return []byte(strings.Join([]string{popDomain, deviceID, storeExternalID, publicKeyB64URL}, "\n"))
}

// Report is the wire form {"kid", "public_key", "pop"} for pair/register
// bodies and the /sync device report, creating the key on first use.
// public_key and pop are base64url without padding. storeExternalID is the
// store the till is in when it reports; at pair/register, before the store
// is known, it is "" (the cloud rebuilds the PoP bytes with the same rule).
// nil when the key cannot be loaded or created (already logged): the till
// then reports no key.
func (s *Store) Report(deviceID, storeExternalID string) map[string]string {
	k, err := s.LoadOrCreate()
	if err != nil {
		if !errors.Is(err, errUnusable) && s.reportWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("approval key not reported: %v", err)
		}
		return nil
	}
	pub := k.Public().(ed25519.PublicKey)
	pk := b64(pub)
	if s.checked.CompareAndSwap(false, true) {
		s.selfCheck(pub)
	}
	return map[string]string{
		"kid":        KID(pub),
		"public_key": pk,
		"pop":        b64(ed25519.Sign(k, PopMessage(deviceID, storeExternalID, pk))),
	}
}

// Approval is one move decision. Every field comes from the cloud's pending
// record (pending_moves on /sync); the till never invents one.
type Approval struct {
	Decision    string // Approve or Decline
	MoveID      string
	DeviceID    string
	SourceStore string
	TargetStore string
	Nonce       string // base64url, from the cloud
	ExpiresAt   string // RFC 3339 UTC, whole seconds, as the cloud sent it
}

// ApprovalMessage builds the exact signed bytes (design §3): the versioned
// domain line then the fields, "\n"-joined. A field that is empty or holds a
// newline, or a decision other than approve/decline, is refused: the bytes
// would be ambiguous.
func ApprovalMessage(a Approval) ([]byte, error) {
	if a.Decision != Approve && a.Decision != Decline {
		return nil, errors.New("approval: decision must be approve or decline")
	}
	fields := []string{a.Decision, a.MoveID, a.DeviceID, a.SourceStore, a.TargetStore, a.Nonce, a.ExpiresAt}
	for _, f := range fields {
		if f == "" || strings.ContainsAny(f, "\n\r") {
			return nil, errors.New("approval: a field is empty or contains a line break")
		}
	}
	return []byte(approvalDomain + "\n" + strings.Join(fields, "\n")), nil
}

// SignApproval signs the approval bytes with this till's key and returns the
// signature, base64url without padding. It never creates a key: no key or an
// unusable file is an error (the till then cannot approve).
func (s *Store) SignApproval(a Approval) (string, error) {
	msg, err := ApprovalMessage(a)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	k, found, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("approval: this till has no approval key")
	}
	return b64(ed25519.Sign(k, msg)), nil
}

var (
	sharedMu sync.Mutex
	shared   *Store
)

// Shared is the process-wide store at the current paths.Data root, so every
// caller (the check-in report, pair, register) shares one store and its
// log-once state. It follows the data root if that changes (tests).
func Shared() *Store {
	p := paths.Data(relPath...)
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil || shared.path != p {
		shared = NewAt(p)
	}
	return shared
}

// selfCheck signs a fixed probe approval through the real SignApproval path
// and verifies it against the public key about to be reported, logging (never
// failing) if it does not verify: a till must not report a key it cannot
// sign with. It also keeps the signer reachable from the server until the
// approval handler lands (UT-1b, ut-docs#5127), which the deadcode baseline
// guard requires; remove it then if the handler's own tests cover the same.
func (s *Store) selfCheck(pub ed25519.PublicKey) {
	probe := Approval{Decision: Decline, MoveID: "self-check", DeviceID: "self-check", SourceStore: "self-check",
		TargetStore: "self-check", Nonce: "self-check", ExpiresAt: "1970-01-01T00:00:00Z"}
	msg, err := ApprovalMessage(probe)
	if err == nil {
		var sig string
		if sig, err = s.SignApproval(probe); err == nil {
			raw, derr := base64.RawURLEncoding.DecodeString(sig)
			if derr != nil || !ed25519.Verify(pub, msg, raw) {
				err = errors.New("signature does not verify")
			}
		}
	}
	if err != nil {
		logging.L().Warnf("approval key self-check failed: %v", err)
	}
}
