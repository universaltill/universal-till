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
)

const replicaIdentityName = "replica-identity.json"

// ReplicaIdentity is written by the join flow next to the DB file.
type ReplicaIdentity struct {
	PrimaryURL    string `json:"primary_url"`
	TillID        string `json:"till_id"`
	Bearer        string `json:"bearer"`
	ReceiptPrefix string `json:"receipt_prefix"`
	TillName      string `json:"till_name"`
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
	// The snapshot brought the primary's sessions; they mean nothing here.
	if _, err := sqlDB.Exec(`DELETE FROM sessions`); err != nil {
		return false, fmt.Errorf("clear sessions: %w", err)
	}
	return true, os.Remove(path)
}
