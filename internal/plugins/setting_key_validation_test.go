package plugins

import (
	"context"
	"strings"
	"testing"
)

// Install-time guard against ut-docs#808: PersistManifest had no in-manifest
// duplicate-key check for type:"settings" entries at all, unlike the
// equivalent payment/page entry checks (validatePaymentEntryKeys,
// validatePageEntryKeys). Two settings entries sharing a key at the default
// "global" scope both attempt to INSERT during ReconcilePluginSettings — on
// a fresh install there's no existing DB row to reconcile against, so both
// go straight to the insert branch, and the second trips
// ux_plugin_settings_global (migration 053's partial UNIQUE INDEX on
// (plugin_id, key) WHERE scope='global'), surfacing as a raw
// "UNIQUE constraint failed: plugin_settings.plugin_id, plugin_settings.key"
// wrapped only as "insert plugin setting: %w" — not a clean, actionable
// error naming the key, unlike every sibling validator in this file.
//
// register/user-scoped duplicates are a milder, distinct gap: the table-level
// UNIQUE(plugin_id, key, scope, scope_id) never fires for them (scope_id is
// always NULL, and SQLite treats NULLs as distinct), so today both rows
// insert silently with no error at all. validateSettingKeys closes both
// cases the same way, keyed on (key, effective scope) rather than key alone,
// since a manifest declaring the same key at two genuinely different scopes
// is not a conflict — ReconcilePluginSettings already treats moving a key
// between scopes as a normal upgrade path.

func settingsManifest(id string, settings []ManifestSetting) *Manifest {
	return &Manifest{
		ID:         id,
		Name:       "Settings " + id,
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Settings:   settings,
	}
}

func TestPersistManifest_RejectsDuplicateGlobalSettingKey(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := settingsManifest("com.dup.global", []ManifestSetting{
		{Key: "api_key", DefaultValue: "a"},
		{Key: "api_key", DefaultValue: "b"},
	})
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a manifest with two global-scope settings entries sharing a key")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("collision error should name the key, got: %v", err)
	}
	if strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Fatalf("collision must be caught by clean validation, not surface the raw SQLite error: %v", err)
	}
	// The rejected plugin must not be half-installed.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.dup.global'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
}

func TestPersistManifest_RejectsDuplicateScopedSettingKey(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// Explicit non-global scope on both — today this silently double-inserts
	// (table UNIQUE never fires, scope_id is always NULL) rather than
	// erroring; validateSettingKeys closes that gap too.
	m := settingsManifest("com.dup.register", []ManifestSetting{
		{Key: "reader_id", DefaultValue: "a", Scope: "register"},
		{Key: "reader_id", DefaultValue: "b", Scope: "register"},
	})
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a manifest with two register-scope settings entries sharing a key")
	}
	if !strings.Contains(err.Error(), "reader_id") {
		t.Fatalf("collision error should name the key, got: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.dup.register'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugin_settings row(s), want 0", n)
	}
}

func TestPersistManifest_SameKeyDifferentScopesNotAConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// Same key, genuinely different scopes — a legitimate shape
	// (ReconcilePluginSettings already treats moving a key between scopes as
	// a normal upgrade), must not be rejected by the new check.
	m := settingsManifest("com.samekey.scopes", []ManifestSetting{
		{Key: "rate", DefaultValue: "a", Scope: "global"},
		{Key: "rate", DefaultValue: "b", Scope: "register"},
	})
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("same key at two different scopes must install cleanly: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.samekey.scopes' AND key = 'rate'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 distinct rows (one per scope), got %d", n)
	}
}

func TestPersistManifest_DefaultScopeTreatedAsGlobalForDuplicateCheck(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// One entry with an explicit "global" scope, one with Scope left empty —
	// PersistManifest defaults an empty Scope to "global" before insert, so
	// the duplicate check must apply the same default to catch this pair,
	// not compare "global" against "" and miss the collision.
	m := settingsManifest("com.dup.implicit-global", []ManifestSetting{
		{Key: "endpoint", DefaultValue: "a", Scope: "global"},
		{Key: "endpoint", DefaultValue: "b"},
	})
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a manifest with an explicit-global and an implicit-global entry sharing a key")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("collision error should name the key, got: %v", err)
	}
}

func TestPersistManifest_NoDuplicateSettingKeysInstallsCleanly(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// Regression guard: a normal, non-colliding manifest must be unaffected.
	m := settingsManifest("com.clean.settings", []ManifestSetting{
		{Key: "api_key", DefaultValue: "a"},
		{Key: "currency", DefaultValue: "gbp", Scope: "global"},
		{Key: "reader_id", DefaultValue: "", Scope: "register"},
	})
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("distinct settings keys must install cleanly: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.clean.settings'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 settings rows, got %d", n)
	}
}
