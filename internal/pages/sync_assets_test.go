package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// listItemAssets/safeAssetPath back the LAN primary→replica item-image
// sync surface (GET /api/sync/assets, /api/sync/assets/file). Item image
// uploads write to the stable per-user data dir (paths.Data — fixed
// 2026-07-29 after uploads were found to be lost on every app
// self-update, see catalog/handlers.go). This test guards that the sync
// manifest looks in the SAME place uploads actually land — if it doesn't,
// listItemAssets silently returns an empty manifest forever and replica
// tills never receive item photos, with no error anywhere to notice it by.

func TestListItemAssets_FindsFilesInTheStableDataDir(t *testing.T) {
	orig := paths.DataDir()
	tmp := t.TempDir()
	paths.Init(tmp)
	t.Cleanup(func() { paths.Init(orig) })

	itemDir := filepath.Join(tmp, "public", "assets", "items", "itm001")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "thumb.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := listItemAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the uploaded file to appear in the sync manifest, got %+v", entries)
	}
	if entries[0].Path != "itm001/thumb.png" {
		t.Fatalf("expected path itm001/thumb.png, got %q", entries[0].Path)
	}
	if entries[0].Size != int64(len("fake-png-bytes")) {
		t.Fatalf("expected the real file size, got %d", entries[0].Size)
	}
}

func TestListItemAssets_EmptyTreeIsNotAnError(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir()) // fresh dir, no items/ subtree at all
	t.Cleanup(func() { paths.Init(orig) })

	entries, err := listItemAssets()
	if err != nil {
		t.Fatalf("expected a missing tree to mean 'no images yet', not an error, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %+v", entries)
	}
}

func TestSafeAssetPath(t *testing.T) {
	orig := paths.DataDir()
	tmp := t.TempDir()
	paths.Init(tmp)
	t.Cleanup(func() { paths.Init(orig) })

	want := filepath.Join(tmp, "public", "assets", "items", "itm001", "thumb.png")
	got, ok := safeAssetPath("itm001/thumb.png")
	if !ok || got != want {
		t.Fatalf("expected (%q, true), got (%q, %v)", want, got, ok)
	}

	// Path traversal and absolute paths must be rejected outright — this
	// path is reachable from any enrolled replica's bearer token, so a
	// compromised or buggy replica must not be able to read arbitrary
	// files off the primary's disk.
	for _, bad := range []string{
		"", "../../../etc/passwd", "/etc/passwd", "itm001/../../../etc/passwd",
		"itm001\\..\\..\\secret", "..",
	} {
		if _, ok := safeAssetPath(bad); ok {
			t.Fatalf("expected safeAssetPath(%q) to be rejected, got ok=true", bad)
		}
	}
}

// --- registerSyncAssets: the PRIMARY-side manifest + file surface ---

// newAssetsPrimaryMux stands up the primary-side asset endpoints backed by a
// single enrolled replica bearer ("tok-assets"), reading files from the
// (test-scoped) paths.Data tree.
func newAssetsPrimaryMux(t *testing.T) *http.ServeMux {
	t.Helper()
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	if _, err := data.NewTillsRepo(db).InsertTill(context.Background(), "Replica 1", hashBearer("tok-assets")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	mux := http.NewServeMux()
	registerSyncAssets(mux, &common.Deps{Db: db})
	return mux
}

func TestRegisterSyncAssets_ManifestAndFileServing(t *testing.T) {
	orig := paths.DataDir()
	tmp := t.TempDir()
	paths.Init(tmp)
	t.Cleanup(func() { paths.Init(orig) })

	itemDir := filepath.Join(tmp, "public", "assets", "items", "itm001")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("fake-png-bytes")
	if err := os.WriteFile(filepath.Join(itemDir, "thumb.png"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	mux := newAssetsPrimaryMux(t)

	// Unauthenticated manifest + file are both rejected — this surface is
	// reachable off the LAN and must never serve without a valid bearer.
	for _, path := range []string{"/api/sync/assets", "/api/sync/assets/file?path=itm001/thumb.png"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthenticated GET %s, got %d", path, rec.Code)
		}
	}

	// Authenticated manifest lists the uploaded file with its real size.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/assets", nil)
	req.Header.Set("Authorization", "Bearer tok-assets")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on the manifest, got %d: %s", rec.Code, rec.Body.String())
	}
	var manifest struct {
		Data []assetEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Data) != 1 || manifest.Data[0].Path != "itm001/thumb.png" || manifest.Data[0].Size != int64(len(want)) {
		t.Fatalf("unexpected manifest: %+v", manifest.Data)
	}

	// Authenticated file fetch returns the exact bytes.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/assets/file?path=itm001/thumb.png", nil)
	req.Header.Set("Authorization", "Bearer tok-assets")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 serving the file, got %d", rec.Code)
	}
	if rec.Body.String() != string(want) {
		t.Fatalf("expected the file bytes, got %q", rec.Body.String())
	}

	// A traversal path is rejected at the handler (400) even with a valid
	// bearer — auth alone must not grant arbitrary file reads.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/assets/file?path=../../../etc/passwd", nil)
	req.Header.Set("Authorization", "Bearer tok-assets")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a traversal path, got %d", rec.Code)
	}
}

// --- syncItemAssets: the REPLICA-side pull/download logic ---

