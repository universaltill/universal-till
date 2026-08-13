package data

import (
	"context"
	"testing"
)

// ut-docs#368: a joined replica can hold a plugins row that says "installed"
// while the plugin's FILE is missing (a join snapshot writes rows, never
// bytes). WasmRuntime.Sync flips such a row to install_state='broken' via
// UpdatePluginInstallState, and the tax fail-closed check asks
// HasBrokenActivePluginForEvent whether the authority for an event (e.g.
// tax.rate.ask) is currently broken — a manifest-registration check
// (plugin_hooks, populated at install time), NOT a "currently loaded" check,
// which is exactly why it still answers correctly while the plugin cannot
// subscribe to the bus.

func TestUpdatePluginInstallState_FlipsOnlyTheNamedPlugin(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	seedCatalogEntry(t, d, "com.example.other", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}
	if err := repo.InstallPlugin(ctx, nil, "com.example.other"); err != nil {
		t.Fatal(err)
	}

	if err := repo.UpdatePluginInstallState(ctx, "com.example.tax", PluginStateBroken); err != nil {
		t.Fatalf("mark broken: %v", err)
	}

	info, ok, err := repo.GetPlugin(ctx, "com.example.tax", "")
	if err != nil || !ok {
		t.Fatalf("get plugin: ok=%v err=%v", ok, err)
	}
	if info.InstallState != PluginStateBroken {
		t.Fatalf("expected install_state broken, got %q", info.InstallState)
	}
	other, _, _ := repo.GetPlugin(ctx, "com.example.other", "")
	if other.InstallState != PluginStateInstalled {
		t.Fatalf("the other plugin's state must be untouched, got %q", other.InstallState)
	}

	// Self-heal direction: broken -> installed must be just as visible.
	if err := repo.UpdatePluginInstallState(ctx, "com.example.tax", PluginStateInstalled); err != nil {
		t.Fatalf("heal: %v", err)
	}
	info, _, _ = repo.GetPlugin(ctx, "com.example.tax", "")
	if info.InstallState != PluginStateInstalled {
		t.Fatalf("expected install_state healed back to installed, got %q", info.InstallState)
	}
}

func TestHasBrokenActivePluginForEvent(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	// An ACTIVE, BROKEN plugin registered (at install time, from its
	// manifest) for tax.rate.ask.
	seedBrokenStatePlugin2 := func(id string, active bool, state, event string) {
		t.Helper()
		isActive := 0
		if active {
			isActive = 1
		}
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugin_catalog
(id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at, is_deprecated)
VALUES (?, '1.0.0', 'P', 'd', 'a', 'w', 'wasm', './plugin.wasm', 'u', 's', '0.1.0', '1', datetime('now'), 0)`, id); err != nil {
			t.Fatalf("seed catalog %s: %v", id, err)
		}
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugins
(id, name, version, install_state, entrypoint, runtime, is_active)
VALUES (?, 'P', '1.0.0', ?, './plugin.wasm', 'wasm', ?)`, id, state, isActive); err != nil {
			t.Fatalf("seed plugin %s: %v", id, err)
		}
		if event != "" {
			if _, err := d.DB.ExecContext(ctx, `INSERT INTO plugin_hooks
(id, plugin_id, event, action, is_active) VALUES (?, ?, ?, 'tax.rate', 1)`,
				"hook-"+id, id, event); err != nil {
				t.Fatalf("seed hook %s: %v", id, err)
			}
		}
	}

	// No plugins at all: not broken.
	broken, err := repo.HasBrokenActivePluginForEvent(ctx, "tax.rate.ask")
	if err != nil {
		t.Fatal(err)
	}
	if broken {
		t.Fatal("no plugins: expected not broken")
	}

	// A healthy, installed tax plugin: not broken.
	seedBrokenStatePlugin2("com.example.tax-ok", true, PluginStateInstalled, "tax.rate.ask")
	if broken, _ = repo.HasBrokenActivePluginForEvent(ctx, "tax.rate.ask"); broken {
		t.Fatal("healthy plugin: expected not broken")
	}

	// A broken plugin registered for a DIFFERENT event: tax stays fine.
	seedBrokenStatePlugin2("com.example.loyalty-broken", true, PluginStateBroken, "sale.completed")
	if broken, _ = repo.HasBrokenActivePluginForEvent(ctx, "tax.rate.ask"); broken {
		t.Fatal("broken plugin on another event must not block tax")
	}

	// An INACTIVE broken tax plugin: disabled on purpose, must not block.
	seedBrokenStatePlugin2("com.example.tax-disabled", false, PluginStateBroken, "tax.rate.ask")
	if broken, _ = repo.HasBrokenActivePluginForEvent(ctx, "tax.rate.ask"); broken {
		t.Fatal("a disabled broken plugin must not block tax")
	}

	// The real case: an ACTIVE broken plugin registered for tax.rate.ask.
	seedBrokenStatePlugin2("com.example.tax-broken", true, PluginStateBroken, "tax.rate.ask")
	if broken, _ = repo.HasBrokenActivePluginForEvent(ctx, "tax.rate.ask"); !broken {
		t.Fatal("an active broken tax plugin must report broken")
	}
}

func TestListInstalledPlugins_CarriesInstallState(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdatePluginInstallState(ctx, "com.example.tax", PluginStateBroken); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ListInstalledPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].InstallState != PluginStateBroken {
		t.Fatalf("ListInstalledPlugins must carry install_state (WasmRuntime.Sync keys its broken/heal transitions on it), got %q", rows[0].InstallState)
	}
}
