package data

import (
	"context"
	"testing"
)

// TestPluginRepo_ListBrokenPluginsForHook_ScopedCorrectly pins the
// highest-consequence property of ListBrokenPluginsForHook (ut-docs#368,
// review finding 2026-08-13): it must return ONLY an active plugin, broken,
// registered for the exact event asked about — never anything else. Getting
// this scoping wrong in either direction is a real production hazard: too
// broad, and an unrelated broken plugin (say, a barcode scanner) blocks tax
// till-wide; too narrow, and the fail-closed tax guard misses a plugin it
// should have caught.
func TestPluginRepo_ListBrokenPluginsForHook_ScopedCorrectly(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)

	const event = "tax.rate.ask"

	// The one plugin that SHOULD show up: active, broken, active hook for
	// the exact event.
	seedCatalogAndPlugin(t, ctx, repo, "com.t.tax-broken", "1.0.0")
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.tax-broken", Event: event, Action: "tax.rate", Priority: 100, IsActive: true},
	}); err != nil {
		t.Fatalf("seed broken tax plugin hook: %v", err)
	}
	if err := repo.SetPluginState(ctx, "com.t.tax-broken", "1.0.0", "broken", true); err != nil {
		t.Fatalf("mark broken: %v", err)
	}

	// A broken plugin registered for a DIFFERENT event (e.g. a barcode
	// scanner plugin) must never show up when asking about tax.rate.ask —
	// the failure mode the review specifically called out: an unrelated
	// broken plugin blocking tax till-wide.
	seedCatalogAndPlugin(t, ctx, repo, "com.t.scanner-broken", "1.0.0")
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.scanner-broken", Event: "barcode.scanned", Action: "scan.normalize", Priority: 100, IsActive: true},
	}); err != nil {
		t.Fatalf("seed broken scanner plugin hook: %v", err)
	}
	if err := repo.SetPluginState(ctx, "com.t.scanner-broken", "1.0.0", "broken", true); err != nil {
		t.Fatalf("mark scanner broken: %v", err)
	}

	// A tax plugin that is DISABLED (is_active=0), not just broken, must not
	// show up either — a disabled plugin isn't "supposed to be running," so
	// its absence isn't the silently-wrong-tax hazard this exists to catch.
	seedCatalogAndPlugin(t, ctx, repo, "com.t.tax-disabled", "1.0.0")
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.tax-disabled", Event: event, Action: "tax.rate", Priority: 100, IsActive: true},
	}); err != nil {
		t.Fatalf("seed disabled tax plugin hook: %v", err)
	}
	if err := repo.SetPluginState(ctx, "com.t.tax-disabled", "1.0.0", "broken", false); err != nil {
		t.Fatalf("mark disabled+broken: %v", err)
	}

	// A tax plugin that is broken but whose tax.rate.ask HOOK is inactive
	// (is_active=0 on the plugin_hooks row) must not show up — it isn't
	// registered to answer this event right now, broken or not.
	seedCatalogAndPlugin(t, ctx, repo, "com.t.tax-inactive-hook", "1.0.0")
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.tax-inactive-hook", Event: event, Action: "tax.rate", Priority: 100, IsActive: false},
	}); err != nil {
		t.Fatalf("seed inactive-hook tax plugin hook: %v", err)
	}
	if err := repo.SetPluginState(ctx, "com.t.tax-inactive-hook", "1.0.0", "broken", true); err != nil {
		t.Fatalf("mark inactive-hook plugin broken: %v", err)
	}

	// A HEALTHY tax plugin (install_state stays 'installed') must not show
	// up — only 'broken' is the fail-closed signal.
	seedCatalogAndPlugin(t, ctx, repo, "com.t.tax-healthy", "1.0.0")
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.tax-healthy", Event: event, Action: "tax.rate", Priority: 100, IsActive: true},
	}); err != nil {
		t.Fatalf("seed healthy tax plugin hook: %v", err)
	}

	rows, err := repo.ListBrokenPluginsForHook(ctx, event)
	if err != nil {
		t.Fatalf("ListBrokenPluginsForHook: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "com.t.tax-broken" {
		t.Fatalf("expected exactly [com.t.tax-broken], got %+v", rows)
	}
}
