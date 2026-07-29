package data

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// newPluginLifecycleTestDB opens a DB via the real migrations (plugins has
// a composite FK to plugin_catalog(id, version), so a hand-rolled schema
// would need to reproduce that anyway) and clears any migration-seeded
// plugin rows for a clean slate.
func newPluginLifecycleTestDB(t *testing.T) (*db.DB, *PluginRepo) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "plugin_lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	for _, tbl := range []string{"plugin_permissions", "plugin_hooks", "plugin_settings", "plugin_entries", "plugins", "plugin_catalog"} {
		if _, err := d.DB.ExecContext(ctx, `DELETE FROM `+tbl); err != nil {
			t.Fatalf("clear seeded %s: %v", tbl, err)
		}
	}
	return d, NewPluginRepo(d.DB)
}

func seedCatalogEntry(t *testing.T, d *db.DB, id, version string) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugin_catalog
(id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at, is_deprecated)
VALUES (?, ?, 'Test Plugin', 'desc', 'author', 'site', 'wasm', 'entry', 'https://example.com/pkg', 'deadbeef', '0.1.0', '1', datetime('now'), 0)`,
		id, version); err != nil {
		t.Fatalf("seed plugin_catalog %s@%s: %v", id, version, err)
	}
}

func TestInstallPlugin_FromCatalogAndReinstallUpgrade(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.faq", "1.0.0")

	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}
	info, ok, err := repo.GetPlugin(ctx, "com.example.faq", "")
	if err != nil || !ok {
		t.Fatalf("expected the plugin installed, got ok=%v err=%v", ok, err)
	}
	if info.Version != "1.0.0" || !info.IsActive || info.InstallState != "installed" {
		t.Fatalf("unexpected plugin row: %+v", info)
	}

	// Re-installing the SAME catalog entry must update in place (ON
	// CONFLICT), not error or duplicate — idempotent retry of an install.
	// NOTE: InstallPlugin's SELECT ... FROM plugin_catalog WHERE id = ? has
	// no ORDER BY, so if the catalog ever held MULTIPLE rows for the same
	// id (e.g. historical versions, which plugin_catalog's schema and
	// GetPluginVersionAt's existence suggest it might), which version
	// "wins" would be an unspecified SQLite row-emission detail rather
	// than a guaranteed behavior — not exercised here since InstallPlugin
	// currently has no production caller (the live install path goes
	// through UpsertPluginManifest instead; see cloudsync_wire.go).
	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}
	info, ok, err = repo.GetPlugin(ctx, "com.example.faq", "")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if info.Version != "1.0.0" || !info.IsActive {
		t.Fatalf("expected idempotent reinstall to leave the same version active, got %+v", info)
	}

	// Installing an id with no catalog entry at all silently affects 0 rows
	// (the query is a SELECT ... FROM plugin_catalog with no match) rather
	// than erroring — confirm that's still true so callers relying on
	// GetPlugin to detect "not installed" aren't surprised by a different
	// failure mode.
	if err := repo.InstallPlugin(ctx, nil, "does.not.exist"); err != nil {
		t.Fatalf("expected no error installing an unknown catalog id (no-op), got %v", err)
	}
	if _, ok, err := repo.GetPlugin(ctx, "does.not.exist", ""); err != nil || ok {
		t.Fatalf("expected no plugin row for an unknown catalog id, got ok=%v err=%v", ok, err)
	}
}

func TestGetPlugin_VersionFilterAndNotFound(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.faq", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := repo.GetPlugin(ctx, "com.example.faq", "1.0.0"); err != nil || !ok {
		t.Fatalf("expected a match for the correct version, got ok=%v err=%v", ok, err)
	}
	if _, ok, err := repo.GetPlugin(ctx, "com.example.faq", "9.9.9"); err != nil || ok {
		t.Fatalf("expected no match for a version that was never installed, got ok=%v err=%v", ok, err)
	}
	if _, ok, err := repo.GetPlugin(ctx, "does-not-exist", ""); err != nil || ok {
		t.Fatalf("expected no match for an unknown plugin id, got ok=%v err=%v", ok, err)
	}
}

func TestUpdatePluginVersionAndSetPluginActive(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.faq", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}

	// plugins has a composite FK to plugin_catalog(id, version) -- the
	// catalog entry for the new version must exist before the plugins row
	// can move to it (matches the real upgrade flow: fetch the new catalog
	// entry, then UpdatePluginVersion).
	seedCatalogEntry(t, d, "com.example.faq", "1.1.0")
	if err := repo.UpdatePluginVersion(ctx, nil, "com.example.faq", "1.1.0", "new-entry", "installing"); err != nil {
		t.Fatal(err)
	}
	info, _, err := repo.GetPlugin(ctx, "com.example.faq", "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "1.1.0" || info.Entrypoint != "new-entry" || info.InstallState != "installing" {
		t.Fatalf("update did not apply: %+v", info)
	}

	if err := repo.SetPluginActive(ctx, nil, "com.example.faq", false); err != nil {
		t.Fatal(err)
	}
	if version, ok, err := repo.GetActivePluginVersion(ctx, "com.example.faq"); err != nil || ok {
		t.Fatalf("expected no active version after deactivating, got version=%q ok=%v err=%v", version, ok, err)
	}

	if err := repo.SetPluginActive(ctx, nil, "com.example.faq", true); err != nil {
		t.Fatal(err)
	}
	if version, ok, err := repo.GetActivePluginVersion(ctx, "com.example.faq"); err != nil || !ok || version != "1.1.0" {
		t.Fatalf("expected active version 1.1.0, got version=%q ok=%v err=%v", version, ok, err)
	}
}

func TestSetPluginStateAndListRevokedPlugins(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.faq", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}

	revoked, err := repo.ListRevokedPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 0 {
		t.Fatalf("expected no revoked plugins yet, got %+v", revoked)
	}

	if err := repo.SetPluginState(ctx, "com.example.faq", "1.0.0", "revoked", false); err != nil {
		t.Fatal(err)
	}
	revoked, err = repo.ListRevokedPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0].ID != "com.example.faq" || revoked[0].IsActive {
		t.Fatalf("expected the plugin revoked and inactive, got %+v", revoked)
	}
}

func TestHasActivePrinterPermissionAndCapability(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	if has, err := repo.HasActivePrinterPermission(ctx); err != nil || has {
		t.Fatalf("expected no printer permission yet, got has=%v err=%v", has, err)
	}

	seedCatalogEntry(t, d, "com.example.printer", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.printer"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugin_permissions(id, plugin_id, permission, granted) VALUES('perm1','com.example.printer','devices:printer',1)`); err != nil {
		t.Fatal(err)
	}

	if has, err := repo.HasActivePrinterPermission(ctx); err != nil || !has {
		t.Fatalf("expected a granted printer permission on an active plugin, got has=%v err=%v", has, err)
	}
	if has, err := repo.HasActivePrinterCapability(ctx); err != nil || !has {
		t.Fatalf("expected printer capability (also requires install_state='installed'), got has=%v err=%v", has, err)
	}

	// The install_state gate is what actually distinguishes Capability from
	// Permission — prove it: is_active stays 1, but install_state moves off
	// 'installed' (mid-upgrade). Permission must still see the grant;
	// Capability must not.
	if err := repo.SetPluginState(ctx, "com.example.printer", "1.0.0", "installing", true); err != nil {
		t.Fatal(err)
	}
	if has, err := repo.HasActivePrinterPermission(ctx); err != nil || !has {
		t.Fatalf("expected Permission to ignore install_state and still see the grant, got has=%v err=%v", has, err)
	}
	if has, err := repo.HasActivePrinterCapability(ctx); err != nil || has {
		t.Fatalf("expected Capability to require install_state='installed', got has=%v err=%v", has, err)
	}
	if err := repo.SetPluginState(ctx, "com.example.printer", "1.0.0", "installed", true); err != nil {
		t.Fatal(err)
	}

	// Deactivating the plugin must revoke the EFFECTIVE permission (both
	// methods) even though the plugin_permissions row itself is untouched.
	if err := repo.SetPluginActive(ctx, nil, "com.example.printer", false); err != nil {
		t.Fatal(err)
	}
	if has, err := repo.HasActivePrinterPermission(ctx); err != nil || has {
		t.Fatalf("expected no printer permission once the plugin is inactive, got has=%v err=%v", has, err)
	}
	if has, err := repo.HasActivePrinterCapability(ctx); err != nil || has {
		t.Fatalf("expected no printer capability once the plugin is inactive, got has=%v err=%v", has, err)
	}
}

