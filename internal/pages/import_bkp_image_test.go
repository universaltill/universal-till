package pages

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/paths"

	_ "modernc.org/sqlite"
)

// ut-docs#1223: a .bkp whose Products row carries a resolvable
// ProductImagePath must bring the real photo across, not just the
// ut-docs#1189 placeholder icon. These tests build a synthetic .bkp with a
// documents.zip member — never real customer data, same convention as
// internal/catimport/bkp_test.go and import_bkp_page_test.go.

// buildBkpDBBytesWithImagePath builds a Products table that also carries
// ProductImagePath (the real speedy kasse column this card adds support
// for), with one row referencing imgArchivePath.
func buildBkpDBBytesWithImagePath(t *testing.T, imgArchivePath string) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "bkp-image-fixture-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	t.Cleanup(func() { _ = os.Remove(path) })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// Same disposable-scratch reasoning as the sibling .bkp page tests —
	// this file is read back as raw bytes and removed at test end.
	if _, err := db.Exec(`PRAGMA journal_mode = MEMORY`); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if _, err := db.Exec(`PRAGMA synchronous = OFF`); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT,
		ProductTextShort TEXT,
		SalesPrice REAL,
		ProductGroupText TEXT,
		Status INTEGER,
		ProductType INTEGER,
		ProductImagePath TEXT
	)`); err != nil {
		t.Fatalf("create Products table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO Products
		(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType, ProductImagePath)
		VALUES ('30001','Flat White',3.10,'Coffee',1,1,?)`, imgArchivePath); err != nil {
		t.Fatalf("insert product: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close temp db: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp db bytes: %v", err)
	}
	return b
}

// buildTestPNG returns a tiny valid PNG's raw bytes — a real, decodable
// image, not just arbitrary bytes, so the commit-time image.Decode step
// this card adds is genuinely exercised end to end.
func buildTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

// buildOversizedTestPNG returns a real, fully decodable PNG whose pixel
// count sits just over imaging.MaxPixels — the ut-docs#1328 pixel-bomb
// shape (a solid color compresses to a small file while still being
// genuinely decodable, so a rejection here is attributable to the
// dimension guard, not to some unrelated "corrupt file" failure).
func buildOversizedTestPNG(t *testing.T) []byte {
	t.Helper()
	const w, h = 7000, 6000
	if int64(w)*int64(h) <= imaging.MaxPixels {
		t.Fatalf("test fixture %dx%d must exceed imaging.MaxPixels (%d)", w, h, imaging.MaxPixels)
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode oversized test png: %v", err)
	}
	return buf.Bytes()
}

// buildBkpZipWithImage assembles a .bkp-shaped ZIP whose Products table
// references imgArchivePath, plus a documents.zip member holding imgBytes
// at that same path.
func buildBkpZipWithImage(t *testing.T, imgArchivePath string, imgBytes []byte) []byte {
	t.Helper()
	dbBytes := buildBkpDBBytesWithImagePath(t, imgArchivePath)

	var docsBuf bytes.Buffer
	dzw := zip.NewWriter(&docsBuf)
	dw, err := dzw.Create(imgArchivePath)
	if err != nil {
		t.Fatalf("create documents.zip entry: %v", err)
	}
	if _, err := dw.Write(imgBytes); err != nil {
		t.Fatalf("write documents.zip entry: %v", err)
	}
	if err := dzw.Close(); err != nil {
		t.Fatalf("close documents.zip writer: %v", err)
	}

	return zipBkpEntriesWithDocs(t, dbBytes, docsBuf.Bytes())
}

