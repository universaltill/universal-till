package data

import (
	"context"
	"testing"
)

func TestPluginRepo_EnsureCatalogEntry_DoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)

	row := CatalogUpsertRow{
		ID: "com.t.cat1", Version: "1.0.0", Name: "Original Name", Runtime: "wasm",
		Entrypoint: "p.wasm", PackageURL: "u1", SHA256: "s1", MinPOSVersion: "0.1.0",
		PublishedAt: "2026-07-30",
	}
	if err := repo.EnsureCatalogEntry(ctx, nil, row); err != nil {
		t.Fatalf("first ensure: %v", err)
	}

	// A second Ensure call with different metadata for the SAME (id,version)
	// must be a no-op (ON CONFLICT DO NOTHING) -- existing catalog rows
	// (e.g. from a marketplace install) are never clobbered by a local import.
	row2 := row
	row2.Name = "Different Name"
	row2.PackageURL = "u2"
	if err := repo.EnsureCatalogEntry(ctx, nil, row2); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	var name, pkgURL string
	if err := d.QueryRow(`SELECT name, package_url FROM plugin_catalog WHERE id=? AND version=?`, "com.t.cat1", "1.0.0").Scan(&name, &pkgURL); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "Original Name" || pkgURL != "u1" {
		t.Fatalf("EnsureCatalogEntry overwrote an existing row: name=%q pkgURL=%q", name, pkgURL)
	}
}

func TestPluginRepo_UpsertCatalogEntry_InsertsAndUpdates(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)

	row := CatalogUpsertRow{
		ID: "com.t.cat2", Version: "1.0.0", Name: "V1", Runtime: "wasm",
		Entrypoint: "p.wasm", PackageURL: "u1", SHA256: "s1", MinPOSVersion: "0.1.0",
		PublishedAt: "2026-07-30",
	}
	if err := repo.UpsertCatalogEntry(ctx, row); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Mark deprecated directly, then upsert again: publishing a new build of
	// the same version must un-deprecate it.
	if _, err := d.DB.ExecContext(ctx, `UPDATE plugin_catalog SET is_deprecated = 1 WHERE id=? AND version=?`, "com.t.cat2", "1.0.0"); err != nil {
		t.Fatalf("mark deprecated: %v", err)
	}
	row.Name = "V1-republished"
	row.SHA256 = "s2"
	if err := repo.UpsertCatalogEntry(ctx, row); err != nil {
		t.Fatalf("upsert (update): %v", err)
	}
	var name, sha string
	var deprecated int
	if err := d.QueryRow(`SELECT name, sha256, is_deprecated FROM plugin_catalog WHERE id=? AND version=?`, "com.t.cat2", "1.0.0").
		Scan(&name, &sha, &deprecated); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "V1-republished" || sha != "s2" || deprecated != 0 {
		t.Fatalf("upsert did not update in place: name=%q sha=%q deprecated=%d", name, sha, deprecated)
	}
}

func TestPluginRepo_ListCatalog_ExcludesDeprecated(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)

	if err := repo.UpsertCatalogEntry(ctx, CatalogUpsertRow{
		ID: "com.t.live", Version: "1.0.0", Name: "Live", Runtime: "wasm", Entrypoint: "p.wasm",
		PackageURL: "u", SHA256: "s", MinPOSVersion: "0.1.0", PublishedAt: "2026-07-30",
	}); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := repo.UpsertCatalogEntry(ctx, CatalogUpsertRow{
		ID: "com.t.dead", Version: "1.0.0", Name: "Dead", Runtime: "wasm", Entrypoint: "p.wasm",
		PackageURL: "u", SHA256: "s", MinPOSVersion: "0.1.0", PublishedAt: "2026-07-30",
	}); err != nil {
		t.Fatalf("seed dead: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `UPDATE plugin_catalog SET is_deprecated = 1 WHERE id='com.t.dead'`); err != nil {
		t.Fatalf("deprecate: %v", err)
	}

	rows, err := repo.ListCatalog(ctx)
	if err != nil {
		t.Fatalf("ListCatalog: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "com.t.live" {
		t.Fatalf("expected only the non-deprecated entry, got %+v", rows)
	}
}

func TestPluginRepo_ListReceiptTemplates(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.receipt", "1.0.0")

	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.receipt", []PluginEntryRow{
		{Type: "receipt_template", Key: "compact", Label: "Compact", SortOrder: 1, ConfigJSON: `{"width":58}`},
		{Type: "page", Key: "home", Label: "Home"}, // must be excluded (wrong type)
	}); err != nil {
		t.Fatalf("seed entries: %v", err)
	}

	rows, err := repo.ListReceiptTemplates(ctx)
	if err != nil {
		t.Fatalf("ListReceiptTemplates: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 receipt template, got %+v", rows)
	}
	r := rows[0]
	if r.PluginID != "com.t.receipt" || r.PluginVersion != "1.0.0" || r.EntryKey != "compact" || r.ConfigJSON != `{"width":58}` {
		t.Fatalf("unexpected row: %+v", r)
	}

	// Deactivating the owning plugin must hide its templates too.
	if err := repo.SetPluginActive(ctx, nil, "com.t.receipt", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if rows, err := repo.ListReceiptTemplates(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("expected no templates once the plugin is inactive, got %+v err=%v", rows, err)
	}
}

