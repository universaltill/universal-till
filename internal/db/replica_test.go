package db

import (
	"path/filepath"
	"testing"
)

// A replica restores the primary's snapshot (which carries the primary's
// marketplace device id + "registered" marker). Applying the replica identity
// must overwrite the device id with this replica's own and clear the marker,
// so the till re-registers itself as a distinct device under the shared store.
func TestApplyReplicaIdentityReissuesDeviceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	// Simulate the inherited-from-primary settings the snapshot brought.
	for k, v := range map[string]string{
		"marketplace.device_id":         "till-primary",
		"marketplace.device_registered": "till-primary",
		"marketplace.store_id":          "store-shared",
		"marketplace.token":             "shared-token",
	} {
		if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	if err := StageReplicaIdentity(path, ReplicaIdentity{
		PrimaryURL: "http://primary.local", TillID: "till-2", Bearer: "b",
		ReceiptPrefix: "T2-", TillName: "Back lane", DeviceID: "till-replica-xyz",
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	applied, err := ApplyReplicaIdentity(d.DB, path)
	if err != nil || !applied {
		t.Fatalf("apply: applied=%v err=%v", applied, err)
	}

	get := func(key string) string {
		var v string
		_ = d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
		return v
	}
	if got := get("marketplace.device_id"); got != "till-replica-xyz" {
		t.Fatalf("device_id = %q, want the replica's own till-replica-xyz", got)
	}
	if got := get("marketplace.device_registered"); got != "" {
		t.Fatalf("device_registered = %q, want cleared so it re-registers", got)
	}
	// The shared store identity must survive untouched.
	if got := get("marketplace.store_id"); got != "store-shared" {
		t.Fatalf("store_id = %q, want the shared store-shared", got)
	}
	if got := get("marketplace.token"); got != "shared-token" {
		t.Fatalf("store token was disturbed: %q", got)
	}
}
