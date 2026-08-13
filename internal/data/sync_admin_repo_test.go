package data

import (
	"context"
	"database/sql"
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

// ut-docs#268 round 2 (independent review): sync.till_register_id is this
// till's own register identity (pos.ResolveTillRegisterID) and must NEVER
// travel between tills — a bare "till."-prefixed key would have been
// missed by PerTillSettingPrefixes entirely (till.name is the deliberate,
// documented exception; a same-shaped register-identity key is not), and
// the first version of this key shipped without the "sync." prefix,
// caught here before merge. A primary and a replica each pick their OWN
// register from the same shop-wide registers table, and an admin pull in
// either direction must leave both untouched.
func TestAdminDumpApplyRoundTrip_TillRegisterIDNeverSyncs(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// Same shop-wide registers table on both sides (as a real admin sync
	// would produce), but each till has resolved a DIFFERENT one as its
	// own.
	for _, d := range []*db.DB{primary, replica} {
		mustExec(t, d, `INSERT INTO registers (id, name, is_active) VALUES ('regA', 'Front Till', 1)`)
		mustExec(t, d, `INSERT INTO registers (id, name, is_active) VALUES ('regB', 'Back Till', 1)`)
	}
	mustExec(t, primary, `INSERT INTO settings (key, value) VALUES ('sync.till_register_id', 'regA')`)
	mustExec(t, replica, `INSERT INTO settings (key, value) VALUES ('sync.till_register_id', 'regB')`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, rec := range bundle.Tables["settings"] {
		if rec["key"] == "sync.till_register_id" {
			t.Fatal("till register identity leaked into the admin dump")
		}
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var v string
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v); err != nil || v != "regB" {
		t.Fatalf("replica's own register identity clobbered by an admin pull from the primary: got %q, want regB (err=%v)", v, err)
	}

	// And the reverse direction: applying the REPLICA's bundle (e.g. after
	// a promotion) must not clobber whichever till receives it either —
	// defense in depth (ApplyAdmin's own per-till skip), not just DumpAdmin.
	replicaBundle, err := NewSyncAdminRepo(replica.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump replica: %v", err)
	}
	if err := NewSyncAdminRepo(primary.DB).ApplyAdmin(ctx, wireTrip(t, replicaBundle)); err != nil {
		t.Fatalf("apply to primary: %v", err)
	}
	if err := primary.QueryRow(`SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v); err != nil || v != "regA" {
		t.Fatalf("primary's own register identity clobbered by an admin pull from a replica: got %q, want regA (err=%v)", v, err)
	}
}

// ut-docs#405: the shop's till roster now syncs like any other admin
// table, but bearer_hash is that row's sync-auth secret and must never
// leave the primary — redactCols strips it out of the dump, and migration
// 030 relaxed the column to nullable so the resulting upsert (which force-
// nulls it) doesn't violate NOT NULL. Two tills landing on a replica with
// no bearer_hash at all must not collide against the UNIQUE index either
// (SQLite treats every NULL as distinct).
func TestAdminDumpApplyRoundTrip_TillsRosterRedactsBearerHash(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Front Counter', 'realhash-a')`)
	mustExec(t, primary, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-b', 'Back Counter', 'realhash-b')`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, rec := range bundle.Tables["tills"] {
		if _, ok := rec["bearer_hash"]; ok {
			t.Fatalf("bearer_hash must never appear in a dumped tills row, got %+v", rec)
		}
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	rows, err := replica.Query(`SELECT id, name, bearer_hash FROM tills ORDER BY id`)
	if err != nil {
		t.Fatalf("query replica tills: %v", err)
	}
	defer rows.Close()
	var got []struct {
		id, name string
		hash     sql.NullString
	}
	for rows.Next() {
		var r struct {
			id, name string
			hash     sql.NullString
		}
		if err := rows.Scan(&r.id, &r.name, &r.hash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected both tills to land on the replica, got %d", len(got))
	}
	for _, r := range got {
		if r.hash.Valid {
			t.Fatalf("till %s: bearer_hash leaked to the replica: %q", r.id, r.hash.String)
		}
	}

	// Primary's own copy is untouched — dumping never mutates the source.
	var stillReal string
	if err := primary.QueryRow(`SELECT bearer_hash FROM tills WHERE id = 'till-a'`).Scan(&stillReal); err != nil || stillReal != "realhash-a" {
		t.Fatalf("primary's own bearer_hash must be unchanged: %q %v", stillReal, err)
	}
}

// ut-docs#405 (independent review finding): a replica doesn't only learn
// about tills through this incremental sync — the enrolment snapshot
// (ut-docs#368) is a full-DB copy, so a freshly-joined replica ALREADY has
// every till's REAL bearer_hash the moment it joins, before this table's
// sync ever runs once. skipCols's "leave whatever's there alone" (the
// payment_methods.plugin_id semantics) would let that real secret sit on
// the replica forever, untouched by every subsequent pull — redactCols
// must actively NULL it out on every apply, not merely avoid setting a
// new one, exactly BECAUSE a real value can already be there through a
// path other than this table's own sync.
func TestAdminApplyTills_RedactsPreExistingBearerHash(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Front', 'realhash-a')`)
	// Simulates a snapshot-joined replica: it already holds the primary's
	// real row verbatim, from BEFORE any incremental admin-bundle sync.
	mustExec(t, replica, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Front', 'realhash-a')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var hash sql.NullString
	if err := replica.QueryRow(`SELECT bearer_hash FROM tills WHERE id = 'till-a'`).Scan(&hash); err != nil {
		t.Fatalf("query: %v", err)
	}
	if hash.Valid {
		t.Fatalf("a pre-existing REAL bearer_hash survived an admin-bundle apply on the replica: %q", hash.String)
	}
}

// ut-docs#405 (independent review finding): same staleness problem as
// bearer_hash, for a non-secret reason — a snapshot-joined replica starts
// with the primary's real last_seen_at timestamps baked in, and nothing
// but the primary ever updates them again. Left as skipCols (the first
// draft of this fix), that stale timestamp would sit on the replica
// forever, rendered as if it were live. redactCols means every replica
// honestly shows "—" for a sibling's last-seen time instead.
func TestAdminApplyTills_RedactsPreExistingLastSeenAt(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO tills (id, name, bearer_hash, last_seen_at) VALUES ('till-a', 'Front', 'hash-a', '2026-08-07T10:00:00Z')`)
	// Simulates a snapshot-joined replica holding a much older timestamp
	// than the primary's current one, from before any incremental sync.
	mustExec(t, replica, `INSERT INTO tills (id, name, bearer_hash, last_seen_at) VALUES ('till-a', 'Front', 'hash-a', '2026-01-01T00:00:00Z')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var lastSeen sql.NullString
	if err := replica.QueryRow(`SELECT last_seen_at FROM tills WHERE id = 'till-a'`).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if lastSeen.Valid {
		t.Fatalf("a stale pre-existing last_seen_at survived an admin-bundle apply on the replica: %q", lastSeen.String)
	}
}

// ut-docs#405 (independent review finding): TillByBearerHash bumps
// tills.last_seen_at as a side effect of every single authenticated sync
// call (see syncTill / registerSyncAdmin's admin-bundle endpoint) — if
// that column were part of the dump, the bundle's whole-shop fingerprint
// would move on every tick, permanently defeating the ?have=
// unchanged-poll optimisation for EVERY table, not just tills, the moment
// any till has ever synced once. last_seen_at must never enter the dump
// at all (skipCols, not redactCols — a replica just never learns a
// sibling's live last-seen time, which the UI already renders as "—").
func TestAdminDumpFingerprint_StableAcrossTillAuthTouch(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	mustExec(t, primary, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Replica 1', 'hash-a')`)

	repo := NewSyncAdminRepo(primary.DB)
	before, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}

	// Exactly what every authenticated sync call does.
	if _, ok, err := NewTillsRepo(primary.DB).TillByBearerHash(ctx, "hash-a"); err != nil || !ok {
		t.Fatalf("auth: ok=%v err=%v", ok, err)
	}

	after, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	if before.Fingerprint() != after.Fingerprint() {
		t.Fatalf("fingerprint moved after a mere sync-auth touch (last_seen_at leaked into the dump): %s -> %s",
			before.Fingerprint(), after.Fingerprint())
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
