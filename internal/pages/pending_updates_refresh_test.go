package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/settings"
)

// TestReloadPlugins_RefreshesPendingUpdates_InstallLandedLatest covers the
// card's own scenario (ut-docs#2787): a sync/manual install/update lands the
// catalog's latest version, but the status-bar chip was last published by a
// scheduler tick up to 15 minutes ago and still says an update is pending.
// ReloadPlugins (which every plugin lifecycle change passes through) must
// refresh the published count immediately, not leave it stale.
func TestReloadPlugins_RefreshesPendingUpdates_InstallLandedLatest(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.theme", "Theme", "dev-1", "1.1.0", "theme")
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Theme", Version: "1.1.0", CanonicalType: "theme"},
	})
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 2})

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}

	if got := plugins.CurrentPendingUpdates(); got.Count != 0 {
		t.Fatalf("Count = %d, want 0 — the installed version already matches the catalog", got.Count)
	}
}

// TestReloadPlugins_RefreshesPendingUpdates_RollbackLeavesOnePending covers a
// rollback landing an older version than the catalog: the chip must count it.
func TestReloadPlugins_RefreshesPendingUpdates_RollbackLeavesOnePending(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.theme", "Theme", "dev-1", "1.0.0", "theme")
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Theme", Version: "1.1.0", CanonicalType: "theme"},
	})
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 0})

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}

	got := plugins.CurrentPendingUpdates()
	if got.Count != 1 {
		t.Fatalf("Count = %d, want 1", got.Count)
	}
	if got.LanguagePending {
		t.Fatalf("LanguagePending = true, want false — the pending update is a theme, not a language pack")
	}
}

// TestReloadPlugins_RefreshesPendingUpdates_Uninstall covers an uninstall
// dropping the plugin the stale count was about.
func TestReloadPlugins_RefreshesPendingUpdates_Uninstall(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.theme", "Theme", "dev-1", "1.0.0", "theme")
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Theme", Version: "1.1.0", CanonicalType: "theme"},
	})
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 1})

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

	if err := plugins.UninstallPlugin(t.Context(), db, "com.test.theme"); err != nil {
		t.Fatalf("UninstallPlugin: %v", err)
	}
	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}

	if got := plugins.CurrentPendingUpdates().Count; got != 0 {
		t.Fatalf("Count = %d, want 0 — the plugin the pending update was about is gone", got)
	}
}

// TestReloadPlugins_RefreshesPendingUpdates_MainTillAutoAppliesLanguage
// covers a main/standalone till: a pending language-pack update is what the
// scheduler auto-applies itself, so RefreshPendingUpdates must not count it
// (ut-docs#2787 design step 1).
func TestReloadPlugins_RefreshesPendingUpdates_MainTillAutoAppliesLanguage(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang", "Lang Pack", "dev-1", "1.0.0", "language")
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Lang Pack", Version: "1.1.0", CanonicalType: "language"},
	})

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

	// A stale language-pack count (e.g. from an earlier tick) must clear —
	// without it this test would pass with no refresh at all.
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 1, LanguagePending: true})

	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}

	got := plugins.CurrentPendingUpdates()
	if got.Count != 0 {
		t.Fatalf("Count = %d, want 0 — a main till auto-applies the language pack itself", got.Count)
	}
	if got.LanguagePending {
		t.Fatalf("LanguagePending = true, want false on a main till")
	}
}

// TestReloadPlugins_RefreshesPendingUpdates_JoinedTillCountsLanguage covers a
// joined till (sync.primary_url set): it never auto-applies, so a pending
// language update must count and set LanguagePending/MainTillURL.
func TestReloadPlugins_RefreshesPendingUpdates_JoinedTillCountsLanguage(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang", "Lang Pack", "dev-1", "1.0.0", "language")
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Lang Pack", Version: "1.1.0", CanonicalType: "language"},
	})
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "https://primary.example"); err != nil {
		t.Fatalf("set sync.primary_url: %v", err)
	}

	d := &common.Deps{Db: db, Settings: st, CatalogRepo: repo, Cfg: schedulerTestCfg}

	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}

	got := plugins.CurrentPendingUpdates()
	if got.Count != 1 {
		t.Fatalf("Count = %d, want 1", got.Count)
	}
	if !got.LanguagePending {
		t.Fatalf("LanguagePending = false, want true on a joined till")
	}
	if got.MainTillURL != "https://primary.example" {
		t.Fatalf("MainTillURL = %q, want the primary's address", got.MainTillURL)
	}
}

// TestReloadPlugins_RefreshesPendingUpdates_NilCatalogRepoIsNoOp covers a
// Deps with no catalog repo (not fully wired) — the published status must be
// left untouched, matching pluginUpdateCheckTick's own nil-CatalogRepo no-op.
func TestReloadPlugins_RefreshesPendingUpdates_NilCatalogRepoIsNoOp(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 42})
	db := openRealSchemaPagesDB(t)

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: nil, Cfg: schedulerTestCfg}

	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}

	if got := plugins.CurrentPendingUpdates().Count; got != 42 {
		t.Fatalf("Count = %d, want unchanged (42) — nil CatalogRepo must be a silent no-op", got)
	}
}

// TestUninstallHandler_RefreshesPendingUpdatesChip drives the real mux
// (POST /api/plugins/{id}/uninstall via registerPluginAPI) rather than
// calling ReloadPlugins directly: two installed themes each have a pending
// update, a real scheduler tick publishes Count=2, and uninstalling one
// through the handler must drop the chip to exactly 1 immediately — not
// leave it at 2 until the next 15-minute tick (ut-docs#2787).
func TestUninstallHandler_RefreshesPendingUpdatesChip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetPendingUpdatesAfterTest(t)
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.theme-a", "Theme A", "dev-1", "1.0.0", "theme")
	seedInstalledPluginManifest(t, db, "com.test.theme-b", "Theme B", "dev-1", "1.0.0", "theme")
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Theme A", Version: "1.1.0", CanonicalType: "theme"},
		{DeveloperID: "dev-1", Name: "Theme B", Version: "1.1.0", CanonicalType: "theme"},
	})

	cfg := basePluginCfg()
	cfg.DefaultLocale = schedulerTestCfg.DefaultLocale
	deps := newPluginAPIDeps(t, db, cfg)
	deps.CatalogRepo = repo
	if err := deps.Pm.Reload(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	pluginUpdateCheckTick(t.Context(), deps)
	if got := plugins.CurrentPendingUpdates().Count; got != 2 {
		t.Fatalf("precondition: tick published Count = %d, want 2", got)
	}

	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.theme-a/uninstall", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if got := plugins.CurrentPendingUpdates().Count; got != 1 {
		t.Fatalf("Count = %d after uninstalling one of two pending plugins, want 1", got)
	}
}
