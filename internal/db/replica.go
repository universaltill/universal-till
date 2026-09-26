// Replica identity rewrite (ADR-0011 D2): joining a shop replaces this
// till's DB with the primary's snapshot (staged restore). The join writes
// an identity file BEFORE the restart; this applies it into the restored
// DB right after Open, so the replica keeps its own sync credentials and
// receipt prefix instead of becoming a byte-clone of the primary.
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/universaltill/universal-till/internal/fxlevel"
	"github.com/universaltill/universal-till/internal/secrets"
)

const replicaIdentityName = "replica-identity.json"

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// ReplicaIdentity is written by the join flow next to the DB file.
type ReplicaIdentity struct {
	PrimaryURL    string `json:"primary_url"`
	TillID        string `json:"till_id"`
	Bearer        string `json:"bearer"`
	ReceiptPrefix string `json:"receipt_prefix"`
	TillName      string `json:"till_name"`
	// DeviceID is a fresh marketplace device id for this replica, so the
	// store's fleet lists distinct devices (ADR-0013 two-tier enrolment). The
	// shared store id travels; the primary's device identity and store token
	// do not (ut-docs#2730, TillCloudIdentityPrefixes).
	DeviceID string `json:"device_id"`
	// RegisterID is the register the primary auto-provisioned for this till
	// during enrolment (ut-docs#894). Empty when joining an older primary
	// that doesn't auto-provision.
	RegisterID string `json:"register_id"`
}

// ReplicaIdentityPath locates the identity file for a DB path.
func ReplicaIdentityPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), replicaIdentityName)
}

// StageReplicaIdentity persists the identity for the post-restart apply.
func StageReplicaIdentity(dbPath string, id ReplicaIdentity) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	return os.WriteFile(ReplicaIdentityPath(dbPath), raw, 0o600)
}

// StageRestoreFromReader stages a downloaded snapshot as the pending
// restore (applied by ApplyPendingRestore on next start).
func StageRestoreFromReader(dbPath string, r io.Reader) error {
	pending := filepath.Join(filepath.Dir(dbPath), restorePendingName)
	f, err := os.Create(pending)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		os.Remove(pending)
		return err
	}
	return nil
}