// zipBkpEntriesWithDocs is zipBkpEntries' (import_bkp_page_test.go)
// counterpart that also embeds a documents.zip member.
func zipBkpEntriesWithDocs(t *testing.T, dbBytes, docsZipBytes []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	dbW, err := zw.Create("backup.db")
	if err != nil {
		t.Fatalf("create backup.db entry: %v", err)
	}
	if _, err := dbW.Write(dbBytes); err != nil {
		t.Fatalf("write backup.db entry: %v", err)
	}
	metaW, err := zw.Create("meta.inf")
	if err != nil {
		t.Fatalf("create meta.inf entry: %v", err)
	}
	if _, err := metaW.Write([]byte(`{"app_version":"4.4.08","device":"iMin I22T01","licence_key":"TEST-0000"}`)); err != nil {
		t.Fatalf("write meta.inf entry: %v", err)
	}
	docsW, err := zw.Create("documents.zip")
	if err != nil {
		t.Fatalf("create documents.zip entry: %v", err)
	}
	if _, err := docsW.Write(docsZipBytes); err != nil {
		t.Fatalf("write documents.zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

// TestImport_BkpRealImageWrittenAndRecorded is the ut-docs#1223 acceptance
// test: a .bkp whose Products row carries a resolvable ProductImagePath
// gets the REAL photo — not the ut-docs#1189 placeholder icon — written to
// disk and recorded in item_images, the same way a manual upload is.
func TestImport_BkpRealImageWrittenAndRecorded(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	imgBytes := buildTestPNG(t)
	zipBytes := buildBkpZipWithImage(t, "images/flat-white-uuid.png", imgBytes)
	body, ct := multipartFile(t, "backup.bkp", zipBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var itemID, imgPath string
	err := dp.Db.QueryRow(`
SELECT i.id, img.path FROM items i
JOIN item_images img ON img.item_id = i.id AND img.role = 'thumbnail'
WHERE i.sku = '30001'`).Scan(&itemID, &imgPath)
	if err != nil {
		t.Fatalf("query item_images: %v", err)
	}
	wantPath := "/public/assets/items/" + itemID + "/thumb.png"
	if imgPath != wantPath {
		t.Fatalf("recorded thumbnail path = %q, want %q (a real photo, not the placeholder icon)", imgPath, wantPath)
	}

	onDisk := paths.Data("public", "assets", "items", itemID, "thumb.png")
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("read written thumbnail: %v", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("written thumbnail is not a valid image: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Errorf("written thumbnail size = %dx%d, want 2x2", b.Dx(), b.Dy())
	}
}

// TestImport_BkpDanglingImagePathWarnsAndFallsBackToPlaceholder: a row
// whose ProductImagePath doesn't resolve still imports, with the
// ut-docs#1189 placeholder icon (never a blank tile), and the commit
// response surfaces a warning naming the dropped reference (ut-docs#293's
// defect class — never silently drop it).
func TestImport_BkpDanglingImagePathWarnsAndFallsBackToPlaceholder(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	dbBytes := buildBkpDBBytesWithImagePath(t, "images/does-not-exist.jpg")
	zipBytes := zipBkpEntries(t, dbBytes) // no documents.zip at all

	body, ct := multipartFile(t, "backup.bkp", zipBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does-not-exist.jpg") {
		t.Errorf("commit response must warn about the dangling image path, got: %s", rec.Body.String())
	}

	var itemID, imgPath string
	if err := dp.Db.QueryRow(`
SELECT i.id, img.path FROM items i
JOIN item_images img ON img.item_id = i.id AND img.role = 'thumbnail'
WHERE i.sku = '30001'`).Scan(&itemID, &imgPath); err != nil {
		t.Fatalf("query item_images: %v", err)
	}
	if imgPath == "/public/assets/items/"+itemID+"/thumb.png" {
		t.Errorf("a dangling image reference must fall back to the placeholder icon, not claim a real photo path")
	}
}

// TestImport_BkpOversizedImageWarnsAndFallsBackToPlaceholder is the
// ut-docs#1328 regression for the .bkp commit-time image write: a real,
// fully valid, 42-million-pixel PNG resolved from the archive's
// documents.zip must be rejected by the same imaging.Decode pixel-count
// guard the manual-upload handlers use (independently proven here by
// running with the guard disabled — see internal/imaging/decode_test.go
// and internal/pages/catalog/image_upload_test.go for the sibling manual-
// upload proof), falling back to the placeholder icon with a surfaced
// warning — the same best-effort, never-fail-the-row behavior as a
// dangling or corrupt image reference, never a 500 or an unbounded decode.
func TestImport_BkpOversizedImageWarnsAndFallsBackToPlaceholder(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	imgBytes := buildOversizedTestPNG(t)
	zipBytes := buildBkpZipWithImage(t, "images/flat-white-uuid.png", imgBytes)
	body, ct := multipartFile(t, "backup.bkp", zipBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "photo could not be read") {
		t.Errorf("commit response must warn that the oversized photo could not be read, got: %s", rec.Body.String())
	}

	var itemID, imgPath string
	if err := dp.Db.QueryRow(`
SELECT i.id, img.path FROM items i
JOIN item_images img ON img.item_id = i.id AND img.role = 'thumbnail'
WHERE i.sku = '30001'`).Scan(&itemID, &imgPath); err != nil {
		t.Fatalf("query item_images: %v", err)
	}
	if imgPath == "/public/assets/items/"+itemID+"/thumb.png" {
		t.Errorf("an oversized/pixel-bomb image reference must fall back to the placeholder icon, not claim a real photo path")
	}
	if _, err := os.Stat(paths.Data("public", "assets", "items", itemID, "thumb.png")); err == nil {
		t.Error("a rejected oversized image must not leave a real thumbnail file behind")
	}
}
