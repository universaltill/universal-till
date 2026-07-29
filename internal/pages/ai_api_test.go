package pages

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// writeTestPNG writes a tiny valid PNG so image.Decode succeeds.
func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadReferenceImages_FindsCatalogThumbInTheStableDataDir is a
// regression test for the same bug class fixed earlier this session in
// sync_assets.go (batch 11, "LAN replica image sync silently dead since
// the upload-path fix"): item images are uploaded to paths.Data("public",
// "assets", "items", ...) (internal/pages/catalog/handlers.go), but
// ai_api.go's itemAssetDir constant pointed at the OLD cwd-relative
// "web/public/assets/items" path. Once the app runs from its installed
// data directory (not the repo checkout), the camera-identify feature
// silently found ZERO reference images for every item, degrading
// recognition quality with no visible error.
func TestLoadReferenceImages_FindsCatalogThumbInTheStableDataDir(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })

	writeTestPNG(t, paths.Data("public", "assets", "items", "itm1", "thumb.png"))

	refs := loadReferenceImages([]ai.CatalogItem{{ID: "itm1", SKU: "ABC", Name: "Apple"}})
	if len(refs) != 1 {
		t.Fatalf("expected 1 reference image found in the stable data dir, got %d", len(refs))
	}
	if refs[0].ItemID != "itm1" || len(refs[0].Data) == 0 {
		t.Fatalf("expected a populated reference image for itm1, got %+v (data len %d)", refs[0], len(refs[0].Data))
	}
}

func TestLoadReferenceImages_MissingImagesAreSkippedNotErrored(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir()) // fresh dir, no items/ subtree at all
	t.Cleanup(func() { paths.Init(orig) })

	refs := loadReferenceImages([]ai.CatalogItem{{ID: "itm-no-image"}})
	if len(refs) != 0 {
		t.Fatalf("expected no reference images for an item with no files on disk, got %d", len(refs))
	}
}

func TestLoadRefJPEGDownscales(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thumb.png")
	src := image.NewRGBA(image.Rect(0, 0, 512, 384))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	data, ok := loadRefJPEG(path)
	if !ok {
		t.Fatal("expected re-encode to succeed")
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not JPEG: %v", err)
	}
	if w := img.Bounds().Dx(); w != 160 {
		t.Fatalf("long edge = %d, want 160", w)
	}
}

func TestPruneAIRefsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	names := []string{"100.jpg", "200.jpg", "300.jpg", "400.jpg", "500.jpg", "600.jpg", "700.jpg"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruneAIRefs(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != maxAIRefsPerItem {
		t.Fatalf("kept %d refs, want %d", len(entries), maxAIRefsPerItem)
	}
	if _, err := os.Stat(filepath.Join(dir, "700.jpg")); err != nil {
		t.Fatal("newest ref must survive pruning")
	}
	if _, err := os.Stat(filepath.Join(dir, "100.jpg")); !os.IsNotExist(err) {
		t.Fatal("oldest ref must be pruned")
	}
}

func newAIAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *sql.DB) {
	t.Helper()
	chdirRoot(t)
	origDataDir := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origDataDir) })

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerAIAPI(mux, dp)
	return mux, dp, db
}

// fakeIdentifyServer mimics the minimal Ollama /api/chat response shape for
// identify, modeled on internal/ai/ai_test.go's TestOllamaIdentifyAndHallucinationFilter.
func fakeIdentifyServer(t *testing.T, result ai.IdentifyResult) *ai.Service {
	t.Helper()
	content, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": string(content)},
		})
	}))
	t.Cleanup(srv.Close)
	return ai.New(ai.Config{Provider: "ollama", Endpoint: srv.URL, Model: "test-model"})
}