// ApplyReplicaIdentity runs after Open when an identity file exists:
// writes the sync settings, clears sessions (the snapshot carries the
// primary's), and removes the file. Returns whether it applied.
func ApplyReplicaIdentity(sqlDB *sql.DB, dbPath string) (bool, error) {
	path := ReplicaIdentityPath(dbPath)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var id ReplicaIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return false, fmt.Errorf("parse replica identity: %w", err)
	}
	set := func(key, val string) error {
		_, err := sqlDB.Exec(`
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, val)
		return err
	}
	for k, v := range map[string]string{
		"sync.primary_url":    id.PrimaryURL,
		"sync.till_id":        id.TillID,
		"sync.bearer":         id.Bearer,
		"sync.receipt_prefix": id.ReceiptPrefix,
		"sync.till_name":      id.TillName,
	} {
		if err := set(k, v); err != nil {
			return false, fmt.Errorf("apply identity %s: %w", k, err)
		}
	}
	// This till may already have minted its own plugin-settings secrets key
	// (internal/secrets.KeyStore) from a standalone life before joining —
	// that file lives outside the DB, so the snapshot restore above didn't
	// touch it. Clear it now so the next Load fetches the shop's own key
	// from the primary instead of continuing to use the stale standalone
	// one (ut-docs#1745).
	if err := secrets.ClearLocalKeyFile(); err != nil {
		return false, fmt.Errorf("apply identity: %w", err)
	}
	// Deleting the file is not enough on its own: a long-lived process that
	// can reach ApplyReplicaIdentity a second time without a real restart
	// (Android's in-process app.Run reuse, mobile/mobile.go) may already
	// have a secrets.KeyStore registered from the earlier run, still
	// serving the stale key from its in-memory cache — KeyStore.Load never
	// re-stats the file once cached. Invalidate whatever is registered so
	// nothing can read a cached stale key before the fresh KeyStore for
	// THIS run is registered later in startup (internal/app.Run) —
	// independent-review finding, ut-docs#1745.
	secrets.SetDefault(nil)
	// Give this replica its own marketplace device id (the snapshot may have
	// carried the primary's), so it registers as a distinct device under the
	// shared store. ut-docs#2730: drop ALL of the primary's cloud identity
	// first — above all its store token — in case a pre-fix primary's
	// snapshot still carried it (a current primary strips it at source,
	// RedactedJoinSnapshot). The main till registers this replica's device
	// in the cloud (internal/enroll); no credential crosses the LAN.
	if err := DeleteTillCloudIdentity(sqlDB); err != nil {
		return false, fmt.Errorf("clear inherited cloud identity: %w", err)
	}
	if id.DeviceID != "" {
		if err := set("marketplace.device_id", id.DeviceID); err != nil {
			return false, fmt.Errorf("apply identity device: %w", err)
		}
		// Recorded as minted for THIS till, so the enrolment repair
		// (internal/enroll.repairCopiedIdentity) keeps it.
		if err := set("marketplace.device_till_id", id.TillID); err != nil {
			return false, fmt.Errorf("apply identity device till: %w", err)
		}
	}
	// Sales before the join came in the snapshot — push only what THIS
	// till sells from now on.
	if err := set("sync.push_cursor", nowUTC()); err != nil {
		return false, fmt.Errorf("apply identity cursor: %w", err)
	}
	// The snapshot carried the primary's own register identity
	// (sync.till_register_id, ut-docs#268) baked into its settings row —
	// this replica must NOT start life believing it's the primary's
	// register, which would misroute its first Pfandrückgabe payout onto
	// the wrong drawer before a manager ever gets a chance to set it
	// explicitly (independent review finding, ut-docs#268 round 2). The key
	// already carries the "sync." prefix that keeps ordinary admin-sync
	// pulls from re-clobbering it (PerTillSettingPrefixes) — this join-time
	// rewrite is the other half: the ONE path that legitimately inherits a
	// whole settings row via snapshot restore rather than an admin-bundle
	// apply. Two cases (ut-docs#894):
	//   - The primary auto-provisioned a register for this till during
	//     enrolment and sent its id: pin the setting to it, so this till
	//     resolves to its OWN register instead of tripping
	//     pos.ErrRegisterIdentityAmbiguous now that 2+ registers exist.
	//   - Older primary, no id sent: clear the setting so this replica
	//     re-resolves via pos.ResolveTillRegisterID (pre-#894 behaviour).
	if id.RegisterID != "" {
		if err := set("sync.till_register_id", id.RegisterID); err != nil {
			return false, fmt.Errorf("apply till register identity: %w", err)
		}
	} else if _, err := sqlDB.Exec(`DELETE FROM settings WHERE key = 'sync.till_register_id'`); err != nil {
		return false, fmt.Errorf("clear till register identity: %w", err)
	}
	// ADR-0119 §1 (ut-docs#2859): the snapshot carried the primary's
	// visual effects level and its hardware detection. This till starts at
	// auto and detects its own hardware at its next boot. A prefix match
	// via substr, not LIKE: '_' is a LIKE wildcard.
	if _, err := sqlDB.Exec(`DELETE FROM settings WHERE substr(key, 1, length(?)) = ?`,
		fxlevel.SettingsPrefix, fxlevel.SettingsPrefix); err != nil {
		return false, fmt.Errorf("clear inherited effects level: %w", err)
	}
	// ut-docs#2950: the primary's ADR-0092 support session (diagnostics.*)
	// and the hashes of what it last pushed to the cloud (cloudsync.*) are
	// its own state; admin pulls no longer overwrite them, so drop the
	// snapshot's copy here. install.* stays: it is this machine's
	// provisioning marker, and clearing it would re-run provisioning over
	// the owner's window mode. Prefix match via substr, as above.
	for _, p := range []string{"diagnostics.", "cloudsync."} {
		if _, err := sqlDB.Exec(`DELETE FROM settings WHERE substr(key, 1, length(?)) = ?`, p, p); err != nil {
			return false, fmt.Errorf("clear inherited %s* settings: %w", p, err)
		}
	}
	// The snapshot brought the primary's sessions; they mean nothing here.
	if _, err := sqlDB.Exec(`DELETE FROM sessions`); err != nil {
		return false, fmt.Errorf("clear sessions: %w", err)
	}
	return true, os.Remove(path)
}
