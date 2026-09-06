package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
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

// warnfContaining reports whether logging.Recent() holds a WARN entry whose
// message contains substr — used to assert deleteMissing's satellite-
// divergence Warnf fired (or didn't), per ut-docs#1592. Callers must
// logging.ResetRecent() before the action under test, since Recent() is a
// process-global ring buffer shared by every test in this binary.
func warnfContaining(substr string) bool {
	for _, p := range logging.Recent() {
		if p.Level == "WARN" && strings.Contains(p.Msg, substr) {
			return true
		}
	}
	return false
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

	// registers doesn't admin-sync (ut-docs#1584 — per-till), so this test
	// seeds the same rows by hand on both sides to simulate the shop-wide
	// roster a real till would have (e.g. from the initial full-DB-snapshot
	// join) — but each till has resolved a DIFFERENT one as its own.
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

// ut-docs#844: fiscal.pending_sign_retries (common.KeyPendingFiscalSignRetries)
// is inherently per-till, tender-time bookkeeping — it was never something
// that should sync between tills — but was missing from PerTillSettingPrefixes.
// During a mixed-version rollout a pre-1.4.0 primary could re-seed its stale
// retry queue onto an already-migrated 1.4.0 replica after that replica's
// one-time boot migration (pages.dropStaleFiscalSignRetryQueue, ADR-0056) had
// already cleared it — the migration only runs at boot, not on every sync.
func TestAdminDumpApplyRoundTrip_FiscalPendingSignRetriesNeverSyncs(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// The pre-1.4.0 primary still carries a stale retry queue.
	mustExec(t, primary, `INSERT INTO settings (key, value) VALUES ('fiscal.pending_sign_retries', '["sale-1","sale-2"]')`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, rec := range bundle.Tables["settings"] {
		if rec["key"] == "fiscal.pending_sign_retries" {
			t.Fatal("fiscal.pending_sign_retries leaked into the admin dump")
		}
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var n int
	_ = replica.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'fiscal.pending_sign_retries'`).Scan(&n)
	if n != 0 {
		t.Fatal("stale pre-1.4.0 retry queue re-synced onto the replica after an admin pull")
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

// A stale pre-#785 primary (or any other future writer with the same bug)
// could in principle hand the sync apply path a bundle with two global-scope
// rows for the same (plugin_id, key). Before ut-docs#807, that aborted the
// ENTIRE admin-bundle apply — not just plugin_settings — against
// ux_plugin_settings_global (migration 053), on every pull, until the
// primary itself was fixed. applyPluginSettings now dedupes defensively
// (dedupeGlobalPluginSettings) before ever inserting: the loser is dropped,
// the winner applies, and the rest of the bundle (catalog, users, tax
// codes, …) is unaffected. The raw index itself still rejects a genuine
// duplicate INSERT at the DB level — see
// TestUxPluginSettingsGlobalRejectsDuplicateRow in internal/db.
func TestAdminSyncSharedPluginSettingsDedupesDuplicateGlobalRowsInBundle(t *testing.T) {
	ctx := context.Background()
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, replica, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES ('com.ut.dup', '1.0.0', 'com.ut.dup', 'wasm', 'plugin.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`)
	mustExec(t, replica, `INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES ('com.ut.dup', 'com.ut.dup', '1.0.0', 'plugin.wasm', 'wasm')`)

	// Two rows for the same key, plus an unrelated key that must survive
	// untouched — the dedupe must be scoped to the actual (plugin_id, key)
	// collision, not the whole plugin. The older row (dup-old) is the
	// tiebreak-losing candidate on BOTH signals (older updated_at AND a
	// lexicographically-smaller id), AND it's listed SECOND (after the
	// winner) — a degenerate "last row in wins" implementation (ignoring
	// updated_at/id entirely) would still pick dup-old here and fail this
	// assertion; only TestAdminSyncSharedPluginSettingsDedupeTiebreaksOnIDWhenUpdatedAtTies's
	// winner-listed-last ordering would let that degenerate implementation
	// slip through — the two tests together pin the real direction either
	// way the rows happen to arrive in a bundle.
	bundle := AdminBundle{Tables: map[string][]map[string]any{
		"plugin_settings": {
			{"id": "dup-new", "plugin_id": "com.ut.dup", "key": "k", "value_json": `"configured"`, "scope": "global", "updated_at": "2026-08-12T00:00:00Z"},
			{"id": "dup-old", "plugin_id": "com.ut.dup", "key": "k", "value_json": `"stale"`, "scope": "global", "updated_at": "2026-08-10T00:00:00Z"},
			{"id": "solo", "plugin_id": "com.ut.dup", "key": "other", "value_json": `"untouched"`, "scope": "global", "updated_at": "2026-08-01T00:00:00Z"},
		},
	}}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var v string
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.ut.dup' AND key = 'k'`).Scan(&v); err != nil {
		t.Fatalf("query surviving row: %v", err)
	}
	if v != `"configured"` {
		t.Fatalf("surviving value = %q, want the newer row's %q", v, `"configured"`)
	}
	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.ut.dup' AND key = 'k'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows for the colliding key = %d, want exactly 1 (loser must be dropped, not both kept)", n)
	}
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE id = 'solo'`).Scan(&v); err != nil || v != `"untouched"` {
		t.Fatalf("unrelated key was affected by the dedupe: %q %v", v, err)
	}
}

// A tie on updated_at (the bundle rows collided at second-resolution, same
// as migration 052's own comment anticipates) must still pick a single,
// deterministic winner — id DESC, same tiebreak 052 uses — not silently
// keep both or panic.
func TestAdminSyncSharedPluginSettingsDedupeTiebreaksOnIDWhenUpdatedAtTies(t *testing.T) {
	ctx := context.Background()
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, replica, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES ('com.ut.tie', '1.0.0', 'com.ut.tie', 'wasm', 'plugin.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`)
	mustExec(t, replica, `INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES ('com.ut.tie', 'com.ut.tie', '1.0.0', 'plugin.wasm', 'wasm')`)

	bundle := AdminBundle{Tables: map[string][]map[string]any{
		"plugin_settings": {
			{"id": "a-loses", "plugin_id": "com.ut.tie", "key": "k", "value_json": `"a"`, "scope": "global", "updated_at": "2026-08-12T00:00:00Z"},
			{"id": "z-wins", "plugin_id": "com.ut.tie", "key": "k", "value_json": `"z"`, "scope": "global", "updated_at": "2026-08-12T00:00:00Z"},
		},
	}}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var id string
	if err := replica.QueryRow(`SELECT id FROM plugin_settings WHERE plugin_id = 'com.ut.tie' AND key = 'k'`).Scan(&id); err != nil {
		t.Fatalf("query surviving row: %v", err)
	}
	if id != "z-wins" {
		t.Fatalf("surviving id = %q, want %q (id DESC tiebreak)", id, "z-wins")
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

// ut-docs#1546, reported from the pilot pair on 2026-09-04: "I added a table
// in the main till but it didn't sync with the secondary (pi)."
//
// The floor plan and kitchen routing are shop-wide setup, not per-till state,
// and were simply absent from adminTables — so a satellite could not see the
// tables at all, could therefore neither take nor settle a table order, and
// sent kitchen tickets nowhere. This is a coverage test as much as a
// regression test: the failure mode is a table being forgotten, so it asserts
// the data actually lands rather than that some code path ran.
func TestAdminDumpApplyRoundTrip_FloorPlanAndKitchenRouting(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO categories (id, name) VALUES ('cat-hot', 'Hot Drinks')`)
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm-esp', 'ESP', 'Espresso', 250)`)
	mustExec(t, primary, `INSERT INTO tables (id, label, area_zone, seat_count, created_at, updated_at)
		VALUES ('tbl-1', 'Table 1', 'Terrace', 4, '2026-09-04', '2026-09-04')`)
	mustExec(t, primary, `INSERT INTO kitchen_stations (id, name, destination_type, created_at, updated_at)
		VALUES ('stn-bar', 'Bar', 'printer', '2026-09-04', '2026-09-04')`)
	mustExec(t, primary, `INSERT INTO item_station_routes (item_id, station_id) VALUES ('itm-esp', 'stn-bar')`)
	mustExec(t, primary, `INSERT INTO category_station_routes (category_id, station_id) VALUES ('cat-hot', 'stn-bar')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var label, zone string
	var seats int
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT label, area_zone, seat_count FROM tables WHERE id = 'tbl-1'`).Scan(&label, &zone, &seats); err != nil {
		t.Fatalf("the floor plan did not reach the satellite — it cannot take a table order at all: %v", err)
	}
	if label != "Table 1" || zone != "Terrace" || seats != 4 {
		t.Errorf("table synced with wrong content: %q / %q / %d", label, zone, seats)
	}

	var station string
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT name FROM kitchen_stations WHERE id = 'stn-bar'`).Scan(&station); err != nil {
		t.Fatalf("kitchen stations did not sync — tickets from the satellite go nowhere: %v", err)
	}

	for _, q := range []struct{ what, sql string }{
		{"item→station route", `SELECT COUNT(*) FROM item_station_routes WHERE item_id='itm-esp' AND station_id='stn-bar'`},
		{"category→station route", `SELECT COUNT(*) FROM category_station_routes WHERE category_id='cat-hot' AND station_id='stn-bar'`},
	} {
		var n int
		if err := replica.DB.QueryRowContext(ctx, q.sql).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s did not sync (n=%d, err=%v) — the satellite would print to the wrong station or none", q.what, n, err)
		}
	}

	// And a deletion on the primary must propagate, not leave a ghost table
	// an operator can still seat customers at.
	mustExec(t, primary, `DELETE FROM tables WHERE id = 'tbl-1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle2); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var n int
	if err := replica.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tables WHERE id='tbl-1'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a table removed on the primary is still on the satellite (n=%d, err=%v)", n, err)
	}
}