func TestGetPluginVersionAt(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.faq", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}
	// installed_at/updated_at default to datetime('now') via the schema;
	// InstallPlugin also explicitly sets updated_at to time.Now(). Query
	// "at" a point comfortably after install.
	at := time.Now().Add(time.Hour)
	version, ok, err := repo.GetPluginVersionAt(ctx, "com.example.faq", at)
	if err != nil || !ok || version != "1.0.0" {
		t.Fatalf("expected version 1.0.0 active at %v, got version=%q ok=%v err=%v", at, version, ok, err)
	}

	// A point before the plugin was ever installed: no version yet.
	before := time.Now().Add(-24 * time.Hour)
	if _, ok, err := repo.GetPluginVersionAt(ctx, "com.example.faq", before); err != nil || ok {
		t.Fatalf("expected no version active before install time, got ok=%v err=%v", ok, err)
	}
}

func TestListPluginSettings(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.faq", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.faq"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugin_settings(id, plugin_id, key, value_json, scope) VALUES
('s1','com.example.faq','greeting','"hi"','global'),
('s2','com.example.faq','register_only','"x"','register'),
('s3','com.example.faq','user_scoped','"y"','user')`); err != nil {
		t.Fatal(err)
	}

	settings, err := repo.ListPluginSettings(ctx, "com.example.faq")
	if err != nil {
		t.Fatal(err)
	}
	// user-scoped setting excluded: ListPluginSettings only returns
	// global/register scope (per-till config, not per-operator).
	if len(settings) != 2 {
		t.Fatalf("expected 2 settings (global+register), got %+v", settings)
	}
}

func TestListPluginHookEvents(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugin_hooks(id, plugin_id, event, action, is_active) VALUES
('h1','com.example.tax','tax.rate.ask','tax.rate',1),
('h2','com.example.tax','sale.completed','notify',0)`); err != nil {
		t.Fatal(err)
	}

	events, err := repo.ListPluginHookEvents(ctx, "com.example.tax")
	if err != nil {
		t.Fatal(err)
	}
	// The inactive hook's event must not appear.
	if len(events) != 1 || events[0] != "tax.rate.ask" {
		t.Fatalf("expected only the active hook's event, got %+v", events)
	}
}

