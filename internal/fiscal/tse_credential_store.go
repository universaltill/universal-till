// Signing-device operational-credential storage (ADR-0053, ut-docs#802;
// named per ADR-0081): the till's at-rest home for the merchant-scoped
// signing credential fetched once from Universal Till Cloud after reseller
// provisioning completes. This is the till's FIRST arbitrary-merchant-secret
// storage primitive — deliberately minimal, following
// internal/plugins/oauth/token_client.go's mechanics (restrictive file
// permissions, MkdirAll first, JSON on disk) but under paths.Data
// (till-operational secret data), NOT paths.Plugins (plugin auth cache — a
// different data class).
//
// Custody boundaries (binding, ADR-0045 Decision 2/3 + ADR-0053):
//   - Only the OPERATIONAL credential ever lands here. The admin PUK lives
//     exclusively in the cloud secret store and must never reach a till.
//   - Never logged, never included in a diagnostics/support bundle
//     (ADR-0034/ADR-0022 — there is no filesystem-walking support-bundle
//     collector in this repo today, so this path is excluded by absence
//     rather than by an explicit denylist;
//     TestSigningDeviceCredentialExcludedFromSupportBundle pins today's
//     disjointness but is not a substitute for re-checking this comment if
//     a real collector is ever added), never synced to the marketplace.
package fiscal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// signingDeviceCredentialRelPath is the credential's location under the data
// root: its own fiscal/ directory (0700), shared with nothing that any
// collector or sync enumerates.
var signingDeviceCredentialRelPath = []string{"fiscal", "signing_device_credential.json"}

// legacyTSECredentialRelPath is where ADR-0048/ADR-0053-era tills wrote the
// same credential before ADR-0081 renamed it. Read by
// NewSigningDeviceCredentialStore's one-time self-heal only; nothing writes
// here anymore.
var legacyTSECredentialRelPath = []string{"fiscal", "tse_operational_credential.json"}

// SigningDeviceCredentialStore reads/writes the operational credential file.
// The zero value is not usable — construct via
// NewSigningDeviceCredentialStore (production path under paths.Data) or
// NewSigningDeviceCredentialStoreAt (tests).
type SigningDeviceCredentialStore struct {
	path string
}

// NewSigningDeviceCredentialStore returns the store at the stable production
// path, paths.Data("fiscal", "signing_device_credential.json") — never a
// cwd-relative path, so the credential survives a self-update (ADR-0003's
// stable-data-dir rule).
//
// Self-healing rename (ADR-0081 Decision 4): a till provisioned before the
// rename still holds its credential under the legacy TSE-named file in the
// same directory. If the stable path is absent and the legacy file is
// present, the legacy file is renamed in place before the store is returned,
// so Load()/Exists() see it with no operator action and no re-provisioning
// (the single-use cloud endpoint would 410 a second fetch). The directory
// already exists on any till old enough to have written the legacy file, so
// this introduces no MkdirAll gap; a fresh install has neither file and
// nothing happens. The stable path wins if both exist — a newer credential
// is never overwritten by an older one. A rename failure is logged and left
// alone: the store then honestly reports nothing stored, which the
// provisioning path treats as "fetch again" rather than silently proceeding
// unsigned.
func NewSigningDeviceCredentialStore() *SigningDeviceCredentialStore {
	s := &SigningDeviceCredentialStore{path: paths.Data(signingDeviceCredentialRelPath...)}
	if s.Exists() {
		return s
	}
	legacy := paths.Data(legacyTSECredentialRelPath...)
	if fi, err := os.Stat(legacy); err != nil || fi.IsDir() {
		return s
	}
	if err := os.Rename(legacy, s.path); err != nil {
		logging.L().Warnf("signing device credential store: could not move legacy credential %s to %s: %v (ADR-0081)", legacy, s.path, err)
	}
	return s
}

// NewSigningDeviceCredentialStoreAt returns a store rooted at an explicit
// path — the test seam (same convention as issuereport.PendingDir).
func NewSigningDeviceCredentialStoreAt(path string) *SigningDeviceCredentialStore {
	return &SigningDeviceCredentialStore{path: path}
}

// Path returns where the credential lives on disk.
func (s *SigningDeviceCredentialStore) Path() string { return s.path }

// Exists reports whether a credential is stored locally.
func (s *SigningDeviceCredentialStore) Exists() bool {
	fi, err := os.Stat(s.path)
	return err == nil && !fi.IsDir()
}

// Save persists cred with restrictive permissions: parent directory 0700
// (MkdirAll first — a fresh install has no fiscal/ dir yet), file 0600. An
// empty credential is rejected outright: fiscal.signing_device_configured is
// only ever set after a confirmed store, and "confirmed" must never mean an
// empty map.
//
// The write is write-tmp-then-rename, not a direct os.WriteFile: WriteFile
// opens O_CREATE|O_TRUNC, so a mid-write failure (full disk, IO error) would
// otherwise leave a truncated/zero-length file behind. That file would still
// pass Exists() (review finding, ut-docs#802) and, via the caller's stat-only
// idempotency fast path, could flip fiscal.signing_device_configured true
// over a credential that was never actually readable. Rename is atomic on
// the same filesystem, so Path() only ever observes "absent" or "fully
// written."
func (s *SigningDeviceCredentialStore) Save(cred map[string]any) error {
	if len(cred) == 0 {
		return fmt.Errorf("signing device credential store: refusing to store an empty credential")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("signing device credential store: create dir: %w", err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("signing device credential store: encode: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("signing device credential store: write: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("signing device credential store: commit: %w", err)
	}
	return nil
}

// Load reads the stored credential. ok=false with a nil error means nothing
// is stored yet — same shape as a settings Get.
func (s *SigningDeviceCredentialStore) Load() (map[string]any, bool, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("signing device credential store: read: %w", err)
	}
	var cred map[string]any
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, false, fmt.Errorf("signing device credential store: decode: %w", err)
	}
	return cred, true, nil
}
