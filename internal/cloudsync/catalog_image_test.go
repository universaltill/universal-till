package cloudsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// set_catalog_image (ut-docs reference/manage-shop-catalog-api.md §3.9,
// ut-docs#3139): the payload decode, dispatch, the image fetch from the
// cloud, and the snapshot's image_sha256.

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var goodSHA = strings.Repeat("ab", 32)

func TestApplySetCatalogImage_Decode(t *testing.T) {
	if status, msg := apply(context.Background(), directive{Type: "set_catalog_image", Payload: map[string]any{"entity": "item", "id": "i1", "clear": true}}, Hooks{}); status != "failed" || msg != "set_catalog_image is not supported on this till" {
		t.Fatalf("nil hook: %q %q", status, msg)
	}
	var calls int
	var got CatalogImage
	hooks := Hooks{SetCatalogImage: func(ctx context.Context, img CatalogImage) (string, error) {
		calls++
		got = img
		return "image set on item Apple", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"id": "i1", "sha256": goodSHA}, "missing entity"},
		{map[string]any{"entity": "variant", "id": "i1", "sha256": goodSHA}, "unknown entity variant"},
		{map[string]any{"entity": 5.0, "id": "i1", "sha256": goodSHA}, "bad entity"},
		{map[string]any{"entity": "item", "sha256": goodSHA}, "missing id"},
		{map[string]any{"entity": "item", "id": "  ", "sha256": goodSHA}, "missing id"},
		{map[string]any{"entity": "item", "id": 7.0, "sha256": goodSHA}, "bad id"},
		{map[string]any{"entity": "item", "id": "i1"}, "missing sha256"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": "xyz"}, "bad sha256"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": strings.ToUpper(goodSHA)}, "bad sha256"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": goodSHA + "00"}, "bad sha256"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": 1.0}, "bad sha256"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": goodSHA, "clear": true}, "sha256 and clear cannot both be set"},
		{map[string]any{"entity": "item", "id": "i1", "clear": "yes"}, "bad clear"},
		{map[string]any{"entity": "item", "id": "i1", "clear": 1.0}, "bad clear"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": goodSHA, "size": "big"}, "bad size"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": goodSHA, "size": -1.0}, "bad size"},
		{map[string]any{"entity": "item", "id": "i1", "sha256": goodSHA, "size": float64(catalogImageMaxBytes + 1)}, "bad size"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "set_catalog_image", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want failed %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for refused payloads", calls)
	}

	status, msg := apply(context.Background(), directive{Type: "set_catalog_image", Payload: map[string]any{"entity": "item", "id": " i1 ", "sha256": goodSHA, "size": 1234.0}}, hooks)
	if status != "applied" || msg != "image set on item Apple" {
		t.Fatalf("set: %q %q", status, msg)
	}
	if got.Entity != "item" || got.ID != "i1" || got.SHA256 != goodSHA || got.Size != 1234 || got.Clear || got.Fetch == nil {
		t.Fatalf("set decoded as %+v", got)
	}
	// apply alone (no tick) has no cloud to fetch from: Fetch fails
	// cleanly rather than panicking.
	if _, err := got.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch without a cloud must fail")
	}

	status, _ = apply(context.Background(), directive{Type: "set_catalog_image", Payload: map[string]any{"entity": "category", "id": "c1", "clear": true, "sha256": ""}}, hooks)
	if status != "applied" || got.Entity != "category" || got.ID != "c1" || !got.Clear || got.SHA256 != "" || got.Fetch != nil {
		t.Fatalf("clear decoded as %q %+v", status, got)
	}
	// clear:false with a sha is a set.
	status, _ = apply(context.Background(), directive{Type: "set_catalog_image", Payload: map[string]any{"entity": "category", "id": "c1", "clear": false, "sha256": goodSHA}}, hooks)
	if status != "applied" || got.Clear || got.SHA256 != goodSHA {
		t.Fatalf("clear:false + sha decoded as %q %+v", status, got)
	}
}

// Main-till only (a satellite leaves it pending) and a catalog type (an
// applied one re-pushes the snapshot in the same tick).
func TestSetCatalogImageIsMainTillOnlyCatalogType(t *testing.T) {
	if !mainTillOnlyTypes["set_catalog_image"] {
		t.Error("set_catalog_image not in mainTillOnlyTypes")
	}
	if !catalogTypes["set_catalog_image"] {
		t.Error("set_catalog_image not in catalogTypes")
	}
}

func TestTickSatelliteSkipsSetCatalogImage(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "d1", "type": "set_catalog_image", "payload": map[string]any{"entity": "item", "id": "i1", "clear": true}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	ran := 0
	hooks := Hooks{SetCatalogImage: func(context.Context, CatalogImage) (string, error) { ran++; return "", nil }}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	if ran != 0 || len(cloud.results) != 0 {
		t.Fatalf("satellite: ran=%d results=%v", ran, cloud.results)
	}
}