func TestCatalogPage(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.a", "1.0.0")
	seedCatalogEntry(t, d, "com.example.b", "1.0.0")
	if _, err := d.DB.ExecContext(ctx, `UPDATE plugin_catalog SET tags_json = '["loyalty"]' WHERE id = 'com.example.a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `UPDATE plugin_catalog SET tags_json = '["payments"]' WHERE id = 'com.example.b'`); err != nil {
		t.Fatal(err)
	}
	// A deprecated entry must never appear on the catalog page.
	seedCatalogEntry(t, d, "com.example.old", "1.0.0")
	if _, err := d.DB.ExecContext(ctx, `UPDATE plugin_catalog SET is_deprecated = 1 WHERE id = 'com.example.old'`); err != nil {
		t.Fatal(err)
	}

	rows, total, err := repo.CatalogPage(ctx, 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("expected 2 non-deprecated entries, got total=%d rows=%+v", total, rows)
	}

	rows, total, err = repo.CatalogPage(ctx, 0, 10, "loyalty")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "com.example.a" {
		t.Fatalf("expected only the loyalty-tagged entry, got total=%d rows=%+v", total, rows)
	}

	// Pagination: limit 1, offset 1 on the unfiltered set returns the
	// second row but total still reflects the full (unpaginated) count.
	rows, total, err = repo.CatalogPage(ctx, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 1 {
		t.Fatalf("expected total=2 with 1 row for this page, got total=%d rows=%+v", total, rows)
	}
}