// ut-docs#1546 review: a kitchen station's name and routing are shop-wide, but
// its printer_address is till-local — the field takes a network address OR a
// device path, and a satellite inheriting the primary's "/dev/usb/lp0" prints
// to whatever is plugged into its own first USB port, or nowhere.
func TestAdminDumpApplyRoundTrip_KitchenStationPrinterAddressStaysLocal(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO kitchen_stations (id, name, destination_type, printer_address, created_at, updated_at)
		VALUES ('stn-kitchen', 'Kitchen', 'printer', '/dev/usb/lp0', '2026-09-04', '2026-09-04')`)
	mustExec(t, replica, `INSERT INTO kitchen_stations (id, name, destination_type, printer_address, created_at, updated_at)
		VALUES ('stn-kitchen', 'Old name', 'printer', '192.168.1.50:9100', '2026-09-04', '2026-09-04')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var name, addr string
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT name, printer_address FROM kitchen_stations WHERE id = 'stn-kitchen'`).Scan(&name, &addr); err != nil {
		t.Fatalf("station missing after apply: %v", err)
	}
	if name != "Kitchen" {
		t.Errorf("the station's shop-wide name should follow the primary, got %q", name)
	}
	if addr != "192.168.1.50:9100" {
		t.Errorf("printer_address must stay the satellite's own — it inherited %q from the primary, so its kitchen tickets go to the wrong device or nowhere", addr)
	}
}

// ut-docs#1554: role_permissions is the security-relevant half of that
// card — a manager revoking a grant on the primary must actually reach
// every satellite, not just a newly-granted permission. Both DBs already
// carry the identical migration-seeded roles/permission_actions/
// role_permissions rows (that's the fragility #1554 called out: they only
// match "because every till seeds them identically"), so this test proves
// the two properties that seeding alone can never cover: a NEW grant made
// on the primary reaches the satellite, and a REVOKED grant does too.
func TestAdminDumpApplyRoundTrip_RolePermissions(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// 'cashier' isn't seeded with 'audit' by migration 001_init.sql — grant
	// it on the primary, as the permission matrix editor's
	// AuthRepo.SetRolePermission would.
	mustExec(t, primary, `INSERT INTO role_permissions (role, action, granted) VALUES ('cashier', 'audit', 1)
		ON CONFLICT (role, action) DO UPDATE SET granted = excluded.granted`)
	// A seeded grant, revoked on the primary — this is the actual bug
	// #1554 fixed: before this change, nothing propagated this to a
	// satellite at all.
	mustExec(t, primary, `UPDATE role_permissions SET granted = 0 WHERE role = 'admin' AND action = 'refund'`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	// wireTrip: role_permissions.granted is the one INTEGER column this test
	// asserts on, so it must cross the same JSON hop a real replica sees
	// (numbers becoming float64) — every other ApplyAdmin call in this file
	// does the same (see wireTrip's own doc comment above).
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var granted int
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT granted FROM role_permissions WHERE role = 'cashier' AND action = 'audit'`).Scan(&granted); err != nil {
		t.Fatalf("new grant did not sync to the satellite: %v", err)
	}
	if granted != 1 {
		t.Errorf("cashier/audit should be granted on the satellite, got granted=%d", granted)
	}

	if err := replica.DB.QueryRowContext(ctx,
		`SELECT granted FROM role_permissions WHERE role = 'admin' AND action = 'refund'`).Scan(&granted); err != nil {
		t.Fatalf("admin/refund row missing on satellite after apply: %v", err)
	}
	if granted != 0 {
		t.Errorf("admin/refund was revoked on the primary but the satellite still grants it (granted=%d) — this is the exact security gap #1554 fixed", granted)
	}
}