// multipartPhotoRequest builds a real multipart/form-data POST with a photo
// field (and any extra plain fields), matching what a browser upload sends.
func multipartPhotoRequest(t *testing.T, path string, photo []byte, extra map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if photo != nil {
		fw, err := w.CreateFormFile("photo", "photo.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(photo); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range extra {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIdentifyAPI_NotConfiguredReturns404(t *testing.T) {
	mux, _, _ := newAIAPITestDeps(t)
	req := multipartPhotoRequest(t, "/api/pos/identify", testPNGBytes(t), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when AI identify isn't configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIdentifyAPI_RejectsMissingOrInvalidPhoto(t *testing.T) {
	mux, dp, _ := newAIAPITestDeps(t)
	dp.AI = fakeIdentifyServer(t, ai.IdentifyResult{})

	req := multipartPhotoRequest(t, "/api/pos/identify", nil, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with no photo field, got %d: %s", rec.Code, rec.Body.String())
	}

	req = multipartPhotoRequest(t, "/api/pos/identify", []byte("not an image"), nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-image photo, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIdentifyAPI_SuccessReturnsMatchesWithPriceAndThumbAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, db := newAIAPITestDeps(t)
	// itm1/ABC/£1.00 is seeded by seedForPages.
	writeTestPNG(t, paths.Data("public", "assets", "items", "itm1", "thumb.png"))
	dp.AI = fakeIdentifyServer(t, ai.IdentifyResult{
		SuggestedName: "Apple",
		// A hallucinated id outside the requested catalog (only itm1 was
		// sent) alongside the real match -- internal/ai.Service.Identify
		// filters these against the requested item list; this test's
		// "exactly one match" assertion below would fail if that filter
		// were ever removed, not just accidentally pass with one match.
		Matches: []ai.Candidate{
			{ItemID: "itm1", Confidence: "high"},
			{ItemID: "made-up-item-id", Confidence: "low"},
		},
	})

	req := multipartPhotoRequest(t, "/api/pos/identify", testPNGBytes(t), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Data struct {
			Matches []struct {
				ItemID       string `json:"item_id"`
				SKU          string `json:"sku"`
				PriceDisplay string `json:"price_display"`
				ThumbURL     string `json:"thumb_url"`
			} `json:"matches"`
			SuggestedName string `json:"suggested_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	if len(out.Data.Matches) != 1 {
		t.Fatalf("expected exactly one match, got %+v", out.Data)
	}
	m := out.Data.Matches[0]
	if m.ItemID != "itm1" || m.SKU != "ABC" {
		t.Fatalf("expected itm1/ABC matched with catalog metadata, got %+v", m)
	}
	if m.PriceDisplay != "£1.00" {
		t.Fatalf("expected the resolved price rendered, got %q", m.PriceDisplay)
	}
	if m.ThumbURL != "/public/assets/items/itm1/thumb.png" {
		t.Fatalf("expected the thumb URL now that the stable-data-dir bug is fixed, got %q", m.ThumbURL)
	}

	var payload string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE action='ai_identify'`).Scan(&payload); err != nil {
		t.Fatalf("expected an ai_identify audit row: %v", err)
	}
	if !strings.Contains(payload, `"matches":1`) {
		t.Fatalf("expected the audit payload to record the match count, got: %s", payload)
	}
}

func TestConfirmAPI_ValidatesItemIDAndSavesRealReferenceImage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, db := newAIAPITestDeps(t)
	dp.AI = fakeIdentifyServer(t, ai.IdentifyResult{})

	// A path-traversal-shaped item_id must be rejected before ever
	// touching the filesystem.
	req := multipartPhotoRequest(t, "/api/pos/identify/confirm", testPNGBytes(t), map[string]string{"item_id": "../../etc"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a path-traversal-shaped item_id, got %d: %s", rec.Code, rec.Body.String())
	}

	req = multipartPhotoRequest(t, "/api/pos/identify/confirm", testPNGBytes(t), map[string]string{"item_id": "does-not-exist"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown item, got %d: %s", rec.Code, rec.Body.String())
	}

	req = multipartPhotoRequest(t, "/api/pos/identify/confirm", testPNGBytes(t), map[string]string{"item_id": "itm1"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	entries, err := os.ReadDir(paths.Data("public", "assets", "items", "itm1", "ai_ref"))
	if err != nil {
		t.Fatalf("expected the confirmed reference image saved under the stable data dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one saved reference image, got %d", len(entries))
	}

	var payload string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE action='ai_identify_confirmed'`).Scan(&payload); err != nil {
		t.Fatalf("expected an ai_identify_confirmed audit row: %v", err)
	}
	if !strings.Contains(payload, "itm1") {
		t.Fatalf("expected the audit payload to record the item id, got: %s", payload)
	}
}

func TestConfirmAPI_NotConfiguredReturns404(t *testing.T) {
	mux, _, _ := newAIAPITestDeps(t)
	req := multipartPhotoRequest(t, "/api/pos/identify/confirm", testPNGBytes(t), map[string]string{"item_id": "itm1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when AI identify isn't configured, got %d: %s", rec.Code, rec.Body.String())
	}
}
