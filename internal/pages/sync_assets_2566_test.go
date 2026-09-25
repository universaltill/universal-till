package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#2566: uploaded category photos (#2500) lived only on the main
// till — admin sync carried the image_path, so a joined till showed no
// photo. The categories scope of the photo sync surface closes that gap;
// these tests pin it end to end (real primary handlers, real replica pull)
// plus the size cap and the same-size-replacement change detector.

// withDataDir points paths.Data at a fresh dir for the test.
func withDataDir(t *testing.T) string {
	t.Helper()
	orig := paths.DataDir()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init(orig) })
	return dir
}

func writeAsset(t *testing.T, root, scope, rel string, b []byte) string {
	t.Helper()
	p := filepath.Join(root, "public", "assets", scope, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// runPull runs the real primary mux over primaryRoot and the real replica
// pull into replicaRoot. Both sides read the one paths.Data global, so the
// wrapping handler switches it to the primary per request and back after.
// It returns how many file downloads the pull made.
func runPull(t *testing.T, primaryRoot, replicaRoot string) int {
	t.Helper()
	mux := newAssetsPrimaryMux(t)
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/file") {
			fetches++
		}
		paths.Init(primaryRoot)
		defer paths.Init(replicaRoot)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	paths.Init(replicaRoot)
	syncAssets(context.Background(), srv.Client(), srv.URL, "tok-assets")
	return fetches
}

func TestSyncAssets_CategoryPhotoReachesTheReplica(t *testing.T) {
	primaryRoot := withDataDir(t)
	replicaRoot := t.TempDir()
	want := []byte("category-photo-bytes")
	writeAsset(t, primaryRoot, "categories", "cat-drinks/thumb.png", want)
	writeAsset(t, primaryRoot, "items", "itm001/thumb.png", []byte("item-photo"))

	runPull(t, primaryRoot, replicaRoot)

	got, err := os.ReadFile(filepath.Join(replicaRoot, "public", "assets", "categories", "cat-drinks", "thumb.png"))
	if err != nil {
		t.Fatalf("expected the category photo downloaded into the replica's categories tree, got %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if _, err := os.Stat(filepath.Join(replicaRoot, "public", "assets", "items", "itm001", "thumb.png")); err != nil {
		t.Fatalf("expected item photos to keep syncing alongside, got %v", err)
	}
	// Scopes never cross: nothing from categories lands under items.
	if _, err := os.Stat(filepath.Join(replicaRoot, "public", "assets", "items", "cat-drinks")); err == nil {
		t.Fatalf("a category photo leaked into the items tree")
	}
}

func TestSyncAssets_CategoryEndpointsRequireTheBearer(t *testing.T) {
	root := withDataDir(t)
	writeAsset(t, root, "categories", "cat1/thumb.png", []byte("x"))
	mux := newAssetsPrimaryMux(t)
	for _, p := range []string{"/api/sync/assets/categories", "/api/sync/assets/categories/file?path=cat1/thumb.png"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthenticated GET %s, got %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/assets/categories/file?path=../items/itm001/thumb.png", nil)
	req.Header.Set("Authorization", "Bearer tok-assets")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a traversal out of the categories scope, got %d", rec.Code)
	}
}

func TestSyncAssets_SameSizeReplacementReDownloads(t *testing.T) {
	primaryRoot := withDataDir(t)
	replicaRoot := t.TempDir()
	p := writeAsset(t, primaryRoot, "categories", "cat1/thumb.png", []byte("AAAA"))
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	runPull(t, primaryRoot, replicaRoot)

	// The owner replaces the photo with one of the exact same size.
	if err := os.WriteFile(p, []byte("BBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPull(t, primaryRoot, replicaRoot)

	got, _ := os.ReadFile(filepath.Join(replicaRoot, "public", "assets", "categories", "cat1", "thumb.png"))
	if string(got) != "BBBB" {
		t.Fatalf("expected the same-size replacement re-downloaded (mtime detector), got %q", got)
	}
}

func TestSyncAssets_UnchangedFileIsNotFetchedAgain(t *testing.T) {
	primaryRoot := withDataDir(t)
	replicaRoot := t.TempDir()
	writeAsset(t, primaryRoot, "categories", "cat1/thumb.png", []byte("AAAA"))
	if n := runPull(t, primaryRoot, replicaRoot); n != 1 {
		t.Fatalf("expected one download on the first tick, got %d", n)
	}
	if n := runPull(t, primaryRoot, replicaRoot); n != 0 {
		t.Fatalf("expected an unchanged photo not fetched again, got %d downloads", n)
	}

	// Stamping the primary's mtime on the replica copy is what keeps the
	// next tick from re-downloading it.
	pst, _ := os.Stat(filepath.Join(primaryRoot, "public", "assets", "categories", "cat1", "thumb.png"))
	rst, err := os.Stat(filepath.Join(replicaRoot, "public", "assets", "categories", "cat1", "thumb.png"))
	if err != nil {
		t.Fatal(err)
	}
	if rst.ModTime().Unix() != pst.ModTime().Unix() {
		t.Fatalf("expected the replica copy stamped with the primary's mtime, got %v vs %v", rst.ModTime(), pst.ModTime())
	}
}

func TestSyncAssets_OversizedFilesNeverSync(t *testing.T) {
	root := withDataDir(t)
	big := make([]byte, syncAssetMaxBytes+1)
	writeAsset(t, root, "categories", "cat1/thumb.png", big)
	list, err := categoryAssetScope.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected a file over the cap left out of the manifest, got %+v", list)
	}
}

// A primary (or anything answering as one) that advertises a small file but
// streams more — or advertises one over the cap — must never get more than
// the cap written to the replica's disk.
func TestSyncAssets_ReplicaEnforcesCapAndManifestSize(t *testing.T) {
	replicaRoot := withDataDir(t)
	served := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync/assets", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []assetEntry{
			{Path: "liar/thumb.png", Size: 4},
			{Path: "huge/thumb.png", Size: syncAssetMaxBytes + 1},
		}})
	})
	mux.HandleFunc("GET /api/sync/assets/file", func(w http.ResponseWriter, r *http.Request) {
		served++
		_, _ = w.Write(make([]byte, 64)) // more than the advertised 4 bytes
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	syncAssets(context.Background(), srv.Client(), srv.URL, "b")

	if served != 1 {
		t.Fatalf("expected the over-cap entry skipped before any fetch (1 fetch for the liar), got %d", served)
	}
	for _, rel := range []string{"liar/thumb.png", "liar/thumb.png" + syncTmpSuffix, "huge/thumb.png"} {
		if _, err := os.Stat(filepath.Join(replicaRoot, "public", "assets", "items", rel)); err == nil {
			t.Fatalf("expected nothing written for %s", rel)
		}
	}
}

// A replica on this version pulling from an older primary (no categories
// endpoints → 404) keeps syncing items and writes nothing for categories.
func TestSyncAssets_OlderPrimaryWithoutCategoriesScope(t *testing.T) {
	replicaRoot := withDataDir(t)
	want := []byte("item")
	primary := newStubAssetsPrimary(t, []assetEntry{{Path: "itm001/thumb.png", Size: int64(len(want))}}, want)

	syncAssets(context.Background(), primary.server.Client(), primary.server.URL, "b")

	if _, err := os.Stat(replicaAssetPath(replicaRoot, "itm001/thumb.png")); err != nil {
		t.Fatalf("expected items to still sync from an older primary, got %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(replicaRoot, "public", "assets", "categories")); len(entries) != 0 {
		t.Fatalf("expected no categories written from an older primary, got %d", len(entries))
	}
}

func TestSameMod_ToleratesFATTwoSecondRounding(t *testing.T) {
	for _, c := range []struct {
		local, remote int64
		want          bool
	}{
		{100, 0, true}, {100, 100, true}, {100, 101, true}, {101, 99, true},
		{100, 103, false}, {97, 100, false},
	} {
		if got := sameMod(c.local, c.remote); got != c.want {
			t.Errorf("sameMod(%d, %d) = %v, want %v", c.local, c.remote, got, c.want)
		}
	}
}

func TestSafePath_RefusesDriveRelativePaths(t *testing.T) {
	withDataDir(t)
	for _, bad := range []string{"C:x", "c:thumb.png", "cat1/C:x"} {
		if _, ok := categoryAssetScope.safePath(bad); ok {
			t.Fatalf("expected %q refused", bad)
		}
	}
}
