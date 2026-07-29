package pages

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestHTMLEscape(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"plain":               "plain",
		"a&b":                 "a&amp;b",
		"<script>":            "&lt;script&gt;",
		`say "hi"`:            "say &quot;hi&quot;",
		`<a href="x">&</a>`:   `&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;`,
		"a & b < c > d \" e":  "a &amp; b &lt; c &gt; d &quot; e",
		// & must be escaped exactly once (no double-encoding of the entities it emits).
		"&amp;": "&amp;amp;",
	}
	for in, want := range cases {
		if got := htmlEscape(in); got != want {
			t.Errorf("htmlEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// newImportTestDeps builds Deps on a fully-migrated database — the import
// commit path creates items/categories/barcodes/stock and writes audit rows,
// which the simplified seedForPages schema (no categories.parent_id) can't
// satisfy.
func newImportTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
}

// multipartCSV builds a multipart body with the CSV in "file" and optional
// extra form fields.
func multipartCSV(t *testing.T, csv string, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "catalog.csv")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte(csv)); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

const importCSV = "Name,SKU,Barcode,Price,Category,In stock\n" +
	"Widget,W1,5012345678900,1.50,Snacks,7\n" +
	"Gadget,G2,,2.00,,0\n"

func TestImport_PreviewDoesNotWrite(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, importCSV, nil) // no commit
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Widget") {
		t.Fatalf("preview should list the parsed rows, got: %s", rec.Body.String())
	}
	// Preview must not create anything.
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("preview wrote %d rows, expected 0", n)
	}
}

func TestImport_CommitCreatesCatalog(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	// Both rows should have been created (Gadget has no barcode, still valid).
	var itemID string
	if err := dp.Db.QueryRow(`SELECT id FROM items WHERE sku = 'W1'`).Scan(&itemID); err != nil {
		t.Fatalf("Widget not created: %v", err)
	}
	var gN int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'G2'`).Scan(&gN); err != nil || gN != 1 {
		t.Fatalf("Gadget not created (n=%d err=%v)", gN, err)
	}
	// Barcode attached.
	var barItem string
	if err := dp.Db.QueryRow(`SELECT item_id FROM item_barcodes WHERE barcode = '5012345678900'`).Scan(&barItem); err != nil {
		t.Fatalf("barcode not attached: %v", err)
	}
	if barItem != itemID {
		t.Fatalf("barcode attached to %q, want %q", barItem, itemID)
	}
	// Opening stock carried as an inventory quantity (Widget = 7).
	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM inventory WHERE item_id = ?`, itemID).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 7 {
		t.Fatalf("opening stock = %v, want 7", qty)
	}
	// Category created and linked.
	var catName string
	if err := dp.Db.QueryRow(`SELECT c.name FROM items i JOIN categories c ON c.id = i.category_id WHERE i.id = ?`, itemID).Scan(&catName); err != nil {
		t.Fatalf("category not linked: %v", err)
	}
	if catName != "Snacks" {
		t.Fatalf("category = %q, want Snacks", catName)
	}

	// Idempotency: re-committing the same file must skip the duplicates
	// (barcode/SKU already present), creating nothing new.
	body2, ct2 := multipartCSV(t, importCSV, map[string]string{"commit": "1"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-commit: code %d body %s", rec2.Code, rec2.Body.String())
	}
	var total int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku IN ('W1','G2')`).Scan(&total); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if total != 2 {
		t.Fatalf("re-import created duplicates: %d items, want 2", total)
	}
}

func TestImport_ManagerGate(t *testing.T) {
	t.Setenv("UT_AUTH", "") // auth ON
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// GET /import redirects non-managers to /catalog.
	req := httptest.NewRequest(http.MethodGet, "/import", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /import non-manager: code %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/catalog" {
		t.Fatalf("GET /import redirect = %q, want /catalog", loc)
	}

	// POST /api/import forbidden for non-managers.
	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "1"})
	req = httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/import non-manager: code %d, want 403", rec.Code)
	}

	// Export endpoints likewise gated.
	req = httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/catalog/export non-manager: code %d, want 403", rec.Code)
	}
}

func TestCatalogExport_RoundTripsHeader(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmX','SKX','Exported Item',150,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: code %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content-type = %q", ct)
	}
	out := rec.Body.String()
	// Header row uses the importer's own column names (round-trips).
	if !strings.HasPrefix(out, "Name,SKU,Barcode,Price,Category,Description,Sold by weight,In stock,Active") {
		t.Fatalf("export header wrong: %q", out)
	}
	if !strings.Contains(out, "Exported Item") || !strings.Contains(out, "1.50") {
		t.Fatalf("export missing seeded item: %q", out)
	}
}
