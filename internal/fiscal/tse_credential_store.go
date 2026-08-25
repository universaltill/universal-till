// TSE operational-credential storage (ADR-0053, ut-docs#802): the till's
// at-rest home for the merchant-scoped signing credential fetched once from
// Universal Till Cloud after reseller provisioning completes. This is the
// till's FIRST arbitrary-merchant-secret storage primitive — deliberately
// minimal, following internal/plugins/oauth/token_client.go's mechanics
// (restrictive file permissions, MkdirAll first, JSON on disk) but under
// paths.Data (till-operational secret data), NOT paths.Plugins (plugin auth
// cache — a different data class).
//
// Custody boundaries (binding, ADR-0045 Decision 2/3 + ADR-0053):
//   - Only the OPERATIONAL credential ever lands here. The admin PUK lives
//     exclusively in the cloud secret store and must never reach a till.
//   - Never logged, never included in a diagnostics/support bundle
//     (ADR-0034/ADR-0022 — the bundle collector is allowlist-based and this
//     path is on no allowlist; TestTSECredentialExcludedFromSupportBundle
//     pins that), never synced to the marketplace.
package fiscal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/paths"
)

// tseCredentialRelPath is the credential's location under the data root: its
// own fiscal/ directory (0700), shared with nothing that any collector or
// sync enumerates.
var tseCredentialRelPath = []string{"fiscal", "tse_operational_credential.json"}

// TSECredentialStore reads/writes the operational credential file. The zero
// value is not usable — construct via NewTSECredentialStore (production
// path under paths.Data) or NewTSECredentialStoreAt (tests).
type TSECredentialStore struct {
	path string
}

// NewTSECredentialStore returns the store at the stable production path,
// paths.Data("fiscal", "tse_operational_credential.json") — never a
// cwd-relative path, so the credential survives a self-update (ADR-0003's
// stable-data-dir rule).
func NewTSECredentialStore() *TSECredentialStore {
	return &TSECredentialStore{path: paths.Data(tseCredentialRelPath...)}
}

// NewTSECredentialStoreAt returns a store rooted at an explicit path — the
// test seam (same convention as issuereport.PendingDir).
func NewTSECredentialStoreAt(path string) *TSECredentialStore {
	return &TSECredentialStore{path: path}
}

// Path returns where the credential lives on disk.
func (s *TSECredentialStore) Path() string { return s.path }

// Exists reports whether a credential is stored locally.
func (s *TSECredentialStore) Exists() bool {
	fi, err := os.Stat(s.path)
	return err == nil && !fi.IsDir()
}

// Save persists cred with restrictive permissions: parent directory 0700
// (MkdirAll first — a fresh install has no fiscal/ dir yet), file 0600. An
// empty credential is rejected outright: fiscal.tse_configured is only ever
// set after a confirmed store, and "confirmed" must never mean an empty map.
func (s *TSECredentialStore) Save(cred map[string]any) error {
	if len(cred) == 0 {
		return fmt.Errorf("tse credential store: refusing to store an empty credential")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("tse credential store: create dir: %w", err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("tse credential store: encode: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("tse credential store: write: %w", err)
	}
	return nil
}

// Load reads the stored credential. ok=false with a nil error means nothing
// is stored yet — same shape as a settings Get.
func (s *TSECredentialStore) Load() (map[string]any, bool, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("tse credential store: read: %w", err)
	}
	var cred map[string]any
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, false, fmt.Errorf("tse credential store: decode: %w", err)
	}
	return cred, true, nil
}