// stubAssetsPrimary is a hand-rolled primary that serves a fixed manifest and
// file bytes, independent of paths.Data (so the replica's download target can
// be a separate tree). It counts file fetches to prove skip-when-unchanged.
type stubAssetsPrimary struct {
	server     *httptest.Server
	fileBytes  []byte
	fileServed int
	fileStatus int // override; 0 => 200
}

func newStubAssetsPrimary(t *testing.T, manifest []assetEntry, fileBytes []byte) *stubAssetsPrimary {
	t.Helper()
	s := &stubAssetsPrimary{fileBytes: fileBytes}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync/assets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": manifest, "error": nil})
	})
	mux.HandleFunc("GET /api/sync/assets/file", func(w http.ResponseWriter, r *http.Request) {
		s.fileServed++
		if s.fileStatus != 0 {
			w.WriteHeader(s.fileStatus)
			return
		}
		_, _ = w.Write(s.fileBytes)
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func replicaAssetPath(root, rel string) string {
	return filepath.Join(root, "public", "assets", "items", filepath.FromSlash(rel))
}

func TestSyncItemAssets_DownloadsMissingThenSkipsMatching(t *testing.T) {
	orig := paths.DataDir()
	replicaRoot := t.TempDir()
	paths.Init(replicaRoot)
	t.Cleanup(func() { paths.Init(orig) })

	want := []byte("PNG-abc-123")
	primary := newStubAssetsPrimary(t,
		[]assetEntry{{Path: "itm001/thumb.png", Size: int64(len(want))}}, want)

	client := primary.server.Client()
	syncItemAssets(context.Background(), client, primary.server.URL, "bearer")

	got, err := os.ReadFile(replicaAssetPath(replicaRoot, "itm001/thumb.png"))
	if err != nil {
		t.Fatalf("expected the missing file downloaded to the replica, got %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("expected downloaded bytes %q, got %q", want, got)
	}
	if primary.fileServed != 1 {
		t.Fatalf("expected exactly one file fetch, got %d", primary.fileServed)
	}

	// Second tick: the local file already matches the manifest size, so it is
	// skipped — no second fetch.
	syncItemAssets(context.Background(), client, primary.server.URL, "bearer")
	if primary.fileServed != 1 {
		t.Fatalf("expected the matching-size file to be skipped on the second tick, got %d fetches", primary.fileServed)
	}
}

func TestSyncItemAssets_ReDownloadsWhenSizeChanged(t *testing.T) {
	orig := paths.DataDir()
	replicaRoot := t.TempDir()
	paths.Init(replicaRoot)
	t.Cleanup(func() { paths.Init(orig) })

	// A stale local file whose size differs from the manifest must be replaced.
	local := replicaAssetPath(replicaRoot, "itm001/thumb.png")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	want := []byte("brand-new-larger-bytes")
	primary := newStubAssetsPrimary(t,
		[]assetEntry{{Path: "itm001/thumb.png", Size: int64(len(want))}}, want)

	syncItemAssets(context.Background(), primary.server.Client(), primary.server.URL, "bearer")

	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("expected the changed file re-downloaded, got %q", got)
	}
}

func TestSyncItemAssets_PrimaryManifestErrorIsNonFatal(t *testing.T) {
	orig := paths.DataDir()
	replicaRoot := t.TempDir()
	paths.Init(replicaRoot)
	t.Cleanup(func() { paths.Init(orig) })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync/assets", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Must return cleanly (best-effort, non-blocking) and write nothing.
	syncItemAssets(context.Background(), srv.Client(), srv.URL, "bearer")
	if entries, _ := os.ReadDir(filepath.Join(replicaRoot, "public", "assets", "items")); len(entries) != 0 {
		t.Fatalf("expected no files written when the manifest errors, got %d", len(entries))
	}
}

func TestSyncItemAssets_FileFetch404IsSkipped(t *testing.T) {
	orig := paths.DataDir()
	replicaRoot := t.TempDir()
	paths.Init(replicaRoot)
	t.Cleanup(func() { paths.Init(orig) })

	primary := newStubAssetsPrimary(t,
		[]assetEntry{{Path: "itm001/thumb.png", Size: 10}}, nil)
	primary.fileStatus = http.StatusNotFound

	syncItemAssets(context.Background(), primary.server.Client(), primary.server.URL, "bearer")
	if _, err := os.Stat(replicaAssetPath(replicaRoot, "itm001/thumb.png")); err == nil {
		t.Fatalf("expected no file written when the primary 404s the fetch")
	}
}

func TestSyncItemAssets_RejectsTraversalPathsFromTheWire(t *testing.T) {
	orig := paths.DataDir()
	replicaRoot := t.TempDir()
	paths.Init(replicaRoot)
	t.Cleanup(func() { paths.Init(orig) })

	// A malicious/buggy primary advertises a traversal path — safeAssetPath
	// must drop it, so nothing is fetched and nothing escapes the asset root.
	primary := newStubAssetsPrimary(t,
		[]assetEntry{{Path: "../../../etc/evil", Size: 5}}, []byte("evil!"))

	syncItemAssets(context.Background(), primary.server.Client(), primary.server.URL, "bearer")
	if primary.fileServed != 0 {
		t.Fatalf("expected a traversal manifest entry to be dropped before any fetch, got %d", primary.fileServed)
	}
}
