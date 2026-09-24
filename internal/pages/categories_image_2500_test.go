package pages

import (
	"bytes"
	"html"
	"image"
	"image/color"
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
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#2500: the category dialog sets a category's image — a built-in
// icon or an uploaded photo — in the same Save as its name/colour/links.
// The dialog form is multipart now (it carries a file), so every test here
// posts a REAL multipart body: ParseForm alone ignores one and group_id /
// station_id would silently arrive empty (the ut-docs#2018 trap).

type catPart struct{ name, value string }

func categoryMultipart(t *testing.T, fields []catPart, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := mw.WriteField(f.name, f.value); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		fw, err := mw.CreateFormFile("image", "photo.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func postCategoryMultipart(t *testing.T, mux *http.ServeMux, path string, fields []catPart, file []byte, user auth.User) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := categoryMultipart(t, fields, file)
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("HX-Request", "true")
	req = auth.WithUser(req, user)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{G: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func useTempDataDir(t *testing.T) string {
	t.Helper()
	orig := paths.DataDir()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init(orig) })
	return dir
}

func TestCategoryImage_CreateWithIconEditWithUploadThenIconThenNone(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	dataDir := useTempDataDir(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	ctx := t.Context()
	if _, err := data.NewModifierRepo(d.Db).CreateGroup(ctx, "g-milk", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	readImage := func(id string) string {
		t.Helper()
		var p string
		if err := d.Db.QueryRow(`SELECT COALESCE(image_path, '') FROM categories WHERE id = ?`, id).Scan(&p); err != nil {
			t.Fatalf("read image_path: %v", err)
		}
		return p
	}

	// Create with a built-in icon — and a group, which must survive the
	// multipart body (ut-docs#2018).
	rec := postCategoryMultipart(t, mux, "/api/categories", []catPart{
		{"name", "Coffee"}, {"icon", "coffee"}, {"group_id", "g-milk"},
	}, nil, manager)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/categories" {
		t.Fatalf("create: code=%d hx-redirect=%q body=%s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Coffee'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := readImage(id); got != "/public/assets/category-icons/coffee.svg" {
		t.Fatalf("create with icon stored %q", got)
	}
	if links, _ := data.NewModifierRepo(d.Db).AllCategoryModifierGroupLinks(ctx); len(links[id]) != 1 {
		t.Fatalf("group_id lost in the multipart body (ut-docs#2018): links=%v", links[id])
	}

	// Edit with an upload: the file is written under the data dir and the
	// path stored; the icon choice is replaced. Links unchanged this time.
	rec = postCategoryMultipart(t, mux, "/api/categories/"+id, []catPart{
		{"name", "Coffee"}, {"group_id", "g-milk"},
	}, tinyPNG(t), manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit with upload: code=%d body=%s", rec.Code, rec.Body.String())
	}
	file := filepath.Join(dataDir, "public", "assets", "categories", id, "thumb.png")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("uploaded thumb not written at %s: %v", file, err)
	}
	if got := readImage(id); got != "/public/assets/categories/"+id+"/thumb.png" {
		t.Fatalf("edit with upload stored %q", got)
	}

	// A plain Save with icon "" (keep) leaves the upload alone.
	rec = postCategoryMultipart(t, mux, "/api/categories/"+id, []catPart{{"name", "Coffee Bar"}, {"icon", ""}}, nil, manager)
	if rec.Code != http.StatusOK || readImage(id) != "/public/assets/categories/"+id+"/thumb.png" {
		t.Fatalf("keep: code=%d image=%q", rec.Code, readImage(id))
	}

	// Picking a built-in icon replaces the upload AND removes its file.
	rec = postCategoryMultipart(t, mux, "/api/categories/"+id, []catPart{{"name", "Coffee Bar"}, {"icon", "pastry"}}, nil, manager)
	if rec.Code != http.StatusOK || readImage(id) != "/public/assets/category-icons/pastry.svg" {
		t.Fatalf("icon over upload: code=%d image=%q", rec.Code, readImage(id))
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("superseded upload must be removed, stat err=%v", err)
	}

	// "none" clears the image.
	rec = postCategoryMultipart(t, mux, "/api/categories/"+id, []catPart{{"name", "Coffee Bar"}, {"icon", "none"}}, nil, manager)
	if rec.Code != http.StatusOK || readImage(id) != "" {
		t.Fatalf("none: code=%d image=%q", rec.Code, readImage(id))
	}

	// The list row carries the current image for the dialog's picker.
	if _, err := d.Db.Exec(`UPDATE categories SET image_path = '/public/assets/category-icons/drink.svg' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{
		`data-image="/public/assets/category-icons/drink.svg"`,
		`enctype="multipart/form-data"`,
		`id="category-icon-grid"`,
		`data-icon="coffee"`,
		`data-icon="none"`,
		`name="icon" id="category-icon"`,
		`type="file" name="image" id="category-image-file"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /categories missing %s", want)
		}
	}
}

func TestCategoryImage_RefusalsWriteNothing(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	dataDir := useTempDataDir(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	count := func(name string) int {
		var n int
		_ = d.Db.QueryRow(`SELECT count(*) FROM categories WHERE name = ?`, name).Scan(&n)
		return n
	}

	// Unknown icon key → 400 in the dialog, no row created.
	rec := postCategoryMultipart(t, mux, "/api/categories", []catPart{{"name", "BadIcon"}, {"icon", "../../etc"}}, nil, manager)
	if rec.Code != http.StatusBadRequest || count("BadIcon") != 0 {
		t.Fatalf("bad icon: code=%d rows=%d body=%s", rec.Code, count("BadIcon"), rec.Body.String())
	}
	if want := httpx.T("en", "categories.error.image_icon_invalid"); want == "categories.error.image_icon_invalid" || !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
		t.Fatalf("bad icon body should carry the localized message: %s", rec.Body.String())
	}

	// Undecodable upload → the catalog's localized image error, no row.
	rec = postCategoryMultipart(t, mux, "/api/categories", []catPart{{"name", "BadPhoto"}}, []byte("not a png"), manager)
	if rec.Code != http.StatusBadRequest || count("BadPhoto") != 0 {
		t.Fatalf("bad image: code=%d rows=%d body=%s", rec.Code, count("BadPhoto"), rec.Body.String())
	}
	if want := httpx.T("en", "catalog.error.image_invalid"); !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
		t.Fatalf("bad image body = %q, want the localized %q", rec.Body.String(), want)
	}

	// Review finding: a 10–11 MB upload fits under the body cap but not the
	// read limit; it must say "too large", not "not a valid image".
	big := append(tinyPNG(t), bytes.Repeat([]byte{0}, 10<<20)...)
	rec = postCategoryMultipart(t, mux, "/api/categories", []catPart{{"name", "BigPhoto"}}, big, manager)
	if rec.Code != http.StatusBadRequest || count("BigPhoto") != 0 {
		t.Fatalf("big image: code=%d rows=%d", rec.Code, count("BigPhoto"))
	}
	if want := httpx.T("en", "catalog.error.image_too_large"); !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
		t.Fatalf("big image body = %q, want the localized %q", rec.Body.String(), want)
	}

	// A traversal-shaped id never reaches a filesystem path.
	if _, err := d.Db.Exec(`INSERT INTO categories (id, name) VALUES ('a.b', 'Dotted')`); err != nil {
		t.Fatal(err)
	}
	rec = postCategoryMultipart(t, mux, "/api/categories/a.b", []catPart{{"name", "Dotted"}}, tinyPNG(t), manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal id: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir, "public", "assets", "categories")); len(entries) != 0 {
		t.Fatalf("nothing may be written for a refused id, found %d entries", len(entries))
	}

	// On a satellite the dialog is refused as before, multipart or not.
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	rec = postCategoryMultipart(t, mux, "/api/categories", []catPart{{"name", "OnReplica"}, {"icon", "coffee"}}, nil, manager)
	if rec.Code != http.StatusBadRequest || count("OnReplica") != 0 {
		t.Fatalf("replica: code=%d rows=%d", rec.Code, count("OnReplica"))
	}
}