func TestPluginRepo_ListPaymentEntries(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.pay", "1.0.0")

	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.pay", []PluginEntryRow{
		{Type: "payment", Key: "card_reader", Label: "Card Reader", TriggerEvent: "tender.select"},
	}); err != nil {
		t.Fatalf("seed entries: %v", err)
	}

	rows, err := repo.ListPaymentEntries(ctx)
	if err != nil {
		t.Fatalf("ListPaymentEntries: %v", err)
	}
	if len(rows) != 1 || rows[0].EntryKey != "card_reader" || rows[0].TriggerEvent != "tender.select" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestPluginRepo_SyncPluginPaymentMethods(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.pm", "1.0.0")

	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.pm", []PluginEntryRow{
		{Type: "payment", Key: "card_reader", Label: "Card Reader", ConfigJSON: `{"method_type":"card"}`, SortOrder: 1},
	}); err != nil {
		t.Fatalf("seed entries: %v", err)
	}

	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var name, typ string
	var isActive, sortOrder int
	if err := d.QueryRow(`SELECT name, type, is_active, sort_order FROM payment_methods WHERE id='card_reader'`).
		Scan(&name, &typ, &isActive, &sortOrder); err != nil {
		t.Fatalf("read synced method: %v", err)
	}
	if name != "Card Reader" || typ != "card" || isActive != 1 || sortOrder != 101 {
		t.Fatalf("unexpected synced row: name=%q type=%q active=%d sort=%d", name, typ, isActive, sortOrder)
	}

	// A built-in method (plugin_id NULL) must never be touched by the sync's
	// deactivate step, even if it shares no entry. The migration already
	// seeds 'cash' as exactly such a built-in row.
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	var cashActive int
	if err := d.QueryRow(`SELECT is_active FROM payment_methods WHERE id='cash'`).Scan(&cashActive); err != nil || cashActive != 1 {
		t.Fatalf("built-in cash method must survive sync untouched: active=%d err=%v", cashActive, err)
	}

	// Disabling the plugin removes its entry from the active set; the next
	// sync must deactivate the plugin-backed method (never delete it --
	// payments history references it) but still leave 'cash' alone.
	if err := repo.SetPluginActive(ctx, nil, "com.t.pm", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	if err := d.QueryRow(`SELECT is_active FROM payment_methods WHERE id='card_reader'`).Scan(&isActive); err != nil {
		t.Fatalf("read after disable: %v", err)
	}
	if isActive != 0 {
		t.Fatalf("expected card_reader deactivated once its plugin is disabled, got active=%d", isActive)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM payment_methods WHERE id='card_reader'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected the row kept (not deleted): n=%d err=%v", n, err)
	}
	if err := d.QueryRow(`SELECT is_active FROM payment_methods WHERE id='cash'`).Scan(&cashActive); err != nil || cashActive != 1 {
		t.Fatalf("cash must remain untouched: active=%d err=%v", cashActive, err)
	}
}

func TestPluginRepo_SyncPluginPaymentMethods_InvalidConfigDefaultsToCard(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.pm2", "1.0.0")

	// No config_json at all (empty string, not valid JSON) must fall back to
	// 'card' rather than erroring the whole sync.
	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.pm2", []PluginEntryRow{
		{Type: "payment", Key: "mystery_tender", Label: "Mystery"},
	}); err != nil {
		t.Fatalf("seed entries: %v", err)
	}
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var typ string
	if err := d.QueryRow(`SELECT type FROM payment_methods WHERE id='mystery_tender'`).Scan(&typ); err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != "card" {
		t.Fatalf("expected default type 'card' for missing config, got %q", typ)
	}
}

func TestPluginRepo_ListPageEntries(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.page", "1.0.0")

	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.page", []PluginEntryRow{
		{Type: "page", Key: "loyalty", Label: "Loyalty", Route: "/loyalty"},
		{Type: "button", Key: "b", Label: "B"}, // excluded
	}); err != nil {
		t.Fatalf("seed entries: %v", err)
	}

	rows, err := repo.ListPageEntries(ctx)
	if err != nil {
		t.Fatalf("ListPageEntries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 page entry, got %+v", rows)
	}
	r := rows[0]
	if r.PluginID != "com.t.page" || r.PluginVersion != "1.0.0" || r.EntryKey != "loyalty" || r.Route != "/loyalty" {
		t.Fatalf("unexpected row: %+v", r)
	}

	if err := repo.SetPluginActive(ctx, nil, "com.t.page", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if rows, err := repo.ListPageEntries(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("expected no page entries once plugin inactive, got %+v err=%v", rows, err)
	}
}
