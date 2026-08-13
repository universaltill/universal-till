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
	// DeviceID is a fresh marketplace device id for this replica. The snapshot
	// carried the primary's device id; overwriting it here keeps the shared
	// store identity (id/token/key) but gives each till its OWN device — so the
	// store's fleet lists distinct devices (ADR-0013 two-tier enrolment).
	DeviceID string `json:"device_id"`
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
	// Give this replica its own marketplace device id (the snapshot carried the
	// primary's), so it registers as a distinct device under the shared store.
	// Clear the "already registered" marker so enrolment re-registers it.
	if id.DeviceID != "" {
		if err := set("marketplace.device_id", id.DeviceID); err != nil {
			return false, fmt.Errorf("apply identity device: %w", err)
		}
		if _, err := sqlDB.Exec(`DELETE FROM settings WHERE key = 'marketplace.device_registered'`); err != nil {
			return false, fmt.Errorf("clear device_registered: %w", err)
		}
	}
	// Sales before the join came in the snapshot — push only what THIS
	// till sells from now on.
	if err := set("sync.push_cursor", nowUTC()); err != nil {
		return false, fmt.Errorf("apply identity cursor: %w", err)
	}
	// The snapshot carried the primary's own register identity
	// (sync.till_register_id, ut-docs#268) baked into its settings row —
	// clear it so this replica re-resolves its OWN register (via
	// pos.ResolveTillRegisterID) rather than starting life already
	// believing it's the primary's register, which would misroute this
	// replica's first Pfandrückgabe payout onto the wrong drawer before a
	// manager ever gets a chance to set it explicitly (independent review
	// finding, ut-docs#268 round 2). The key already carries the "sync."
	// prefix that keeps ordinary admin-sync pulls from re-clobbering it
	// (PerTillSettingPrefixes) — this join-time clear is the other half:
	// the ONE path that legitimately inherits a whole settings row via
	// snapshot restore rather than an admin-bundle apply.
	if _, err := sqlDB.Exec(`DELETE FROM settings WHERE key = 'sync.till_register_id'`); err != nil {
		return false, fmt.Errorf("clear till register identity: %w", err)
	}
	// The snapshot brought the primary's sessions; they mean nothing here.
	if _, err := sqlDB.Exec(`DELETE FROM sessions`); err != nil {
		return false, fmt.Errorf("clear sessions: %w", err)
	}
	return true, os.Remove(path)
}
