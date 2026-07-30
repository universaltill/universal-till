package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	// GetUpdateInfo finds it by plugin id, and errors for unknown ids.
	info, err := uc.GetUpdateInfo(ctx, "com.test.upd")
	if err != nil || info.AvailableVersion != "1.2.0" {
		t.Fatalf("GetUpdateInfo: %+v, %v", info, err)
	}
	if _, err := uc.GetUpdateInfo(ctx, "com.test.unknown"); err == nil || !strings.Contains(err.Error(), "no update available") {
		t.Fatalf("unknown id: %v", err)
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
