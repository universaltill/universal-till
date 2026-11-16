package pages

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

func photoTestPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type photoFixture struct {
	d      *common.Deps
	mux    *http.ServeMux
	folder string
	mgr    auth.User
}

func newPhotoFixture(t *testing.T) *photoFixture {
	t.Helper()
	prevData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(prevData) })
	d := newImportTestDeps(t)
	d.AuthSvc = auth.NewService(d.Db)
	mux := http.NewServeMux()
	registerImportPhotos(mux, d)
	if _, err := d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('m1','m1','m1','x','manager')`); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	for _, f := range []string{"one.png", "two.png"} {
		if err := os.WriteFile(filepath.Join(folder, f), photoTestPNG(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"P1", "P2"} {
		if _, err := d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES (?,?,?,100,1)`, "itm-"+s, s, "Item "+s); err != nil {
			t.Fatal(err)
		}
	}
	return &photoFixture{d: d, mux: mux, folder: folder, mgr: auth.User{ID: "m1", Role: "manager"}}
}

func (f *photoFixture) post(t *testing.T, u auth.User, csv string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "p.csv")
	fw.Write([]byte(csv))
	for k, v := range fields {
		w.WriteField(k, v)
	}
	w.Close()
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/import/photos", &buf), u)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func thumbRows(t *testing.T, d *common.Deps) int {
	t.Helper()
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM item_images WHERE role='thumbnail'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPhotoImport_RoleGating(t *testing.T) {
	f := newPhotoFixture(t)
	csv := "sku;image\nP1;one.png\n"
	rec := f.post(t, auth.User{ID: "c1", Role: "cashier"}, csv, map[string]string{"folder": f.folder, "commit": "1"})
	if rec.Code != http.StatusForbidden || thumbRows(t, f.d) != 0 {
		t.Fatalf("cashier = %d rows=%d", rec.Code, thumbRows(t, f.d))
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, photoFailedRoute+"?id=x", nil), auth.User{ID: "c1", Role: "cashier"})
	rr := httptest.NewRecorder()
	f.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cashier failed.csv = %d", rr.Code)
	}
}

func TestPhotoImport_JoinedTillRefused(t *testing.T) {
	f := newPhotoFixture(t)
	if err := f.d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, f.mgr, "sku,image\nP1,one.png\n", map[string]string{"folder": f.folder, "commit": "1"})
	if rec.Code != http.StatusConflict || thumbRows(t, f.d) != 0 {
		t.Fatalf("joined = %d", rec.Code)
	}
}

func TestPhotoImport_PreviewApplyReplaceIdempotent(t *testing.T) {
	f := newPhotoFixture(t)
	csv := "\xef\xbb\xbfSKU;Bildpfad\r\nP1;one.png\r\nP2;..\\evil.png\r\nNOPE;two.png\r\n"
	prev := f.post(t, f.mgr, csv, map[string]string{"folder": f.folder, "commit": "0"})
	if prev.Code != 200 || thumbRows(t, f.d) != 0 {
		t.Fatalf("preview = %d rows=%d: %s", prev.Code, thumbRows(t, f.d), prev.Body.String())
	}
	for _, want := range []string{`data-photo-status="add"`, `data-photo-status="outside_folder"`, `data-photo-status="unknown_sku"`} {
		if !strings.Contains(prev.Body.String(), want) {
			t.Errorf("preview missing %s: %s", want, prev.Body.String())
		}
	}
	if strings.Contains(prev.Body.String(), f.folder) {
		t.Error("response leaks the folder path")
	}
	app := f.post(t, f.mgr, csv, map[string]string{"folder": f.folder, "commit": "1"})
	if app.Code != 200 || thumbRows(t, f.d) != 1 {
		t.Fatalf("apply = %d rows=%d: %s", app.Code, thumbRows(t, f.d), app.Body.String())
	}
	if !strings.Contains(app.Body.String(), photoFailedRoute) {
		t.Error("failed-rows link missing")
	}
	if _, err := os.Stat(paths.Data("public", "assets", "items", "itm-P1", "thumb.png")); err != nil {
		t.Fatalf("thumb not written: %v", err)
	}
	var audits int
	f.d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='photo_import'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit entries = %d", audits)
	}
	// Re-run: item already has a photo, replace off -> skipped.
	again := f.post(t, f.mgr, csv, map[string]string{"folder": f.folder, "commit": "1"})
	if !strings.Contains(again.Body.String(), `data-photo-status="skipped_has_photo"`) || thumbRows(t, f.d) != 1 {
		t.Fatalf("re-run: %s", again.Body.String())
	}
	// Replace on: replaced, still one row.
	rep := f.post(t, f.mgr, csv, map[string]string{"folder": f.folder, "commit": "1", "replace": "1"})
	if !strings.Contains(rep.Body.String(), `data-photo-status="replace"`) || thumbRows(t, f.d) != 1 {
		t.Fatalf("replace: %s", rep.Body.String())
	}
	// It never touched anything else about the item.
	var name string
	var price int
	f.d.Db.QueryRow(`SELECT name, base_price FROM items WHERE id='itm-P1'`).Scan(&name, &price)
	if name != "Item P1" || price != 100 {
		t.Fatalf("item edited: %s %d", name, price)
	}
	// Failed CSV download works for the owner only.
	id := app.Body.String()[strings.Index(app.Body.String(), "?id=")+4:]
	id = id[:strings.Index(id, `"`)]
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, photoFailedRoute+"?id="+id, nil), f.mgr)
	rr := httptest.NewRecorder()
	f.mux.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "P2,outside_folder") {
		t.Fatalf("failed csv = %d %q", rr.Code, rr.Body.String())
	}
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, photoFailedRoute+"?id="+id, nil), auth.User{ID: "other", Role: "manager"})
	rr = httptest.NewRecorder()
	f.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("other user's failed csv = %d", rr.Code)
	}
}

var _ = data.NewCatalogRepo

// A relative folder would resolve against the process's working directory
// (not the shop's folder): refused before any row is read.
func TestPhotoImport_RelativeFolderRefused(t *testing.T) {
	f := newPhotoFixture(t)
	for _, folder := range []string{".", "photos", "./"} {
		rec := f.post(t, f.mgr, "sku,image\nP1,one.png\n", map[string]string{"folder": folder, "commit": "1"})
		if rec.Code != http.StatusBadRequest || thumbRows(t, f.d) != 0 {
			t.Fatalf("folder %q = %d rows=%d", folder, rec.Code, thumbRows(t, f.d))
		}
	}
}