// ut-docs#1589 (found in #1554's own review): a replica one release ahead
// of its primary has extra migration-seeded roles/permission_actions/
// role_permissions rows the primary's dump doesn't mention at all — before
// this fix, deleteMissing's generic prune phase silently deleted them on
// every pull, with no warning. Simulates that skew directly: the replica
// gets a role, an action and a grant the primary has never heard of, and
// they must all survive an ApplyAdmin pull unpruned.
func TestAdminDumpApplyRoundTrip_RolePermissions_SurvivesReplicaAheadOfPrimarySkew(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// The replica is running a migration the primary hasn't reached yet: a
	// new role, a new permission action, and a grant tying them together —
	// none of which exist on the primary, so none appear in its dump.
	mustExec(t, replica, `INSERT INTO roles (role) VALUES ('shift_lead')`)
	mustExec(t, replica, `INSERT INTO permission_actions (action) VALUES ('inventory_count')`)
	mustExec(t, replica, `INSERT INTO role_permissions (role, action, granted) VALUES ('shift_lead', 'refund', 1)`)
	mustExec(t, replica, `INSERT INTO role_permissions (role, action, granted) VALUES ('cashier', 'inventory_count', 1)`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var count int
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM roles WHERE role = 'shift_lead'`).Scan(&count); err != nil {
		t.Fatalf("query roles: %v", err)
	}
	if count != 1 {
		t.Errorf("replica's own newer-migration role 'shift_lead' was pruned by a sync pull from an older primary (count=%d) — version-skew data loss", count)
	}

	if err := replica.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM permission_actions WHERE action = 'inventory_count'`).Scan(&count); err != nil {
		t.Fatalf("query permission_actions: %v", err)
	}
	if count != 1 {
		t.Errorf("replica's own newer-migration action 'inventory_count' was pruned by a sync pull from an older primary (count=%d) — version-skew data loss", count)
	}

	var granted int
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT granted FROM role_permissions WHERE role = 'shift_lead' AND action = 'refund'`).Scan(&granted); err != nil {
		t.Errorf("shift_lead/refund grant was pruned by a sync pull from an older primary that has never heard of 'shift_lead': %v", err)
	} else if granted != 1 {
		t.Errorf("shift_lead/refund should still be granted, got granted=%d", granted)
	}

	if err := replica.DB.QueryRowContext(ctx,
		`SELECT granted FROM role_permissions WHERE role = 'cashier' AND action = 'inventory_count'`).Scan(&granted); err != nil {
		t.Errorf("cashier/inventory_count grant was pruned by a sync pull from an older primary that has never heard of 'inventory_count': %v", err)
	} else if granted != 1 {
		t.Errorf("cashier/inventory_count should still be granted, got granted=%d", granted)
	}
}

// ut-docs#1589 review finding: the skew fix above must NOT blanket-exempt
// role_permissions the way roles/permission_actions are exempted, or it
// silently reopens #1554's own bug in the opposite (over-permissive)
// direction — a grant a satellite fabricated locally, for a role and
// action the primary already knows about perfectly well, would then
// survive every sync pull forever instead of being healed back to the
// primary's (absent) state. Both DBs here are on the IDENTICAL migration —
// no skew at all — so 'cashier' and 'audit' are known to both sides; only
// the grant row itself is satellite-local.
func TestAdminDumpApplyRoundTrip_RolePermissions_PrunesSameVersionLocalDrift(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// A satellite-local edit (or a bug) grants 'cashier'/'audit' on the
	// replica only — the primary has no opinion on this pairing at all.
	mustExec(t, replica, `INSERT INTO role_permissions (role, action, granted) VALUES ('cashier', 'audit', 1)`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var count int
	if err := replica.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM role_permissions WHERE role = 'cashier' AND action = 'audit'`).Scan(&count); err != nil {
		t.Fatalf("query role_permissions: %v", err)
	}
	if count != 0 {
		t.Errorf("a satellite-local grant for a role/action the primary already knows about must still be pruned (primary wins) — got count=%d, ut-docs#1589's skew fix must not survive this case", count)
	}
}

