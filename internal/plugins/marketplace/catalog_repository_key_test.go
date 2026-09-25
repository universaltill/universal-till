package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustCatalogKey is the white-box cache key for (locale, deviceArch).
func mustCatalogKey(t *testing.T, locale, deviceArch string) string {
	t.Helper()
	key, err := catalogKey(locale, deviceArch)
	if err != nil {
		t.Fatalf("catalogKey(%q, %q): %v", locale, deviceArch, err)
	}
	return key
}

// localeEchoServer answers every catalog page with one plugin named after
// the request's locale and arch, so a snapshot shows which request made it.
func localeEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		name := "plugin-" + q.Get("locale") + "-" + q.Get("arch")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"plugins": []map[string]any{{
			"listing_id":     name,
			"name":           name,
			"version":        "1.0.0",
			"artifact_url":   "http://example.com/plugin.tar.gz",
			"sha256":         "deadbeef",
			"canonical_type": "payment",
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func onlyPluginName(t *testing.T, snap *CatalogSnapshot) string {
	t.Helper()
	if snap == nil || len(snap.Plugins) != 1 {
		t.Fatalf("want exactly one plugin, got %+v", snap)
	}
	return snap.Plugins[0].Name
}

// ut-docs#2674: /plugins (UI locale, no arch) and /plugins/store (shop
// locale, device arch) used to share one cache slot, so each page's fetch
// replaced the other's filtered catalog — in memory and on disk.
func TestCatalogRepository_KeysDoNotOverwriteEachOther(t *testing.T) {
	srv := localeEchoServer(t)
	dir := t.TempDir()
	repo, err := NewCatalogRepository(testClient(t, srv.URL), dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	ctx := context.Background()

	// Store page first, then /plugins in a second locale.
	if _, _, err := repo.GetOrFetch(ctx, "en-US", "linux/amd64"); err != nil {
		t.Fatalf("GetOrFetch store key: %v", err)
	}
	if _, _, err := repo.GetOrFetch(ctx, "de", ""); err != nil {
		t.Fatalf("GetOrFetch plugins-page key: %v", err)
	}

	check := func(r *CatalogRepository, where string) {
		t.Helper()
		store, _, err := r.Get("en-US", "linux/amd64")
		if err != nil {
			t.Fatalf("%s: Get store key: %v", where, err)
		}
		if got := onlyPluginName(t, store); got != "plugin-en-US-linux/amd64" {
			t.Errorf("%s: store key serves %q — another key's fetch overwrote it", where, got)
		}
		page, _, err := r.Get("de", "")
		if err != nil {
			t.Fatalf("%s: Get plugins-page key: %v", where, err)
		}
		if got := onlyPluginName(t, page); got != "plugin-de-" {
			t.Errorf("%s: plugins-page key serves %q", where, got)
		}
	}
	check(repo, "memory")

	// A restarted till (fresh repo, same cache dir) reads each key back
	// from its own file.
	restarted, err := NewCatalogRepository(testClient(t, "http://127.0.0.1:1"), dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository (restart): %v", err)
	}
	check(restarted, "disk")
}

// Offline behaviour is unchanged per key: a stale per-key snapshot is served
// when the marketplace is unreachable, and never another key's.
func TestCatalogRepository_StalePerKeyCacheServedOffline(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewCatalogRepository(testClient(t, "http://127.0.0.1:1"), dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	old := time.Now().Add(-24 * time.Hour)
	for _, s := range []*CatalogSnapshot{
		{Plugins: []PluginSummary{{Name: "store-plugin"}}, FetchedAt: old, Locale: "en-US", DeviceArch: "linux/arm64"},
		{Plugins: []PluginSummary{{Name: "page-plugin"}}, FetchedAt: old, Locale: "fa", DeviceArch: ""},
	} {
		if err := repo.SeedSnapshot(s); err != nil {
			t.Fatalf("SeedSnapshot: %v", err)
		}
	}

	snap, stale, err := repo.GetOrFetch(context.Background(), "en-US", "linux/arm64")
	if err != nil || !stale {
		t.Fatalf("GetOrFetch offline: stale=%v err=%v; want the stale cache served", stale, err)
	}
	if got := onlyPluginName(t, snap); got != "store-plugin" {
		t.Errorf("store key served %q offline", got)
	}
	snap, stale, err = repo.GetOrFetch(context.Background(), "fa", "")
	if err != nil || !stale {
		t.Fatalf("GetOrFetch offline: stale=%v err=%v; want the stale cache served", stale, err)
	}
	if got := onlyPluginName(t, snap); got != "page-plugin" {
		t.Errorf("plugins-page key served %q offline", got)
	}

	// A key nothing was cached for has nothing to serve — it must not
	// borrow another key's catalog.
	if snap, _, err := repo.Get("en-US", ""); err == nil {
		t.Errorf("Get of an uncached key returned %+v; want an error", snap)
	}
}

// A till upgraded while offline still has only the pre-ut-docs#2674 single
// file. It keeps serving it for the one key it was written for.
func TestCatalogRepository_LegacySnapshotServesOnlyItsOwnKey(t *testing.T) {
	dir := t.TempDir()
	raw, err := json.Marshal(CatalogSnapshot{
		Plugins:    []PluginSummary{{Name: "legacy-plugin"}},
		FetchedAt:  time.Now(),
		Locale:     "en-US",
		DeviceArch: "linux/amd64",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacySnapshotFile), raw, 0o644); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}
	repo, err := NewCatalogRepository(nil, dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}

	snap, _, err := repo.Get("en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Get legacy key: %v", err)
	}
	if got := onlyPluginName(t, snap); got != "legacy-plugin" {
		t.Errorf("legacy key served %q", got)
	}
	if snap, _, err := repo.Get("de", ""); err == nil {
		t.Errorf("legacy snapshot served for a different key: %+v", snap)
	}
}

// The locale can come from ?lang=, and key parts become a file name, so a
// key that could escape the cache dir or collide is refused outright.
func TestCatalogRepository_RejectsUnsafeKeys(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewCatalogRepository(testClient(t, "http://127.0.0.1:1"), dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	for _, k := range [][2]string{
		{"../../etc", ""},
		{"en/US", ""},
		{"en.US", ""},
		{"en-US", "../x"},
		{"en-US", "linux/amd64/extra"},
		{"en-US", "linux+amd64"},
		{"a-very-long-locale-string-that-is-not-bcp47", ""},
	} {
		if _, _, err := repo.GetOrFetch(context.Background(), k[0], k[1]); err == nil {
			t.Errorf("GetOrFetch(%q, %q): want an error", k[0], k[1])
		}
		if _, _, err := repo.Get(k[0], k[1]); err == nil {
			t.Errorf("Get(%q, %q): want an error", k[0], k[1])
		}
		if err := repo.SeedSnapshot(&CatalogSnapshot{Locale: k[0], DeviceArch: k[1]}); err == nil {
			t.Errorf("SeedSnapshot(%q, %q): want an error", k[0], k[1])
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("unsafe keys wrote files: %v", entries)
	}

	// Valid keys that differ only in which part is empty get distinct files.
	names := map[string]bool{}
	for _, k := range [][2]string{{"", ""}, {"en", ""}, {"", "linux"}, {"en", "linux"}, {"linux", "en"}, {"en", "linux/amd64"}} {
		names[snapshotFileName(k[0], k[1])] = true
	}
	if len(names) != 6 {
		t.Errorf("distinct keys share a snapshot file: %v", names)
	}
}
