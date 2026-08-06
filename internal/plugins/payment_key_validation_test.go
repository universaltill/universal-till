package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// Install-time half of the payment-key hijack fix (2026-07-30): the sync
// guard stops a colliding key from capturing an existing tender row, but
// the plugin author should hear about the collision at install time with
// a clear error, not ship a tender that silently never materializes.

func openRealDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "validate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func paymentManifest(id, key string) *Manifest {
	return &Manifest{
		ID:         id,
		Name:       "Pay " + id,
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Entries: []ManifestEntry{
			{Type: "payment", Key: key, Label: "Tender " + key},
		},
	}
}

func TestPersistManifest_RejectsPaymentKeyCollidingWithNonPluginMethod(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// 'cash' is seeded by 001_init as a built-in tender.
	err := PersistManifest(ctx, d.DB, paymentManifest("com.evil.tender", "cash"), InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a payment entry key colliding with the built-in cash tender")
	}
	if !strings.Contains(err.Error(), "cash") {
		t.Fatalf("collision error should name the key, got: %v", err)
	}
	// The rejected plugin must not be half-installed.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.evil.tender'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
}

func TestPersistManifest_RejectsPaymentKeyOwnedByAnotherPlugin(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, paymentManifest("com.first.pay", "shared"), InstallOptions{}); err != nil {
		t.Fatalf("first plugin must install cleanly: %v", err)
	}
	err := PersistManifest(ctx, d.DB, paymentManifest("com.second.pay", "shared"), InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a payment key already owned by another plugin")
	}
	if !strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), "com.first.pay") {
		t.Fatalf("collision error should name the key and its owner, got: %v", err)
	}
}

func TestPersistManifest_KeyReservedByUninstalledPluginSaysSo(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// The uninstalled-plugin state: the tender ROW survives (sales history
	// references it) but the plugins row is gone. The reservation stands —
	// history keeps its method — but the error must not pretend the owner
	// is installed.
	mustExecSQL(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id)
VALUES ('ghostpay', 'Ghost Pay', 'card', 0, 100, 'com.gone.pay')`)

	err := PersistManifest(ctx, d.DB, paymentManifest("com.new.pay", "ghostpay"), InstallOptions{})
	if err == nil {
		t.Fatal("key held by an uninstalled plugin's retained tender row must still be rejected")
	}
	if !strings.Contains(err.Error(), "ghostpay") || !strings.Contains(err.Error(), "com.gone.pay") || !strings.Contains(err.Error(), "no longer installed") {
		t.Fatalf("error should name key + former owner and say it is no longer installed, got: %v", err)
	}
}

func mustExecSQL(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestRollback_RejectsCollidingPaymentKeys(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()

	// Plugin currently at 2.0.0 with a clean key; its on-disk 1.0.0
	// manifest (the rollback target) declares key "cash" — legacy versions
	// predating validation can carry colliding keys, and rollback writes
	// entries without going through PersistManifest.
	v2 := paymentManifest("com.rb.pay", "rbpay")
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.pay', '1.0.0', 'RB Pay', 'wasm', './main.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`)

	dir := filepath.Join(base, "com.rb.pay", "versions", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"com.rb.pay","name":"RB Pay","version":"1.0.0","entrypoint":"./main.wasm","runtime":"wasm",
"entries":[{"type":"payment","key":"cash","label":"Old Cash Grab"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(d.DB, base)
	err := rm.Rollback(ctx, "com.rb.pay", "1.0.0", "tester")
	if err == nil {
		t.Fatal("rollback restored a payment entry key colliding with the built-in cash tender")
	}
	if !strings.Contains(err.Error(), "cash") {
		t.Fatalf("rollback collision error should name the key, got: %v", err)
	}
	// The current version's entries must survive the failed rollback.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = 'com.rb.pay' AND key = 'rbpay'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("failed rollback must leave current entries intact, found %d", n)
	}
}

func TestPersistManifest_PaymentKeyFormatAndSelfUpgrade(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, paymentManifest("com.bad.pay", ""), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty payment key must be rejected with a clear message, got: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, paymentManifest("com.bad.pay", "a:b"), InstallOptions{}); err == nil || !strings.Contains(err.Error(), ":") {
		t.Fatalf("payment key containing ':' must be rejected, got: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, paymentManifest("com.bad.pay", " cash"), InstallOptions{}); err == nil {
		t.Fatal("payment key with surrounding whitespace must be rejected — ' cash' would mint a padded payment_methods id")
	}

	// Reinstall/upgrade of the SAME plugin with the same key is not a
	// collision — its own earlier entry must not block it.
	if err := PersistManifest(ctx, d.DB, paymentManifest("com.good.pay", "goodpay"), InstallOptions{}); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	m := paymentManifest("com.good.pay", "goodpay")
	m.Version = "1.1.0"
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("self-upgrade with unchanged key must pass validation: %v", err)
	}
}

