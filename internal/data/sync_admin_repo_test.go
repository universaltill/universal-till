package data

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// openMigratedDB gives each side of the sync a real, fully migrated schema.
func openMigratedDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// wireTrip simulates the HTTP hop: numbers become float64, like on a replica.
func wireTrip(t *testing.T, b AdminBundle) AdminBundle {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	var out AdminBundle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	return out
}

func TestAdminDumpApplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// Primary's admin state: category, item + barcode, user, shop setting,
	// a per-till setting that must NOT travel, and a translation override.
	mustExec(t, primary, `INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`)
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)
	mustExec(t, primary, `INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('500123', 'itm1', 1)`)
	mustExec(t, primary, `INSERT INTO users (id, username, display_name, role) VALUES ('u1', 'jo', 'Jo', 'cashier')`)
	mustExec(t, primary, `INSERT INTO settings (key, value) VALUES ('shop.name', 'Corner Shop')`)
	mustExec(t, primary, `INSERT INTO settings (key, value) VALUES ('printer.host', '10.0.0.9')`)
	mustExec(t, primary, `INSERT INTO translation_overrides (locale, key, value, updated_at) VALUES ('en', 'nav.home', 'Till', '2026-07-14')`)

	// Replica's own state: per-till keys that must survive, plus a LOCAL
	// item squatting on the primary's SKU under a different id — the prune
	// pass has to clear it before the upsert lands (UNIQUE sku).
	mustExec(t, replica, `INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://primary:8080')`)
	mustExec(t, replica, `INSERT INTO settings (key, value) VALUES ('printer.host', '10.0.0.77')`)
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm-local', 'COLA', 'Local Cola', 999)`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if bundle.Fingerprint() == "" {
		t.Fatal("empty fingerprint")
	}
	again, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	if bundle.Fingerprint() != again.Fingerprint() {
		t.Fatal("fingerprint not stable across identical dumps")
	}
	for _, rec := range bundle.Tables["settings"] {
		if rec["key"] == "printer.host" {
			t.Fatal("per-till setting leaked into the dump")
		}
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var name string
	var price int64
	if err := replica.QueryRow(`SELECT name, base_price FROM items WHERE id = 'itm1'`).
		Scan(&name, &price); err != nil {
		t.Fatalf("synced item missing: %v", err)
	}
	if name != "Cola Can" || price != 120 {
		t.Fatalf("synced item wrong: %s %d", name, price)
	}
	var n int
	_ = replica.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm-local'`).Scan(&n)
	if n != 0 {
		t.Fatal("replica-local SKU squatter survived the prune")
	}
	var v string
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'shop.name'`).Scan(&v); err != nil || v != "Corner Shop" {
		t.Fatalf("shop setting not synced: %q %v", v, err)
	}
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'printer.host'`).Scan(&v); err != nil || v != "10.0.0.77" {
		t.Fatalf("replica per-till setting clobbered: %q %v", v, err)
	}
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'sync.primary_url'`).Scan(&v); err != nil || v != "http://primary:8080" {
		t.Fatalf("replica sync identity clobbered: %q %v", v, err)
	}
	if err := replica.QueryRow(`SELECT value FROM translation_overrides WHERE locale = 'en' AND key = 'nav.home'`).Scan(&v); err != nil || v != "Till" {
		t.Fatalf("translation override not synced: %q %v", v, err)
	}

	// Drift: primary renames + deletes the barcode; fingerprint changes and
	// the deletion propagates.
	mustExec(t, primary, `UPDATE items SET name = 'Cola 330ml' WHERE id = 'itm1'`)
	mustExec(t, primary, `DELETE FROM item_barcodes WHERE barcode = '500123'`)
	drift, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("drift dump: %v", err)
	}
	if drift.Fingerprint() == bundle.Fingerprint() {
		t.Fatal("fingerprint did not change with content")
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, drift)); err != nil {
		t.Fatalf("drift apply: %v", err)
	}
	if err := replica.QueryRow(`SELECT name FROM items WHERE id = 'itm1'`).Scan(&name); err != nil || name != "Cola 330ml" {
		t.Fatalf("rename not applied: %q %v", name, err)
	}
	_ = replica.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = '500123'`).Scan(&n)
	if n != 0 {
		t.Fatal("deleted barcode survived on the replica")
	}
}

