package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/settings"
)

// seedInstalledPluginManifest installs a minimal plugin through the real
// persistence path, like plugin_api_test.go's seedInstalledPlugin, but also
// sets Name/Author/CanonicalType — plugins.UpdateChecker matches an
// installed plugin against the catalog by author+name, and
// StartPluginUpdateScheduler's auto-apply decision keys on canonical type,
// neither of which the shared seedInstalledPlugin helper sets.
func seedInstalledPluginManifest(t *testing.T, db *sql.DB, id, name, author, version, canonicalType string) {
	t.Helper()
	m := &plugins.Manifest{
		ID:            id,
		Name:          name,
		Version:       version,
		Author:        author,
		Entrypoint:    "./plugin",
		Runtime:       "go",
		CanonicalType: canonicalType,
		DeviceArch:    "any",
	}
	if err := plugins.PersistManifest(t.Context(), db, m, plugins.InstallOptions{
		TrustLevel: "untrusted",
		Uploader:   "test",
	}); err != nil {
		t.Fatalf("seed installed plugin %s: %v", id, err)
	}
}

// seededSchedulerCatalogRepo writes a catalog snapshot straight to disk —
// the same "no marketplace client, no network" shape update_checker_test.go's
// own seededCatalogRepo uses in the plugins package, reimplemented here
// because that helper isn't exported across packages.
func seededSchedulerCatalogRepo(t *testing.T, summaries []marketplace.PluginSummary) *marketplace.CatalogRepository {
	t.Helper()
	cacheDir := t.TempDir()
	snapshot := marketplace.CatalogSnapshot{
		Plugins:         summaries,
		SnapshotVersion: 1,
		FetchedAt:       time.Now(),
		Locale:          "en",
		DeviceArch:      "any",
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "catalog-snapshot.json"), raw, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	repo, err := marketplace.NewCatalogRepository(nil, cacheDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	return repo
}

func TestPluginUpdateCheckTick_AutoAppliesLanguagePacksOnly(t *testing.T) {
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang", "Lang Pack", "dev-1", "1.0.0", "language")
	seedInstalledPluginManifest(t, db, "com.test.theme", "Theme", "dev-1", "1.0.0", "theme")

	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Lang Pack", Version: "1.1.0", CanonicalType: "language"},
		{DeveloperID: "dev-1", Name: "Theme", Version: "1.1.0", CanonicalType: "theme"},
	})

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo}

	var applied []string
	orig := pluginApplyUpdateFn
	pluginApplyUpdateFn = func(ctx context.Context, d *common.Deps, pluginID string) (string, string, error) {
		applied = append(applied, pluginID)
		return "1.0.0", "1.1.0", nil
	}
	t.Cleanup(func() { pluginApplyUpdateFn = orig })

	pluginUpdateCheckTick(t.Context(), d)

	if len(applied) != 1 || applied[0] != "com.test.lang" {
		t.Fatalf("expected only the language pack to be auto-applied, got %v", applied)
	}
	if got := plugins.CurrentPendingUpdates().Count; got != 1 {
		t.Fatalf("pending count = %d, want 1 (the theme update, left for the merchant)", got)
	}
}

func TestPluginUpdateCheckTick_ReplicaNeverAutoApplies(t *testing.T) {
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang2", "Lang Pack 2", "dev-2", "1.0.0", "language")

	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-2", Name: "Lang Pack 2", Version: "1.1.0", CanonicalType: "language"},
	})

	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "https://primary.example"); err != nil {
		t.Fatalf("set sync.primary_url: %v", err)
	}
	d := &common.Deps{Db: db, Settings: st, CatalogRepo: repo}

	called := false
	orig := pluginApplyUpdateFn
	pluginApplyUpdateFn = func(ctx context.Context, d *common.Deps, pluginID string) (string, string, error) {
		called = true
		return "", "", nil
	}
	t.Cleanup(func() { pluginApplyUpdateFn = orig })

	pluginUpdateCheckTick(t.Context(), d)

	if called {
		t.Fatalf("a replica till must never auto-apply a plugin update locally (ut-docs#460)")
	}
	if got := plugins.CurrentPendingUpdates().Count; got != 1 {
		t.Fatalf("pending count = %d, want 1 (even a language-pack update stays pending on a replica)", got)
	}
}

func TestPluginUpdateCheckTick_FailedAutoApplyCountsAsPending(t *testing.T) {
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang3", "Lang Pack 3", "dev-3", "1.0.0", "language")

	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-3", Name: "Lang Pack 3", Version: "1.1.0", CanonicalType: "language"},
	})
	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo}

	orig := pluginApplyUpdateFn
	pluginApplyUpdateFn = func(ctx context.Context, d *common.Deps, pluginID string) (string, string, error) {
		return "", "", errors.New("simulated install failure")
	}
	t.Cleanup(func() { pluginApplyUpdateFn = orig })

	pluginUpdateCheckTick(t.Context(), d)

	if got := plugins.CurrentPendingUpdates().Count; got != 1 {
		t.Fatalf("pending count = %d, want 1 — a failed auto-apply must still surface, not vanish", got)
	}
}

func TestPluginUpdateCheckTick_NoCatalogRepo_NoOp(t *testing.T) {
	plugins.SetPendingUpdates(99)
	d := &common.Deps{Db: openRealSchemaPagesDB(t), Settings: settings.NewStore(nil), CatalogRepo: nil}

	pluginUpdateCheckTick(t.Context(), d)

	if got := plugins.CurrentPendingUpdates().Count; got != 99 {
		t.Fatalf("pending count = %d, want unchanged (99) — nil CatalogRepo must be a silent no-op", got)
	}
}

func TestPluginUpdateCheckTick_CatalogReadError_LeavesPendingUnchanged(t *testing.T) {
	plugins.SetPendingUpdates(7)
	db := openRealSchemaPagesDB(t)
	// CheckForUpdates short-circuits before ever touching the catalog when
	// there are zero installed plugins, so an installed plugin is needed
	// here to actually reach (and fail) the catalog read below.
	seedInstalledPluginManifest(t, db, "com.test.anyplugin", "Any Plugin", "dev-x", "1.0.0", "page")
	// An empty cache dir: CatalogRepository.Get() errors "no catalog
	// snapshot available" — the offline-first "never fetched yet" case.
	repo, err := marketplace.NewCatalogRepository(nil, t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo}

	pluginUpdateCheckTick(t.Context(), d)

	if got := plugins.CurrentPendingUpdates().Count; got != 7 {
		t.Fatalf("pending count = %d, want unchanged (7) — a failed check must log and return, never surface an error", got)
	}
}