// Same class of gap as key collisions, but on payment_methods.name (also
// UNIQUE) — ut-docs#16 / ADR-0031's documented residual: two tenders
// wanting the same LABEL previously hard-failed the sync at every startup
// instead of getting a clear install-time error. Extends validation to
// names with the same shape already proven for keys.
func TestPersistManifest_RejectsPaymentNameCollidingWithNonPluginMethod(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := paymentManifest("com.evil.namer", "distinctkey")
	m.Entries[0].Label = "Cash" // collides with the built-in cash tender's NAME, not its key
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a payment entry name colliding with the built-in cash tender")
	}
	if !strings.Contains(err.Error(), "Cash") {
		t.Fatalf("collision error should name the label, got: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.evil.namer'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
}

func TestPersistManifest_RejectsPaymentNameOwnedByAnotherPlugin(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m1 := paymentManifest("com.first.namer", "firstkey")
	m1.Entries[0].Label = "Shared Label"
	if err := PersistManifest(ctx, d.DB, m1, InstallOptions{}); err != nil {
		t.Fatalf("first plugin must install cleanly: %v", err)
	}

	m2 := paymentManifest("com.second.namer", "secondkey")
	m2.Entries[0].Label = "Shared Label"
	err := PersistManifest(ctx, d.DB, m2, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a payment name already owned by another plugin")
	}
	if !strings.Contains(err.Error(), "Shared Label") || !strings.Contains(err.Error(), "com.first.namer") {
		t.Fatalf("collision error should name the label and its owner, got: %v", err)
	}
}

// ut-docs#168: FindPaymentNameConflicts only ever compares a candidate
// label against OTHER plugins' rows — two payment entries sharing one
// label within the SAME manifest were never checked against each other.
// Confirmed pre-fix: such a manifest installed cleanly and only one entry
// materialized into payment_methods at sync time; the other silently
// vanished with no error at install and no warning at sync (an
// intra-plugin collision, not the cross-plugin one the sync's warning
// path detects).
func TestPersistManifest_RejectsDuplicateLabelsWithinSameManifest(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := &Manifest{
		ID:         "com.dup.pay",
		Name:       "Dup Pay",
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Entries: []ManifestEntry{
			{Type: "payment", Key: "keyone", Label: "Same Label"},
			{Type: "payment", Key: "keytwo", Label: "Same Label"},
		},
	}
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a manifest with two payment entries sharing one label")
	}
	if !strings.Contains(err.Error(), "Same Label") {
		t.Fatalf("collision error should name the duplicate label, got: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.dup.pay'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
}

// ut-docs#168: Key gets non-empty/whitespace/':' checks; Label got none —
// an entry with Label:"" installed and synced to a blank-named tender
// (payment_methods.name = ""), and a second plugin doing the same then
// silently lost one entry to the empty-label collision.
func TestPersistManifest_RejectsEmptyPaymentLabel(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := paymentManifest("com.blank.pay", "blankkey")
	m.Entries[0].Label = ""
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty payment label must be rejected with a clear message, got: %v", err)
	}
}

func TestPersistManifest_RejectsWhitespaceOnlyPaymentLabel(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := paymentManifest("com.blank2.pay", "blankkey2")
	m.Entries[0].Label = "   "
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("whitespace-only payment label must be rejected")
	}
}

// Reinstall/upgrade of the SAME plugin with the same label is not a
// collision — its own earlier entry must not block it.
func TestPersistManifest_PaymentNameSelfUpgradeNotAConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := paymentManifest("com.good.namer", "goodkey")
	m.Entries[0].Label = "Good Pay"
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	m2 := paymentManifest("com.good.namer", "goodkey")
	m2.Entries[0].Label = "Good Pay"
	m2.Version = "1.1.0"
	if err := PersistManifest(ctx, d.DB, m2, InstallOptions{}); err != nil {
		t.Fatalf("self-upgrade with unchanged name must pass validation: %v", err)
	}
}
