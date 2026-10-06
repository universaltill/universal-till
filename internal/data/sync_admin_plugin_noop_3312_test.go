package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#3312: applyPluginSettings and applyFiscalRegisterStorage used a
// scoped delete-then-insert, so every apply fired the DELETE and INSERT
// sync_admin_version triggers for each global plugin setting and each
// fiscal-register row, even for an identical bundle. They now delete only
// the rows the bundle no longer carries and leave the rest to #2875's
// conditional upsert.

func seedPluginNoopPrimary(t *testing.T, primary, replica *db.DB) {
	t.Helper()
	for _, d := range []*db.DB{primary, replica} {
		mustExec(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES ('com.ut.pay', '1.0.0', 'pay', 'wasm', 'plugin.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`)
		mustExec(t, d, `INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES ('com.ut.pay', 'pay', '1.0.0', 'plugin.wasm', 'wasm')`)
	}
	mustExec(t, primary, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('ps1', 'com.ut.pay', 'currency', '"eur"', 'global')`)
	mustExec(t, primary, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('ps2', 'com.ut.pay', 'mode', '"live"', 'global')`)
	if err := NewPluginRepo(primary.DB).StorageSet(context.Background(), FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1", []byte(`{"id":"entry-1"}`)); err != nil {
		t.Fatalf("seed fiscal_register entry-1: %v", err)
	}
	if err := NewPluginRepo(primary.DB).StorageSet(context.Background(), FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-2", []byte(`{"id":"entry-2"}`)); err != nil {
		t.Fatalf("seed fiscal_register entry-2: %v", err)
	}
}

func applyFirst(t *testing.T, rrepo *SyncAdminRepo, bundle AdminBundle) {
	t.Helper()
	ctx := context.Background()
	if _, err := rrepo.ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
}

func TestApplyAdmin_IdenticalPluginBundleMovesNoVersion(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedPluginNoopPrimary(t, primary, replica)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(bundle.Tables["plugin_settings"]) != 2 || len(bundle.Tables["plugin_storage"]) != 2 {
		t.Fatalf("bundle should carry 2 plugin_settings and 2 plugin_storage rows, got %d and %d",
			len(bundle.Tables["plugin_settings"]), len(bundle.Tables["plugin_storage"]))
	}
	rrepo := NewSyncAdminRepo(replica.DB)
	applyFirst(t, rrepo, bundle)
	before := syncAdminGeneration(t, replica)

	if _, err := rrepo.ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := syncAdminGeneration(t, replica); got != before {
		t.Errorf("sync_admin_version %d -> %d on an identical plugin bundle, want unchanged", before, got)
	}
}

// Key deletion and value changes still propagate, for both tables.
func TestApplyAdmin_PluginRowChangesStillApply(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedPluginNoopPrimary(t, primary, replica)

	prepo := NewSyncAdminRepo(primary.DB)
	rrepo := NewSyncAdminRepo(replica.DB)
	bundle, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	applyFirst(t, rrepo, bundle)
	before := syncAdminGeneration(t, replica)

	mustExec(t, primary, `UPDATE plugin_settings SET value_json = '"gbp"' WHERE id = 'ps1'`)
	mustExec(t, primary, `DELETE FROM plugin_settings WHERE id = 'ps2'`)
	mustExec(t, primary, `DELETE FROM plugin_storage WHERE plugin_id = ? AND key = ?`, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-2")
	if err := NewPluginRepo(primary.DB).StorageSet(ctx, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1", []byte(`{"id":"entry-1","v":2}`)); err != nil {
		t.Fatalf("update fiscal_register entry-1: %v", err)
	}
	changed, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump after changes: %v", err)
	}
	if _, err := rrepo.ApplyAdminWithResult(ctx, wireTrip(t, changed)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	// One UPDATE (ps1) + one DELETE (ps2) + one DELETE (entry-2) + one
	// UPDATE (entry-1): exactly four trigger fires, not a blanket rewrite.
	if got := syncAdminGeneration(t, replica); got != before+4 {
		t.Errorf("sync_admin_version %d -> %d after four real plugin row changes, want +4", before, got)
	}
	var v string
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE id = 'ps1'`).Scan(&v); err != nil || v != `"gbp"` {
		t.Errorf("ps1 value_json = %q (err %v), want \"gbp\"", v, err)
	}
	var n int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.ut.pay' AND key = 'mode'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("deleted key 'mode' still on the replica: count %d (err %v)", n, err)
	}
	if err := replica.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ? AND key = ?`, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-2").Scan(&n); err != nil || n != 0 {
		t.Errorf("deleted fiscal_register entry-2 still on the replica: count %d (err %v)", n, err)
	}
	if err := replica.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id = ? AND key = ?`, FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"entry-1").Scan(&v); err != nil || v != `{"id":"entry-1","v":2}` {
		t.Errorf("fiscal_register entry-1 = %q (err %v), want the updated value", v, err)
	}
}

// ut-docs#807 still holds: a local global row with the same (plugin_id, key)
// but a different id must not abort the apply on ux_plugin_settings_global;
// the primary's row replaces it.
func TestApplyAdmin_LocalGlobalPluginSettingWithOtherIDIsReplaced(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedPluginNoopPrimary(t, primary, replica)
	mustExec(t, replica, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('local-1', 'com.ut.pay', 'currency', '"usd"', 'global')`)
	mustExec(t, replica, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id) VALUES ('local-reg', 'com.ut.pay', 'reader', '"tmr_1"', 'register', 'till-r')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply must not abort on a same-key, different-id local global row: %v", err)
	}
	var id, v string
	if err := replica.QueryRow(`SELECT id, value_json FROM plugin_settings WHERE plugin_id = 'com.ut.pay' AND key = 'currency' AND scope = 'global'`).Scan(&id, &v); err != nil || id != "ps1" || v != `"eur"` {
		t.Errorf("currency row = (%q, %q) (err %v), want the primary's (ps1, \"eur\")", id, v, err)
	}
	if err := replica.QueryRow(`SELECT value_json FROM plugin_settings WHERE id = 'local-reg'`).Scan(&v); err != nil || v != `"tmr_1"` {
		t.Errorf("register-scoped row = %q (err %v), want it untouched", v, err)
	}
}

// Two global rows swap keys on the primary: each local row now has an
// (id, key) the bundle doesn't carry, so both are cleared before the id
// upsert, which would otherwise move one row onto the other's key.
func TestApplyAdmin_PluginSettingsSwappedKeysApply(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedPluginNoopPrimary(t, primary, replica)

	prepo := NewSyncAdminRepo(primary.DB)
	rrepo := NewSyncAdminRepo(replica.DB)
	bundle, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	applyFirst(t, rrepo, bundle)

	mustExec(t, primary, `UPDATE plugin_settings SET key = 'tmp' WHERE id = 'ps1'`)
	mustExec(t, primary, `UPDATE plugin_settings SET key = 'currency' WHERE id = 'ps2'`)
	mustExec(t, primary, `UPDATE plugin_settings SET key = 'mode' WHERE id = 'ps1'`)
	swapped, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump after swap: %v", err)
	}
	if _, err := rrepo.ApplyAdminWithResult(ctx, wireTrip(t, swapped)); err != nil {
		t.Fatalf("apply after swap: %v", err)
	}
	for id, key := range map[string]string{"ps1": "mode", "ps2": "currency"} {
		var got string
		if err := replica.QueryRow(`SELECT key FROM plugin_settings WHERE id = ?`, id).Scan(&got); err != nil || got != key {
			t.Errorf("%s key = %q (err %v), want %q", id, got, err, key)
		}
	}
}
