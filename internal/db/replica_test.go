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

// ut-docs#894: enrolment auto-provisions a register for the joining till on
// the primary, and the enroll response hands its id back. Applying the
// replica identity must pin sync.till_register_id to THAT register —
// overwriting the primary's own value the snapshot carried — so the replica
// resolves to its freshly-provisioned register instead of hitting
// ErrRegisterIdentityAmbiguous (2+ active registers, nothing persisted).
func TestApplyReplicaIdentitySetsProvisionedRegisterID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	// The snapshot brought the PRIMARY's register identity.
	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`,
		"sync.till_register_id", "regA-primarys-own"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := StageReplicaIdentity(path, ReplicaIdentity{
		PrimaryURL: "http://primary.local", TillID: "till-2", Bearer: "b",
		ReceiptPrefix: "T2-", TillName: "Back lane", RegisterID: "regB-provisioned",
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	applied, err := ApplyReplicaIdentity(d.DB, path)
	if err != nil || !applied {
		t.Fatalf("apply: applied=%v err=%v", applied, err)
	}

	var v string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v); err != nil {
		t.Fatalf("expected sync.till_register_id set, got scan err: %v", err)
	}
	if v != "regB-provisioned" {
		t.Fatalf("sync.till_register_id = %q, want the auto-provisioned regB-provisioned", v)
	}
}

// The snapshot restore also carries the primary's OWN register identity
// (sync.till_register_id, ut-docs#268 — which register a Pfandrückgabe
// payout resolves against) baked into its settings row. When the primary
// sent no register id (an older primary, pre ut-docs#894), applying the
// replica identity must clear it, so this till re-resolves its own
// register (via pos.ResolveTillRegisterID) instead of starting life
// already believing it's the primary's register — which would misroute
// this replica's very first payout onto the wrong drawer (independent
// review finding, ut-docs#268 round 2).
func TestApplyReplicaIdentityClearsTillRegisterID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`,
		"sync.till_register_id", "regA-primarys-own"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := StageReplicaIdentity(path, ReplicaIdentity{
		PrimaryURL: "http://primary.local", TillID: "till-2", Bearer: "b",
		ReceiptPrefix: "T2-", TillName: "Back lane",
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	applied, err := ApplyReplicaIdentity(d.DB, path)
	if err != nil || !applied {
		t.Fatalf("apply: applied=%v err=%v", applied, err)
	}

	var v string
	scanErr := d.QueryRow(`SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v)
	if scanErr == nil {
		t.Fatalf("expected sync.till_register_id cleared on join, still %q", v)
	}
}
