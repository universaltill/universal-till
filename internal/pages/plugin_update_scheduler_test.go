package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
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

// schedulerTestCfg supplies the shop default locale the update checker keys
// its catalog read on (ut-docs#2674).
var schedulerTestCfg = &config.Config{DefaultLocale: "en-US"}

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
		Locale:          schedulerTestCfg.DefaultLocale,
		DeviceArch:      marketplace.DeviceArch(),
	}
	repo, err := marketplace.NewCatalogRepository(nil, cacheDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	// Written as the pre-ut-docs#2674 single file, which the repository
	// still serves for the (Locale, DeviceArch) key recorded in it.
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "catalog-snapshot.json"), raw, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return repo
}

// resetPendingUpdatesAfterTest restores the process-global pending-update
// count these tests publish into. Without it a test that deliberately parks
// a non-zero count (the no-op / catalog-error cases below) leaks it into
// every later test in this package — including anything that renders
// base.html, which would then grow a phantom "Plugin updates available"
// status chip and fail for reasons that have nothing to do with what it is
// testing (ut-docs#1953 review).
func resetPendingUpdatesAfterTest(t *testing.T) {
	t.Helper()
	before := plugins.CurrentPendingUpdates()
	t.Cleanup(func() { plugins.PublishPendingUpdates(before) })
}

func TestPluginUpdateCheckTick_AutoAppliesLanguagePacksOnly(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang", "Lang Pack", "dev-1", "1.0.0", "language")
	seedInstalledPluginManifest(t, db, "com.test.theme", "Theme", "dev-1", "1.0.0", "theme")

	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Lang Pack", Version: "1.1.0", CanonicalType: "language"},
		{DeveloperID: "dev-1", Name: "Theme", Version: "1.1.0", CanonicalType: "theme"},
	})

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

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
	if got := plugins.CurrentPendingUpdates(); got.Count != 1 {
		t.Fatalf("pending count = %d, want 1 (the theme update, left for the merchant)", got.Count)
	} else if got.LanguagePending {
		t.Fatalf("LanguagePending = true, want false — the only pending item is a theme, not a language pack")
	}
}

func TestPluginUpdateCheckTick_ReplicaNeverAutoApplies(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang2", "Lang Pack 2", "dev-2", "1.0.0", "language")

	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-2", Name: "Lang Pack 2", Version: "1.1.0", CanonicalType: "language"},
	})

	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "https://primary.example"); err != nil {
		t.Fatalf("set sync.primary_url: %v", err)
	}
	d := &common.Deps{Db: db, Settings: st, CatalogRepo: repo, Cfg: schedulerTestCfg}

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
	if got := plugins.CurrentPendingUpdates(); got.Count != 1 {
		t.Fatalf("pending count = %d, want 1 (even a language-pack update stays pending on a replica)", got.Count)
	} else if !got.LanguagePending {
		t.Fatalf("LanguagePending = false, want true — a replica's pending language-pack update must surface distinctly (ut-docs#2299)")
	} else if got.MainTillURL != "https://primary.example" {
		t.Fatalf("MainTillURL = %q, want the main till's address so the chip can point there (ut-docs#2783)", got.MainTillURL)
	}
}

func TestPluginUpdateCheckTick_FailedAutoApplyCountsAsPending(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPluginManifest(t, db, "com.test.lang3", "Lang Pack 3", "dev-3", "1.0.0", "language")

	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-3", Name: "Lang Pack 3", Version: "1.1.0", CanonicalType: "language"},
	})
	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

	orig := pluginApplyUpdateFn
	pluginApplyUpdateFn = func(ctx context.Context, d *common.Deps, pluginID string) (string, string, error) {
		return "", "", errors.New("simulated install failure")
	}
	t.Cleanup(func() { pluginApplyUpdateFn = orig })

	pluginUpdateCheckTick(t.Context(), d)

	if got := plugins.CurrentPendingUpdates(); got.Count != 1 {
		t.Fatalf("pending count = %d, want 1 — a failed auto-apply must still surface, not vanish", got.Count)
	} else if !got.LanguagePending {
		t.Fatalf("LanguagePending = false, want true — a failed language-pack auto-apply must surface distinctly (ut-docs#2299)")
	}
}

func TestPluginUpdateCheckTick_NoCatalogRepo_NoOp(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 99, LanguagePending: false})
	d := &common.Deps{Db: openRealSchemaPagesDB(t), Settings: settings.NewStore(nil), CatalogRepo: nil}

	pluginUpdateCheckTick(t.Context(), d)

	if got := plugins.CurrentPendingUpdates().Count; got != 99 {
		t.Fatalf("pending count = %d, want unchanged (99) — nil CatalogRepo must be a silent no-op", got)
	}
}

func TestPluginUpdateCheckTick_CatalogReadError_LeavesPendingUnchanged(t *testing.T) {
	resetPendingUpdatesAfterTest(t)
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{Count: 7, LanguagePending: false})
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
	d := &common.Deps{Db: db, Settings: settings.NewStore(db), CatalogRepo: repo, Cfg: schedulerTestCfg}

	pluginUpdateCheckTick(t.Context(), d)

	if got := plugins.CurrentPendingUpdates().Count; got != 7 {
		t.Fatalf("pending count = %d, want unchanged (7) — a failed check must log and return, never surface an error", got)
	}
}

// TestStartPluginUpdateScheduler_RunsOnFirstStart proves the scheduler's tick
// fires on process start (bounded by pluginUpdateCheckInitialDelay) rather
// than only on the first full interval elapsing — ut-docs#2299's acceptance
// criterion "after a core self-update, the plugin-update check runs on the
// new version's first start (test on the scheduler trigger, not just a
// manual invocation)". selfupdate.Apply re-execs the binary after a core
// update (internal/selfupdate/selfupdate.go), and that re-exec is a fresh
// process start like any other — app.Run wires StartPluginUpdateScheduler
// unconditionally on every boot (internal/pages/init.go), so a scheduler
// that already ticks promptly on start needs no separate "was this a
// version change" trigger of its own; this test is what actually proves
// that's true, rather than trusting the reasoning above with no coverage.
func TestStartPluginUpdateScheduler_RunsOnFirstStart(t *testing.T) {
	origDelay, origInterval, origTick := pluginUpdateCheckInitialDelay, pluginUpdateCheckInterval, pluginUpdateTickFn
	t.Cleanup(func() {
		pluginUpdateCheckInitialDelay, pluginUpdateCheckInterval, pluginUpdateTickFn = origDelay, origInterval, origTick
	})
	pluginUpdateCheckInitialDelay = 20 * time.Millisecond
	pluginUpdateCheckInterval = 2 * time.Second // must never fire within this test's timeout

	ticks := make(chan struct{}, 8)
	pluginUpdateTickFn = func(ctx context.Context, d *common.Deps) { ticks <- struct{}{} }

	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	d := &common.Deps{}
	StartPluginUpdateScheduler(ctx, d, &wg)

	select {
	case <-ticks:
		// Got the first tick — now prove it really came from the initial
		// delay, not a slow-starting ticker: no second tick should arrive
		// for a while (the interval is 2s, checked well short of that).
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no tick within 500ms of start — StartPluginUpdateScheduler must run on first start, not wait a full interval (pluginUpdateCheckInterval=2s)")
	}
	select {
	case <-ticks:
		t.Fatal("a second tick arrived almost immediately — the first tick must come from the initial delay, not from an interval far shorter than configured")
	case <-time.After(300 * time.Millisecond):
	}

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler goroutine did not join wg within 2s of context cancel")
	}
}