// A table hard-removed on the primary while the SATELLITE still has a local
// sale referencing it (found in review, ut-docs#1546) — the plain DELETE
// above can never hit this path because tbl-1 has no referencing rows
// anywhere. Here the satellite's own history makes the delete FK-blocked
// locally, exactly the "sales history" case every other adminTable's
// hasIsActive fallback already exists for; tables must retire in place
// (enabled=0) the same way, not silently keep a full ghost row an operator
// could still seat customers at.
func TestAdminApply_TableRetiredInPlaceWhenFKBlockedBySatelliteSaleHistory(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO tables (id, label, area_zone, seat_count, created_at, updated_at)
		VALUES ('tbl-1', 'Table 1', 'Terrace', 4, '2026-09-04', '2026-09-04')`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The satellite took an order at this table before the primary removed it.
	mustExec(t, replica, `INSERT INTO sales (id, receipt_no, subtotal, total, table_id)
		VALUES ('sale-1', 'R-0001', 500, 500, 'tbl-1')`)

	mustExec(t, primary, `DELETE FROM tables WHERE id = 'tbl-1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle2); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var enabled int
	err = replica.DB.QueryRowContext(ctx, `SELECT enabled FROM tables WHERE id='tbl-1'`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the table row is gone entirely — the satellite's own sale (sale-1) now references a nonexistent table")
	}
	if err != nil {
		t.Fatalf("query retired table: %v", err)
	}
	if enabled != 0 {
		t.Errorf("a table the primary removed, but the satellite has sale history against, must retire in place (enabled=0) — got enabled=%d, an operator could still seat customers at it", enabled)
	}
}