// The fetch: GET /v1/stores/catalog-images/{sha} under the endpoint base
// (which carries /api), with the till's store Bearer credential; the body
// must hash to the sha, is capped, and any failure is an owner-readable,
// retryable error.
func TestFetchCatalogImage(t *testing.T) {
	good := []byte("\x89PNG pretend image bytes")
	goodHash := hexSum(good)
	big := bytes.Repeat([]byte{1}, catalogImageMaxBytes+1)
	bigHash := hexSum(big)
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		switch strings.TrimPrefix(r.URL.Path, "/api/v1/stores/catalog-images/") {
		case goodHash:
			_, _ = w.Write(good)
		case goodSHA: // served bytes that don't match the requested sha
			_, _ = w.Write(good)
		case bigHash:
			_, _ = w.Write(big)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"image_not_found"}}`))
		}
	}))
	defer srv.Close()
	cfg := testCfg(srv.URL + "/api")
	ctx := context.Background()

	body, err := fetchCatalogImage(ctx, cfg, goodHash)
	if err != nil || !bytes.Equal(body, good) {
		t.Fatalf("good fetch: %v %q", err, body)
	}
	if gotAuth != "Bearer tok-1" || gotPath != "/api/v1/stores/catalog-images/"+goodHash {
		t.Fatalf("request: auth %q path %q", gotAuth, gotPath)
	}
	if _, err := fetchCatalogImage(ctx, cfg, goodSHA); err == nil || err.Error() != "image checksum mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := fetchCatalogImage(ctx, cfg, strings.Repeat("cd", 32)); err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "save the image again") {
		t.Fatalf("404: %v", err)
	}
	if _, err := fetchCatalogImage(ctx, cfg, bigHash); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize: %v", err)
	}
	// Unreachable cloud: a transport failure, not a hang — tick leaves the
	// directive pending on errImageFetchUnreachable and retries next tick.
	dead := testCfg("http://127.0.0.1:1/api")
	if _, err := fetchCatalogImage(ctx, dead, goodHash); !errors.Is(err, errImageFetchUnreachable) {
		t.Fatalf("network: %v", err)
	}
	// Not registered: nothing to fetch from.
	if _, err := fetchCatalogImage(ctx, testCfg(""), goodHash); err == nil {
		t.Fatal("unregistered fetch must fail")
	}
}

// A stalled cloud can't hold the tick past the fetch's own deadline.
func TestFetchCatalogImage_Bounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	orig := catalogImageFetchTimeout
	catalogImageFetchTimeout = 50 * time.Millisecond
	defer func() { catalogImageFetchTimeout = orig }()
	start := time.Now()
	_, err := fetchCatalogImage(context.Background(), testCfg(srv.URL), goodSHA)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("stalled fetch: %v after %s", err, time.Since(start))
	}
}

// In a tick, the hook's Fetch reaches the cloud with the store credential.
func TestTickSetCatalogImageFetchesFromCloud(t *testing.T) {
	img := []byte("png-bytes")
	sum := hexSum(img)
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "d1", "type": "set_catalog_image", "payload": map[string]any{"entity": "item", "id": "i1", "sha256": sum, "size": float64(len(img))}},
	}}
	mux := http.NewServeMux()
	mux.Handle("/", cloud.handler())
	mux.HandleFunc("/v1/stores/catalog-images/"+sum, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(img)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var fetched []byte
	hooks := Hooks{SetCatalogImage: func(ctx context.Context, ci CatalogImage) (string, error) {
		b, err := ci.Fetch(ctx)
		fetched = b
		return "image set on item Apple", err
	}}
	if err := Tick(context.Background(), testCfg(srv.URL), testDB(t), hooks); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fetched, img) {
		t.Fatalf("fetched %q", fetched)
	}
	if len(cloud.results) != 1 || cloud.results[0]["status"] != "applied" {
		t.Fatalf("results = %v", cloud.results)
	}
}

// §3.9 rule 5: the snapshot carries image_sha256 per item — the SHA-256 of
// the PNG the till serves for an uploaded photo, "" for none or a built-in
// icon — and a replaced photo changes the snapshot (hash gate).
func TestSnapshotImageSHA256(t *testing.T) {
	origData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origData) })
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-photo", SKU: "S1", Name: "A photo", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-icon", SKU: "S2", Name: "B icon", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-none", SKU: "S3", Name: "C none", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-gone", SKU: "S4", Name: "D missing file", BasePrice: 100, IsActive: true})
	testsupport.SeedImage(t, db, "img-1", "it-photo", "/public/assets/items/it-photo/thumb.png")
	testsupport.SeedImage(t, db, "img-2", "it-icon", "/public/icons/categories/coffee.png")
	testsupport.SeedImage(t, db, "img-3", "it-gone", "/public/assets/items/it-gone/thumb.png")
	file := paths.Data("public", "assets", "items", "it-photo", "thumb.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	photo := []byte("first photo")
	if err := os.WriteFile(file, photo, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), db); err != nil {
		t.Fatal(err)
	}
	shas := func(snap map[string]any) map[string]any {
		out := map[string]any{}
		for _, it := range snap["items"].([]any) {
			m := it.(map[string]any)
			v, ok := m["image_sha256"]
			if !ok {
				t.Fatalf("item %v has no image_sha256", m["id"])
			}
			out[m["id"].(string)] = v
		}
		return out
	}
	got := shas(cloud.snapshots[0])
	if got["it-photo"] != hexSum(photo) || got["it-icon"] != "" || got["it-none"] != "" || got["it-gone"] != "" {
		t.Fatalf("image_sha256 = %v", got)
	}
	// Unchanged: no second push.
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), db); err != nil || len(cloud.snapshots) != 1 {
		t.Fatalf("unchanged re-push: %v, %d snapshots", err, len(cloud.snapshots))
	}
	// A replaced photo (same size, new bytes, new mtime) changes the snapshot.
	photo2 := []byte("other photo")
	if err := os.WriteFile(file, photo2, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), db); err != nil || len(cloud.snapshots) != 2 {
		t.Fatalf("changed photo: %v, %d snapshots", err, len(cloud.snapshots))
	}
	if got := shas(cloud.snapshots[1]); got["it-photo"] != hexSum(photo2) {
		t.Fatalf("after replace image_sha256 = %v", got["it-photo"])
	}
}

func TestServedImageSHA256(t *testing.T) {
	origData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origData) })
	file := paths.Data("public", "assets", "categories", "c1", "thumb.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("cat"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want string }{
		{"/public/assets/categories/c1/thumb.png", hexSum([]byte("cat"))},
		{"", ""},
		{"/public/icons/categories/coffee.png", ""},
		{"/public/assets/categories/../../../etc/passwd", ""},
		{"/public/assets/categories/c2/thumb.png", ""},
	} {
		if got := ServedImageSHA256(c.path); got != c.want {
			t.Errorf("ServedImageSHA256(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// Review 2026-09-30 (ut-docs#3139): a stalled image route costs a tick one
// fetch timeout, not one per directive, and leaves every affected
// set_catalog_image pending (no result post) for the next tick instead of
// failing them all. A cloud answer (404) is still a real failure.
func TestTickStalledImageRouteLeavesDirectivesPending(t *testing.T) {
	img := []byte("png-bytes")
	sum := hexSum(img)
	other := hexSum([]byte("other"))
	missing := hexSum([]byte("missing"))
	set := func(id, sha string) map[string]any {
		return map[string]any{"id": id, "type": "set_catalog_image", "payload": map[string]any{"entity": "item", "id": "i-" + id, "sha256": sha, "size": float64(9)}}
	}
	release := make(chan struct{})
	var stalled, notFound int
	var mu sync.Mutex
	mux := http.NewServeMux()
	cloud := &fakeCloud{directives: []map[string]any{set("d404", missing), set("d1", sum), set("d2", other), set("d3", sum)}}
	mux.Handle("/", cloud.handler())
	mux.HandleFunc("/v1/stores/catalog-images/"+missing, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		notFound++
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	})
	stall := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stalled++
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	mux.HandleFunc("/v1/stores/catalog-images/"+sum, stall)
	mux.HandleFunc("/v1/stores/catalog-images/"+other, stall)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)
	orig := catalogImageFetchTimeout
	catalogImageFetchTimeout = 50 * time.Millisecond
	defer func() { catalogImageFetchTimeout = orig }()

	hooks := Hooks{SetCatalogImage: func(ctx context.Context, ci CatalogImage) (string, error) {
		_, err := ci.Fetch(ctx)
		return "image set", err
	}}
	if err := Tick(context.Background(), testCfg(srv.URL), testDB(t), hooks); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if stalled != 1 || notFound != 1 {
		t.Fatalf("fetch attempts: stalled=%d (want 1), 404=%d (want 1)", stalled, notFound)
	}
	if len(cloud.results) != 1 || cloud.results[0]["directive_id"] != "d404" || cloud.results[0]["status"] != "failed" {
		t.Fatalf("results = %v, want only d404 failed", cloud.results)
	}
}