// The nasty compound case: a replica-local item BOTH holds the primary's
// SKU under a different id AND is FK-pinned by local history. The prune
// can't delete it, so retiring it must also release the SKU or the upsert
// UNIQUE-violates and every subsequent pull rolls back forever.
func TestAdminApplyRetiresFKPinnedSKUSquatter(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm-local', 'COLA', 'Local Cola', 999)`)
	mustExec(t, replica, `INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`)
	mustExec(t, replica, `INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('mv1', 'itm-local', 'loc1', 'sale', -1)`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	// Apply TWICE: the second pass proves the mangle is idempotent and the
	// retired row no longer poisons subsequent pulls.
	for i := range 2 {
		if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	var name string
	if err := replica.QueryRow(`SELECT name FROM items WHERE id = 'itm1'`).Scan(&name); err != nil || name != "Cola Can" {
		t.Fatalf("primary item did not land: %q %v", name, err)
	}
	var sku string
	var active int
	if err := replica.QueryRow(`SELECT sku, is_active FROM items WHERE id = 'itm-local'`).Scan(&sku, &active); err != nil {
		t.Fatalf("FK-pinned squatter vanished: %v", err)
	}
	if active != 0 || sku != "COLA~itm-local" {
		t.Fatalf("squatter not retired correctly: sku=%q active=%d", sku, active)
	}
}

// Shared plugin settings (Farshid's "change the secret key once, every till
// gets it"): GLOBAL-scope plugin settings follow the primary; register/user
// scoped rows and replica-only plugins stay local; settings of a plugin the
// replica hasn't installed are skipped, not an FK bomb.
func TestAdminSyncSharedPluginSettings(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	seedPlugin := func(d *db.DB, id string) {
		mustExec(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES (?, '1.0.0', ?, 'wasm', 'plugin.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`, id, id)
		mustExec(t, d, `INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES (?, ?, '1.0.0', 'plugin.wasm', 'wasm')`, id, id)
	}
	// Stripe on both tills; "other" only on the primary; "local" only here.
	seedPlugin(primary, "com.ut.stripe")
	seedPlugin(primary, "com.ut.other")
	seedPlugin(replica, "com.ut.stripe")
	seedPlugin(replica, "com.ut.local")

	mustExec(t, primary, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('p1', 'com.ut.stripe', 'stripe_secret_key', '"sk_live_new"', 'global')`)
	mustExec(t, primary, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('p2', 'com.ut.stripe', 'currency', '"gbp"', 'global')`)
	mustExec(t, primary, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id) VALUES ('p3', 'com.ut.stripe', 'stripe_reader_id', '"tmr_primary"', 'register', 'till-primary')`)
	mustExec(t, primary, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('p4', 'com.ut.other', 'endpoint', '"https://x"', 'global')`)

	mustExec(t, replica, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('r1', 'com.ut.stripe', 'stripe_secret_key', '"sk_stale"', 'global')`)
	mustExec(t, replica, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id) VALUES ('r2', 'com.ut.stripe', 'stripe_reader_id', '"tmr_replica"', 'register', 'till-replica')`)
	mustExec(t, replica, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('r3', 'com.ut.local', 'theme', '"dark"', 'global')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, rec := range bundle.Tables["plugin_settings"] {
		if rec["scope"] != "global" {
			t.Fatalf("non-global plugin setting leaked into the dump: %v", rec)
		}
	}

	// Apply twice: the second pass proves delete-then-insert is idempotent.
	for i := range 2 {
		if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}

	var v string
	var n int
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.ut.stripe' AND key = 'stripe_secret_key' AND scope = 'global'`).Scan(&v); err != nil || v != `"sk_live_new"` {
		t.Fatalf("shared secret key not synced: %q %v", v, err)
	}
	_ = replica.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.ut.stripe' AND key = 'stripe_secret_key'`).Scan(&n)
	if n != 1 {
		t.Fatalf("stale global row not replaced, %d rows", n)
	}
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE id = 'r2'`).Scan(&v); err != nil || v != `"tmr_replica"` {
		t.Fatalf("replica register-scoped reader id clobbered: %q %v", v, err)
	}
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE id = 'r3'`).Scan(&v); err != nil || v != `"dark"` {
		t.Fatalf("replica-only plugin setting clobbered: %q %v", v, err)
	}
	_ = replica.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.ut.other'`).Scan(&n)
	if n != 0 {
		t.Fatal("settings landed for a plugin the replica has not installed")
	}
	_ = replica.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.ut.stripe' AND scope = 'register' AND scope_id = 'till-primary'`).Scan(&n)
	if n != 0 {
		t.Fatal("primary's register-scoped row leaked to the replica")
	}

	// The primary drops one global key: the deletion propagates, and the
	// fingerprint moves so the pull loop notices.
	mustExec(t, primary, `DELETE FROM plugin_settings WHERE id = 'p2'`)
	drift, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("drift dump: %v", err)
	}
	if drift.Fingerprint() == bundle.Fingerprint() {
		t.Fatal("fingerprint did not change with plugin setting content")
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, drift)); err != nil {
		t.Fatalf("drift apply: %v", err)
	}
	_ = replica.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.ut.stripe' AND key = 'currency'`).Scan(&n)
	if n != 0 {
		t.Fatal("deleted global plugin setting survived on the replica")
	}
}

func TestAdminApplyDeactivatesUndeletable(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// The replica sold this item (stock movement references it), so when the
	// primary deletes it the prune must fall back to is_active = 0.
	for _, d := range []*db.DB{primary, replica} {
		mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm9', 'BREAD', 'Bread', 110)`)
	}
	mustExec(t, replica, `INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`)
	mustExec(t, replica, `INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('mv1', 'itm9', 'loc1', 'sale', -1)`)
	mustExec(t, primary, `DELETE FROM items WHERE id = 'itm9'`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var active int
	if err := replica.QueryRow(`SELECT is_active FROM items WHERE id = 'itm9'`).Scan(&active); err != nil {
		t.Fatalf("undeletable item vanished (FK should have blocked): %v", err)
	}
	if active != 0 {
		t.Fatal("undeletable item not deactivated")
	}
}