// ut-docs#1590: registers and stock_locations flipped from excluded
// (ut-docs#1584) to synced now that /registers and /locations gate
// create/rename/activate to primary-only (registers_page.go/
// locations_page.go's requirePrimary — see adminTables' own top comment
// for the full trace). This is the direct replacement for the old
// TestAdminApplyLeavesRegistersAndStockLocationsUntouched, which pinned
// the OLD (excluded) behaviour this same card intentionally reverses.
// Mirrors TestAdminDumpApplyRoundTrip_FloorPlanAndKitchenRouting's shape:
// both tables now dump, both sync to a replica that never heard of them,
// and a primary-side delete propagates (no FK-blocking history here).
func TestAdminDumpApplyRoundTrip_RegistersAndStockLocations(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO stock_locations (id, name, is_active) VALUES ('loc-hq', 'HQ Store', 1)`)
	mustExec(t, primary, `INSERT INTO registers (id, name, location_id, is_active) VALUES ('reg-front', 'Front Till', 'loc-hq', 1)`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, ok := bundle.Tables["stock_locations"]; !ok {
		t.Fatal("stock_locations must appear in the admin dump now — ut-docs#1590 gated creation primary-only, making it safe to sync")
	}
	if _, ok := bundle.Tables["registers"]; !ok {
		t.Fatal("registers must appear in the admin dump now — ut-docs#1590 gated creation primary-only, making it safe to sync")
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var locName string
	if err := replica.QueryRow(`SELECT name FROM stock_locations WHERE id = 'loc-hq'`).Scan(&locName); err != nil || locName != "HQ Store" {
		t.Fatalf("stock location did not reach the satellite: got %q (err=%v)", locName, err)
	}
	var regName, regLoc string
	if err := replica.QueryRow(`SELECT name, location_id FROM registers WHERE id = 'reg-front'`).Scan(&regName, &regLoc); err != nil || regName != "Front Till" || regLoc != "loc-hq" {
		t.Fatalf("register did not reach the satellite (or its FK to stock_locations broke — insert order matters): name=%q location_id=%q err=%v", regName, regLoc, err)
	}

	// A primary-side delete must propagate, not leave a ghost register/
	// location an operator could still pick from the till's own settings.
	mustExec(t, primary, `DELETE FROM registers WHERE id = 'reg-front'`)
	mustExec(t, primary, `DELETE FROM stock_locations WHERE id = 'loc-hq'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM registers WHERE id='reg-front'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a register removed on the primary is still on the satellite (n=%d, err=%v)", n, err)
	}
	if err := replica.QueryRow(`SELECT COUNT(*) FROM stock_locations WHERE id='loc-hq'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a stock location removed on the primary is still on the satellite (n=%d, err=%v)", n, err)
	}
}

// Mirrors TestAdminApply_TableRetiredInPlaceWhenFKBlockedBySatelliteSaleHistory:
// a register the primary removed, but that the satellite has already opened
// a shift against, can't be hard-deleted (shifts.register_id FK) — it must
// retire in place (is_active=0) instead, or the satellite's own shift now
// references a nonexistent register.
func TestAdminApply_RegisterRetiredInPlaceWhenFKBlockedBySatelliteShiftHistory(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO registers (id, name, is_active) VALUES ('reg-1', 'Front Till', 1)`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The satellite opened a shift on this register before the primary
	// removed it.
	mustExec(t, replica, `INSERT INTO users (id, username, display_name) VALUES ('u1', 'cashier1', 'Cashier One')`)
	mustExec(t, replica, `INSERT INTO shifts (id, register_id, cashier_id) VALUES ('shift-1', 'reg-1', 'u1')`)

	mustExec(t, primary, `DELETE FROM registers WHERE id = 'reg-1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	logging.ResetRecent()
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle2); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var active int
	err = replica.QueryRow(`SELECT is_active FROM registers WHERE id='reg-1'`).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the register row is gone entirely — the satellite's own shift (shift-1) now references a nonexistent register")
	}
	if err != nil {
		t.Fatalf("query retired register: %v", err)
	}
	if active != 0 {
		t.Errorf("a register the primary removed, but the satellite has shift history against, must retire in place (is_active=0) — got is_active=%d", active)
	}

	// ut-docs#1610: the retire also mangles registers.name to "Front
	// Till~reg-1" to free the UNIQUE constraint. The /registers management
	// page (ListRegistersForAdmin lists inactive registers too) must show the
	// real name, never the raw mangled value.
	admin, err := NewPOSRepo(replica.DB).ListRegistersForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListRegistersForAdmin: %v", err)
	}
	found := false
	for _, reg := range admin {
		if reg.ID == "reg-1" {
			found = true
			if reg.Name != "Front Till" {
				t.Errorf("ListRegistersForAdmin name for retired register = %q, want %q", reg.Name, "Front Till")
			}
			if reg.IsActive {
				t.Errorf("ListRegistersForAdmin IsActive for retired register = true, want false")
			}
		}
	}
	if !found {
		t.Errorf("ListRegistersForAdmin must still list the retired register; got %+v", admin)
	}

	// ut-docs#1592: retiring a pre-existing satellite-local register must
	// warn, naming the table, row and action, so a shop owner can connect a
	// "my till lost its register" report to this one-time reconciliation.
	if !warnfContaining("pruned pre-existing satellite-local registers row") || !warnfContaining("retired in place") {
		t.Errorf("expected a Warnf naming registers + retired-in-place for reg-1, got: %+v", logging.Recent())
	}
}

// Same fallback, for stock_locations: inventory.location_id FK-blocks the
// hard delete, same shape as the register/shift case above.
func TestAdminApply_StockLocationRetiredInPlaceWhenFKBlockedBySatelliteInventoryHistory(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO stock_locations (id, name, is_active) VALUES ('loc-1', 'Back Room', 1)`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The satellite recorded inventory at this location before the primary
	// removed it.
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm-1', 'SKU-1', 'Widget', 100)`)
	mustExec(t, replica, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1', 'itm-1', 'loc-1', 5)`)

	mustExec(t, primary, `DELETE FROM stock_locations WHERE id = 'loc-1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	logging.ResetRecent()
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle2); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var active int
	err = replica.QueryRow(`SELECT is_active FROM stock_locations WHERE id='loc-1'`).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the stock location row is gone entirely — the satellite's own inventory row (inv-1) now references a nonexistent location")
	}
	if err != nil {
		t.Fatalf("query retired stock location: %v", err)
	}
	if active != 0 {
		t.Errorf("a stock location the primary removed, but the satellite has inventory against, must retire in place (is_active=0) — got is_active=%d", active)
	}

	// ut-docs#1610: the retire also mangles stock_locations.name to "Back
	// Room~loc-1". Both readers that surface a location's name to staff —
	// the pickers (ListStockLocations, no active filter) and the /locations
	// management page (ListStockLocationsForAdmin) — must show the real name.
	pos := NewPOSRepo(replica.DB)
	picker, err := pos.ListStockLocations(ctx)
	if err != nil {
		t.Fatalf("ListStockLocations: %v", err)
	}
	found := false
	for _, l := range picker {
		if l.ID == "loc-1" {
			found = true
			if l.Name != "Back Room" {
				t.Errorf("ListStockLocations name for retired location = %q, want %q", l.Name, "Back Room")
			}
		}
	}
	if !found {
		t.Errorf("ListStockLocations must still list the retired location (it has no active filter); got %+v", picker)
	}
	admin, err := pos.ListStockLocationsForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListStockLocationsForAdmin: %v", err)
	}
	found = false
	for _, l := range admin {
		if l.ID == "loc-1" {
			found = true
			if l.Name != "Back Room" {
				t.Errorf("ListStockLocationsForAdmin name for retired location = %q, want %q", l.Name, "Back Room")
			}
			if l.IsActive {
				t.Errorf("ListStockLocationsForAdmin IsActive for retired location = true, want false")
			}
		}
	}
	if !found {
		t.Errorf("ListStockLocationsForAdmin must still list the retired location; got %+v", admin)
	}

	// ut-docs#1592: same warning requirement as the register case above.
	if !warnfContaining("pruned pre-existing satellite-local stock_locations row") || !warnfContaining("retired in place") {
		t.Errorf("expected a Warnf naming stock_locations + retired-in-place for loc-1, got: %+v", logging.Recent())
	}
}

// ut-docs#1592: the hard-delete path (no FK history at all) must warn too,
// not just the retire-in-place fallback above — a register/stock location
// that's satellite-local AND has never had a shift/inventory row against it
// is hard-deleted outright, and that's exactly the "predates ut-docs#1590"
// case this card exists to surface.
func TestAdminApply_RegisterHardDeletedPreExistingLogsWarning(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	// Empty bundle from the primary (it never knew about this register) is
	// enough to trigger the prune — no primary-side insert/delete needed.
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	// Simulates a register created directly on a satellite before
	// ut-docs#1590 gated /registers to primary-only, with no shift history
	// against it yet.
	mustExec(t, replica, `INSERT INTO registers (id, name, is_active) VALUES ('reg-orphan', 'Satellite Local', 1)`)

	logging.ResetRecent()
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM registers WHERE id='reg-orphan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected reg-orphan to be hard-deleted (no history to FK-block it): n=%d err=%v", n, err)
	}
	if !warnfContaining("pruned pre-existing satellite-local registers row") || !warnfContaining("hard-deleted") {
		t.Errorf("expected a Warnf naming registers + hard-deleted for reg-orphan, got: %+v", logging.Recent())
	}
}

// Mirrors TestAdminApply_RegisterHardDeletedPreExistingLogsWarning for
// stock_locations.
func TestAdminApply_StockLocationHardDeletedPreExistingLogsWarning(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	mustExec(t, replica, `INSERT INTO stock_locations (id, name, is_active) VALUES ('loc-orphan', 'Satellite Local', 1)`)

	logging.ResetRecent()
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM stock_locations WHERE id='loc-orphan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected loc-orphan to be hard-deleted (no history to FK-block it): n=%d err=%v", n, err)
	}
	if !warnfContaining("pruned pre-existing satellite-local stock_locations row") || !warnfContaining("hard-deleted") {
		t.Errorf("expected a Warnf naming stock_locations + hard-deleted for loc-orphan, got: %+v", logging.Recent())
	}
}

// ut-docs#1592: the new Warnf must be scoped to registers/stock_locations
// ONLY — every other adminTable prunes routinely, and logging every one of
// those would just be noise (and would defeat the purpose: a shop owner
// could no longer tell "routine sync" apart from "pre-existing divergence
// worth a look"). tax_codes is a plain hasIsActive table with no special
// gating, so a satellite-local row hits the exact same retire-in-place code
// path as the register/stock_location tests above — it must NOT warn.
func TestAdminApply_OrdinaryTablePruneDoesNotLogSatelliteDivergenceWarning(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	mustExec(t, replica, `INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES ('tax-orphan', 'Local Rate', 0, 1)`)

	logging.ResetRecent()
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE id='tax-orphan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected tax-orphan to be hard-deleted: n=%d err=%v", n, err)
	}
	if warnfContaining("pruned pre-existing satellite-local") {
		t.Errorf("tax_codes is not registers/stock_locations — must not fire the satellite-divergence Warnf, got: %+v", logging.Recent())
	}
}

// ut-docs#1670: plugin_storage is generic plugin-private KV storage, but
// its fiscal_register: prefix backs the shop-wide German TSE till-register
// (ADR-0072/ut-docs#1106) and must sync primary->satellite. The whole
// TABLE must NOT sync -- it holds every other installed plugin's private
// state too -- so this proves all three things at once: the scoped
// (plugin_id AND prefix) row travels, an unrelated plugin's row under a
// DIFFERENT key is never even inspected, and — the review finding this
// test was extended to cover — a DIFFERENT plugin's row that happens to
// share the exact same fiscal_register: prefix is ALSO never touched
// (prefix alone is not the namespace boundary; plugin_id is).
// The real fiscal_register: row is seeded via PluginRepo.StorageSet's
// real []byte write path (not a SQL string literal) so plugin_storage.value
// genuinely has SQLite storage class BLOB here, the same as production —
// a literal string INSERT stores class TEXT instead and would let a
// BLOB-vs-TEXT regression in scanGenericCols' []byte->string conversion
// slip past this test undetected.
func TestAdminDumpApplyRoundTrip_FiscalRegisterStorage(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	if err := NewPluginRepo(primary.DB).StorageSet(ctx, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1", []byte(`{"id":"entry-1","eas_serial":"eas-1"}`)); err != nil {
		t.Fatalf("seed fiscal_register entry via real write path: %v", err)
	}
	// An unrelated plugin's own private state under a DIFFERENT key,
	// present on BOTH sides before the apply -- if this table ever synced
	// whole, the replica's own copy would be pruned as "missing from the
	// primary's dump" (the primary only has a fiscal_register: row, not
	// this key).
	mustExec(t, primary, `INSERT INTO plugin_storage (plugin_id, key, value) VALUES ('com.example.other', 'other:setting', 'primary-value')`)
	mustExec(t, replica, `INSERT INTO plugin_storage (plugin_id, key, value) VALUES ('com.example.other', 'other:setting', 'replica-local-value')`)
	// A DIFFERENT plugin's row that happens to share the exact SAME
	// fiscal_register: prefix -- the actual gap a prefix-only scope would
	// have left open (found in review): its own genuinely private state
	// must never be deleted, and must never be broadcast in the dump.
	mustExec(t, primary, `INSERT INTO plugin_storage (plugin_id, key, value) VALUES ('com.example.other', 'fiscal_register:entry-1', 'not-the-real-tax-de-entry')`)
	mustExec(t, replica, `INSERT INTO plugin_storage (plugin_id, key, value) VALUES ('com.example.other', 'fiscal_register:entry-1', 'replica-local-collision-value')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	recs, ok := bundle.Tables["plugin_storage"]
	if !ok {
		t.Fatal("plugin_storage must appear in the admin dump now — ut-docs#1670")
	}
	for _, rec := range recs {
		if fmt.Sprint(rec["plugin_id"]) != FiscalRegisterDEPluginID {
			t.Fatalf("dump leaked a plugin_storage row belonging to a different plugin: %+v", rec)
		}
		if !strings.HasPrefix(fmt.Sprint(rec["key"]), FiscalRegisterDEKeyPrefix) {
			t.Fatalf("dump leaked a non-fiscal_register plugin_storage row: %+v", rec)
		}
	}
	if len(recs) != 1 {
		t.Fatalf("dump should carry exactly the one real tax-de entry, got %d: %+v", len(recs), recs)
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var value string
	if err := replica.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id = ? AND key = ?`, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1").Scan(&value); err != nil {
		t.Fatalf("fiscal_register entry did not reach the satellite: %v", err)
	}
	if !strings.Contains(value, "eas-1") {
		t.Fatalf("fiscal_register entry value mismatch: %q", value)
	}

	// The unrelated plugin's row (different key) must be completely
	// untouched -- still its OWN replica-local value, not overwritten, not
	// deleted.
	var otherValue string
	if err := replica.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id = 'com.example.other' AND key = 'other:setting'`).Scan(&otherValue); err != nil {
		t.Fatalf("unrelated plugin_storage row was removed by a supposedly-scoped sync: %v", err)
	}
	if otherValue != "replica-local-value" {
		t.Fatalf("unrelated plugin_storage row was overwritten by a supposedly-scoped sync: got %q", otherValue)
	}

	// The colliding-prefix row from a DIFFERENT plugin must ALSO be
	// completely untouched -- this is the actual N1 review finding: prefix
	// alone is not the namespace boundary.
	var collisionValue string
	if err := replica.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id = 'com.example.other' AND key = ?`, FiscalRegisterDEKeyPrefix+"entry-1").Scan(&collisionValue); err != nil {
		t.Fatalf("a different plugin's same-prefix row was removed by the sync — plugin_id scoping regressed: %v", err)
	}
	if collisionValue != "replica-local-collision-value" {
		t.Fatalf("a different plugin's same-prefix row was overwritten by the sync — plugin_id scoping regressed: got %q", collisionValue)
	}

	// A primary-side delete of the fiscal_register: row must propagate
	// (delete-then-insert semantics, same as applyPluginSettings) -- a
	// decommissioned-and-removed entry must not remain a ghost on a
	// satellite forever.
	mustExec(t, primary, `DELETE FROM plugin_storage WHERE plugin_id = ? AND key = ?`, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1")
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ? AND key = ?`, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1").Scan(&n); err != nil || n != 0 {
		t.Errorf("a fiscal_register entry removed on the primary is still on the satellite (n=%d, err=%v)", n, err)
	}
	// The colliding-prefix row must survive the delete-then-insert too —
	// an empty scoped recs set must still never touch a different plugin_id.
	if err := replica.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id = 'com.example.other' AND key = ?`, FiscalRegisterDEKeyPrefix+"entry-1").Scan(&collisionValue); err != nil {
		t.Fatalf("a different plugin's same-prefix row was removed by the second (empty-recs) scoped apply: %v", err)
	}
	if collisionValue != "replica-local-collision-value" {
		t.Fatalf("a different plugin's same-prefix row was overwritten by the second scoped apply: got %q", collisionValue)
	}
	// The unrelated row must STILL be untouched after the second apply too.
	if err := replica.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id = 'com.example.other' AND key = 'other:setting'`).Scan(&otherValue); err != nil {
		t.Fatalf("unrelated plugin_storage row was removed by the second scoped apply: %v", err)
	}
	if otherValue != "replica-local-value" {
		t.Fatalf("unrelated plugin_storage row was overwritten by the second scoped apply: got %q", otherValue)
	}
}
