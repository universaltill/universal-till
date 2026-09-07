package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/secrets"
)

// A replica restores the primary's snapshot (which carries the primary's
// marketplace device id + "registered" marker). Applying the replica identity
// must overwrite the device id with this replica's own and clear the marker,
// so the till re-registers itself as a distinct device under the shared store.
func TestApplyReplicaIdentityReissuesDeviceID(t *testing.T) {
	// Applying the identity now also runs secrets.ClearLocalKeyFile(),
	// which touches paths.Data(...) — sandbox it so a passing test can
	// never be quietly reaching outside its own temp dir (ut-docs#1745
	// review finding).
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

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
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

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
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

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

// ut-docs#1745: a till that was standalone (and so already generated its own
// internal/secrets.KeyStore file, e.g. from configuring a Stripe key before
// ever joining a shop) must not keep using that key after joining — applying
// the replica identity has to clear the local key file too, so the next
// Load fetches the shop's own key from the primary (GET
// /api/sync/secrets-key) instead of silently re-sealing/reading everything
// under the wrong, till-local key.
func TestApplyReplicaIdentityClearsLocalSecretsKey(t *testing.T) {
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	// Simulate this till's pre-join standalone life: it already minted and
	// persisted its own key.
	ks := secrets.NewKeyStore(nil)
	if _, err := ks.Load(t.Context()); err != nil {
		t.Fatalf("seed a standalone key: %v", err)
	}
	if !ks.Exists() {
		t.Fatal("test setup: standalone key file must exist before join")
	}
	// Pin the path itself under this test's own temp dir: if paths.Init
	// above ever regressed, this test would otherwise silently operate on
	// (and delete) a real cwd-relative key file while still passing.
	if dir := paths.DataDir(); !strings.HasPrefix(ks.Path(), dir) {
		t.Fatalf("test setup: key path %q is not under the test data dir %q", ks.Path(), dir)
	}

	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

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

	if ks.Exists() {
		t.Fatal("standalone-generated secrets key must be cleared on replica join")
	}
	if _, err := os.Stat(ks.Path()); !os.IsNotExist(err) {
		t.Fatalf("stat key path after join: %v, want not-exist", err)
	}
}

// A till joining for the first time never had a standalone key — applying
// the identity must not error just because there is nothing to clear.
func TestApplyReplicaIdentitySucceedsWithNoLocalSecretsKey(t *testing.T) {
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	if secrets.NewKeyStore(nil).Exists() {
		t.Fatal("test setup: no standalone key should exist yet")
	}

	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if err := StageReplicaIdentity(path, ReplicaIdentity{
		PrimaryURL: "http://primary.local", TillID: "till-2", Bearer: "b",
		ReceiptPrefix: "T2-", TillName: "Back lane",
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if applied, err := ApplyReplicaIdentity(d.DB, path); err != nil || !applied {
		t.Fatalf("apply: applied=%v err=%v", applied, err)
	}
}

// Independent-review finding (ut-docs#1745): on a long-lived process that
// can run ApplyReplicaIdentity a second time without a real process
// restart (Android's in-process app.Run reuse, mobile/mobile.go), a
// secrets.KeyStore registered by an EARLIER run may already hold the stale
// standalone key cached in memory — KeyStore.Load never re-stats the file
// once cached, so deleting the file on disk alone would not stop that
// already-registered store from keeping serving it. Applying the identity
// must also invalidate whatever store is currently registered, so nothing
// can read a cached stale key in the window before the fresh KeyStore for
// THIS run is registered later in startup (internal/app.Run).
func TestApplyReplicaIdentityInvalidatesRegisteredKeyStore(t *testing.T) {
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	prev := secrets.Default()
	t.Cleanup(func() { secrets.SetDefault(prev) })

	// A previous run's store, already holding a cached key in memory —
	// deliberately NOT at the canonical paths.Data(...) location, to prove
	// the invalidation isn't just "did the file at that path disappear".
	stale := secrets.NewKeyStoreAt(filepath.Join(t.TempDir(), "stale-key.bin"), nil)
	if _, err := stale.Load(t.Context()); err != nil {
		t.Fatalf("seed stale store: %v", err)
	}
	secrets.SetDefault(stale)

	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if err := StageReplicaIdentity(path, ReplicaIdentity{
		PrimaryURL: "http://primary.local", TillID: "till-2", Bearer: "b",
		ReceiptPrefix: "T2-", TillName: "Back lane",
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if applied, err := ApplyReplicaIdentity(d.DB, path); err != nil || !applied {
		t.Fatalf("apply: applied=%v err=%v", applied, err)
	}

	if secrets.Default() != nil {
		t.Fatal("a previously-registered KeyStore must be invalidated on replica join, not left serving a cached stale key")
	}
}
