package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// seededCatalogRepo builds a CatalogRepository whose on-disk snapshot is
// pre-written — no marketplace client, no network.
func seededCatalogRepo(t *testing.T, plugins []marketplace.PluginSummary) *marketplace.CatalogRepository {
	t.Helper()
	cacheDir := t.TempDir()
	snapshot := marketplace.CatalogSnapshot{
		Plugins:         plugins,
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

func TestCheckForUpdatesFindsNewerVersion(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	// Installed 1.0.0 by author dev-1; catalog offers 1.2.0 (plus an older
	// duplicate listing that must lose the highest-version dedupe).
	seedInstalledPlugin(t, db, "com.test.upd", "Updatable", "1.0.0", "none", true)
	if _, err := db.Exec(`UPDATE plugin_catalog SET author = 'dev-1' WHERE id = 'com.test.upd'`); err != nil {
		t.Fatalf("set author: %v", err)
	}
	if _, err := db.Exec(`UPDATE plugins SET author = 'dev-1' WHERE id = 'com.test.upd'`); err != nil {
		t.Fatalf("set author: %v", err)
	}

	repo := seededCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Updatable", Version: "1.1.0", ArtifactHash: "sha256:aaa"},
		{DeveloperID: "dev-1", Name: "Updatable", Version: "1.2.0", ArtifactHash: "sha256:bbb", TrustTier: "verified"},
		{DeveloperID: "dev-2", Name: "Other", Version: "9.9.9"},
	})

	uc := NewUpdateChecker(db, repo)
	updates, err := uc.CheckForUpdates(ctx)
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %+v", updates)
	}
	u := updates[0]
	if u.PluginID != "com.test.upd" || u.InstalledVersion != "1.0.0" || u.AvailableVersion != "1.2.0" {
		t.Fatalf("unexpected update: %+v", u)
	}
	if u.ArtifactHash != "bbb" {
		t.Fatalf("sha256: prefix not stripped: %q", u.ArtifactHash)
	}
}

// TestCheckForUpdatesCarriesCanonicalType covers ut-docs#1953:
// StartPluginUpdateScheduler decides whether to auto-apply an update by the
// installed plugin's canonical type (language packs only), so UpdateInfo
// must actually carry it through from the catalog snapshot.
func TestCheckForUpdatesCarriesCanonicalType(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	seedInstalledPlugin(t, db, "com.test.lang", "German Pack", "1.0.0", "none", true)
	if _, err := db.Exec(`UPDATE plugin_catalog SET author = 'ut' WHERE id = 'com.test.lang'`); err != nil {
		t.Fatalf("set author: %v", err)
	}
	if _, err := db.Exec(`UPDATE plugins SET author = 'ut' WHERE id = 'com.test.lang'`); err != nil {
		t.Fatalf("set author: %v", err)
	}

	repo := seededCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "ut", Name: "German Pack", Version: "1.1.0", CanonicalType: "language"},
	})

	uc := NewUpdateChecker(db, repo)
	updates, err := uc.CheckForUpdates(ctx)
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %+v", updates)
	}
	if updates[0].CanonicalType != "language" {
		t.Fatalf("CanonicalType = %q, want %q", updates[0].CanonicalType, "language")
	}
}

// TestCheckForUpdatesMatchesByInstallStatusListing is the ut-docs#1953
// review's regression test. Matching an installed plugin to its catalog
// listing by manifest author+name is a heuristic nothing enforces: the
// installed Author/Name are the plugin's OWN manifest values (persisted
// verbatim by installer_marketplace.go), while the catalog side is the
// listing's developer_id (which falls back to the vendor display string)
// and the listing's name. A real marketplace-installed language pack whose
// manifest author is a company name and whose listing developer_id is a
// developer identifier matched nothing at all — so the new background
// scheduler discovered nothing, auto-applied nothing and showed no chip,
// i.e. the exact pilot-till symptom the card exists to fix. The
// install-status store's listing↔plugin record — written by the installer,
// and already what /plugins' badge and applyPluginUpdate resolve through —
// is the authoritative mapping and must win.
func TestCheckForUpdatesMatchesByInstallStatusListing(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	seedInstalledPlugin(t, db, "com.universaltill.language.de", "German Language Pack", "1.0.0", "none", true)
	if _, err := db.Exec(`UPDATE plugins SET author = 'Universal Till GmbH' WHERE id = 'com.universaltill.language.de'`); err != nil {
		t.Fatalf("set author: %v", err)
	}

	if err := NewInstallStatusStore(db).Save(ctx, InstallStatusRecord{
		ListingID:      "listing-de-123",
		PluginID:       "com.universaltill.language.de",
		PluginName:     "German Language Pack",
		CurrentVersion: "1.0.0",
		State:          InstallStateActive,
	}); err != nil {
		t.Fatalf("save install status: %v", err)
	}

	// Neither developer_id nor the listing name matches the installed
	// manifest's author/name — only the listing id ties them together.
	repo := seededCatalogRepo(t, []marketplace.PluginSummary{
		{
			ID: "listing-de-123", ListingID: "listing-de-123",
			DeveloperID: "dev-ut-42", Name: "Deutsch (Germany) Language Pack",
			Version: "1.4.0", CanonicalType: "language",
		},
	})

	updates, err := NewUpdateChecker(db, repo).CheckForUpdates(ctx)
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %+v, want the listing-matched update", updates)
	}
	if updates[0].PluginID != "com.universaltill.language.de" || updates[0].AvailableVersion != "1.4.0" {
		t.Fatalf("unexpected update: %+v", updates[0])
	}
	if updates[0].CanonicalType != "language" {
		t.Fatalf("CanonicalType = %q, want language (the scheduler's auto-apply key)", updates[0].CanonicalType)
	}
}

func TestCheckForUpdatesNoInstalledOrCurrent(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	// No installed plugins → empty, and the catalog isn't even needed.
	uc := NewUpdateChecker(db, seededCatalogRepo(t, nil))
	updates, err := uc.CheckForUpdates(ctx)
	if err != nil || len(updates) != 0 {
		t.Fatalf("empty install: %+v, %v", updates, err)
	}

	// Installed and current → no update offered.
	seedInstalledPlugin(t, db, "com.test.current", "Current", "2.0.0", "none", true)
	if _, err := db.Exec(`UPDATE plugins SET author = 'dev-1' WHERE id = 'com.test.current'`); err != nil {
		t.Fatalf("set author: %v", err)
	}
	uc2 := NewUpdateChecker(db, seededCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Current", Version: "2.0.0"},
	}))
	updates, err = uc2.CheckForUpdates(ctx)
	if err != nil || len(updates) != 0 {
		t.Fatalf("current version offered as update: %+v, %v", updates, err)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		v1, v2 string
		want   int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.2.0", "1.10.0", -1}, // numeric, not lexicographic
		{"0.2.49", "0.2.5", 1},
		{"2.0", "2.0.0", 0}, // padded
		{"2.0.1", "2.0", 1},
		{"1.0.0", "2.0.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.v1, c.v2); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.v1, c.v2, got, c.want)
		}
	}
}
