package pages

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// uuidPattern matches a canonical UUID (the shape internal item/variant IDs
// use) — ut-docs#303 tests assert this never appears in operator-facing
// import/catalog response text.
var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

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

// TestImport_BarcodeAttachFailureStillImportsStock covers ut-docs#293's
// second bug: a row whose barcode fails to attach must still have its item
// created and its opening stock imported, and the failure reason must
// survive into that row's rendered status rather than being discarded.
func TestImport_BarcodeAttachFailureStillImportsStock(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// Both rows share a barcode. The preview-time duplicate check only
	// looks at what's already committed in the DB (not the rest of this
	// batch), so both rows are annotated "ok"; the second row's AddBarcode
	// only fails inside the commit loop, once the first row's barcode has
	// actually been inserted — a deterministic, realistic attach failure.
	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W1,5012345678900,1.50,Snacks,7\n" +
		"Gadget,G2,5012345678900,2.00,Snacks,4\n"

	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	// Gadget (second row) must still be created despite the barcode clash.
	var itemID string
	if err := dp.Db.QueryRow(`SELECT id FROM items WHERE sku = 'G2'`).Scan(&itemID); err != nil {
		t.Fatalf("Gadget not created despite barcode attach failure: %v", err)
	}

	// The failure reason must be visible in the rendered per-row status,
	// not discarded — the card requires an operator-visible warning. Since
	// ut-docs#303, that reason names the CONFLICTING item ("Widget", the
	// row that actually holds the barcode) instead of leaking its raw
	// internal ID — assert both halves of that fix: the name is present,
	// and no raw UUID (36 hex-and-dashes chars) leaked into the response.
	resp := rec.Body.String()
	if !strings.Contains(resp, "already in use by Widget") {
		t.Fatalf("barcode conflict must name the conflicting item (Widget), got: %s", resp)
	}
	if uuidPattern.MatchString(resp) {
		t.Fatalf("raw internal ID leaked into the operator-facing response: %s", resp)
	}

	// Gadget's opening stock must still be imported — the barcode failure
	// must not skip the stock-import step below it.
	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM inventory WHERE item_id = ?`, itemID).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 4 {
		t.Fatalf("Gadget opening stock = %v, want 4 (barcode attach failure must not skip stock import)", qty)
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

// lastImportAudit decodes the most recent "catalog"/"import" audit_log row's
// data_json — the payload the operator's only durable record of a commit
// depends on once the HTTP response itself is gone.
func lastImportAudit(t *testing.T, db *sql.DB) map[string]any {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type = 'catalog' AND action = 'import' ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatalf("read import audit row: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode import audit payload %q: %v", raw, err)
	}
	return out
}

// stockAdjustedPublishCount counts "stock.adjusted" publish audit rows
// (internal/plugins EventBus.publish writes one event_published row per
// call, subscribers or not) — the only durable trace of whether
// publishStockAdjusted actually fired, without needing a live subscriber.
func stockAdjustedPublishCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'event' AND action = 'event_published' AND data_json LIKE '%stock.adjusted%'`).Scan(&n); err != nil {
		t.Fatalf("count stock.adjusted publishes: %v", err)
	}
	return n
}

// TestImport_WarnedRowSummaryCountsAndSurvives covers item 1 of the review:
// a warned row must be counted separately (warned), still counted as
// created (not failed), surfaced in the summary line via a locale key, and
// recorded in the audit payload.
//
// Reverting created++ to instead count a warned row as failed would fail
// this test: Gadget (the warned row) would drop out of "Imported" and the
// item-existence check below would still pass (the item IS created despite
// the miscount) but the summary's created count and the audit "created"
// field would both go from 2 to 1, which this test asserts against.
func TestImport_WarnedRowSummaryCountsAndSurvives(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	// Load the real locale bundle so the summary's T() calls render actual
	// text instead of depending on whatever another test in this package
	// happened to leave in httpx's global translator (i18nRef is
	// package-level state — order-dependent otherwise).
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// Widget is a clean row. Gadget shares Widget's barcode, so its
	// AddBarcode fails inside the commit loop (deterministic — see
	// TestImport_BarcodeAttachFailureStillImportsStock) and the row is
	// warned, not failed.
	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W1,5012345678900,1.50,Snacks,7\n" +
		"Gadget,G2,5012345678900,2.00,Snacks,4\n"

	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	// Both items exist — the warned row is still a completed import, not a
	// skip/failure.
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku IN ('W1','G2')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("both rows should be created (n=%d err=%v)", n, err)
	}

	// Summary line: Imported must count both rows, and a new figure keyed
	// off the new locale key (import.warned → "Warnings" in en.json) must
	// report exactly 1.
	if !strings.Contains(resp, "Imported: 2") {
		t.Fatalf("summary should report Imported: 2, got: %s", resp)
	}
	if !strings.Contains(resp, "Warnings: 1") {
		t.Fatalf("summary should report Warnings: 1 via the new locale key, got: %s", resp)
	}

	// Audit payload must carry the warned count too, since it's the only
	// record left once the HTTP response is discarded.
	audit := lastImportAudit(t, dp.Db)
	if got, ok := audit["created"].(float64); !ok || got != 2 {
		t.Fatalf("audit created = %v, want 2", audit["created"])
	}
	if got, ok := audit["warned"].(float64); !ok || got != 1 {
		t.Fatalf("audit warned = %v, want 1", audit["warned"])
	}
}

// TestImport_WarnedRowsSurvive200RowCap covers the other half of item 1: the
// rendered table hard-caps at 200 rows, but a warned row must never be one
// of the ones silently dropped by that cap.
//
// If warned rows were not exempted/partitioned ahead of the cap, this test
// would fail whenever the warned row's csv position falls after row 200 —
// exactly the "40 duplicate barcodes on a 500-row migration" scenario from
// the review.
func TestImport_WarnedRowsSurvive200RowCap(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	var sb strings.Builder
	sb.WriteString("Name,SKU,Barcode,Price,Category,In stock\n")
	// 205 clean rows with no barcode (each unique SKU), followed by one
	// deliberately-warned row at the very end — past where the old
	// unconditional 200-row cap would have cut it off.
	for i := 0; i < 205; i++ {
		sb.WriteString(fmt.Sprintf("Plain%03d,P%03d,,1.00,Snacks,0\n", i, i))
	}
	sb.WriteString("Warned,WRN,5012345678900,1.00,Snacks,1\n")
	sb.WriteString("WarnedDup,WRN2,5012345678900,1.00,Snacks,1\n")

	body, ct := multipartCSV(t, sb.String(), map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	if !strings.Contains(resp, "WarnedDup") {
		t.Fatalf("the warned row (207th in the file) must still be rendered despite the 200-row cap, got table without it: %s", resp)
	}
	// WarnedDup's barcode clashes with "Warned" (the row above it, which
	// actually holds it) — since ut-docs#303 the reason names that item.
	if !strings.Contains(resp, "already in use by Warned<") {
		t.Fatalf("warned row's reason must be visible, got: %s", resp)
	}
	// The visual treatment (ut-docs#303 review) — a warned row must carry
	// its own CSS class and icon, distinct from a clean or failed row.
	if !strings.Contains(resp, `class="row-warn"`) || !strings.Contains(resp, `row-warn-icon`) {
		t.Fatalf("warned row must carry the row-warn visual treatment, got: %s", resp)
	}
	// The "… N more" note must only be counting the still-hidden plain rows
	// (207 total - 1 rendered warned row - 200 rendered plain rows = 6).
	if !strings.Contains(resp, "… 6 more") {
		t.Fatalf("more-rows note should read '… 6 more' (only plain rows are capped), got: %s", resp)
	}
}

// TestImport_LocationLookupFailureWarnsStockNotCarried covers item 2's first
// bug: EnsureStockLocation runs once outside the loop, and when it fails,
// a row that genuinely carried stock must warn, not silently read "created"
// with the stock dropped.
//
// If the `locErr != nil` branch were removed (reverting to the original
// `it.HasStock && it.Stock > 0 && locErr == nil` guard with no warning),
// this test would fail: the item would still read "created" with no
// mention of the stock loss.
func TestImport_LocationLookupFailureWarnsStockNotCarried(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	// Force EnsureStockLocation to fail deterministically: renaming the
	// table away makes its lookup query return a real (non-ErrNoRows) DB
	// error, the same shape internal/data/pos_repo.go documents it can
	// return on a failed select/insert.
	if _, err := dp.Db.Exec(`ALTER TABLE stock_locations RENAME TO stock_locations_moved`); err != nil {
		t.Fatalf("rename stock_locations: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W1,5012345678900,1.50,Snacks,7\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("item should still be created despite the location lookup failure (n=%d err=%v)", n, err)
	}
	if !strings.Contains(resp, "stock not carried") {
		t.Fatalf("row should warn that stock was not carried, got: %s", resp)
	}
}

// TestImport_NegativeStockQuantityWarns covers item 2's second bug:
// catimport happily parses a negative "In stock" value, which the `> 0`
// guard then drops with no trace.
//
// If the negative-quantity case were removed from the switch, this test
// would fail: no "stock not carried"/"negative quantity" warning would
// appear anywhere in the response for this row.
func TestImport_NegativeStockQuantityWarns(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W1,5012345678900,1.50,Snacks,-3\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("item should still be created (n=%d err=%v)", n, err)
	}
	if !strings.Contains(resp, "negative quantity") {
		t.Fatalf("row should warn about the negative quantity, got: %s", resp)
	}

	// No stock movement was recorded for the negative quantity.
	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM inventory WHERE item_id = (SELECT id FROM items WHERE sku='W1')`).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 0 {
		t.Fatalf("negative opening stock must not be carried, got qty=%v", qty)
	}
}

// TestImport_StockRecordingFailureWarnsAndDoesNotPublish covers the
// "publishes only on stock success" property from item 4: when
// RecordStockMovement fails, both a warning must appear AND
// publishStockAdjusted must NOT fire.
//
// If publishStockAdjusted were hoisted out of the success `else` branch (so
// it runs regardless of RecordStockMovement's error), this test would fail
// on the stockAdjustedPublishCount assertion: a "stock.adjusted"
// event_published audit row would exist despite the recorded failure.
func TestImport_StockRecordingFailureWarnsAndDoesNotPublish(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	// Force RecordStockMovement's insert to fail deterministically for a
	// specific quantity, simulating a genuine DB error (e.g. a constraint,
	// a full disk) without touching production code.
	if _, err := dp.Db.Exec(`
CREATE TRIGGER import_test_stock_fail BEFORE INSERT ON stock_movements
WHEN NEW.quantity > 999
BEGIN SELECT RAISE(ABORT, 'simulated stock movement failure'); END;`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W1,5012345678900,1.50,Snacks,5000\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("item should still be created despite the stock movement failure (n=%d err=%v)", n, err)
	}
	if !strings.Contains(resp, "stock not carried") {
		t.Fatalf("row should warn that stock was not carried, got: %s", resp)
	}
	if got := stockAdjustedPublishCount(t, dp.Db); got != 0 {
		t.Fatalf("stock.adjusted must not be published when RecordStockMovement failed, got %d publishes", got)
	}
}

// TestImport_UnsupportedBarcodeShapeWarnsButStillImports covers item 3: a
// legitimate 4-digit produce PLU (or any alphanumeric internal code) is
// discarded by catimport's normalizeBarcode, but the row must still import
// and warn that its barcode was dropped, rather than silently importing
// with an empty barcode.
//
// If the BarcodeIssue plumbing (catimport recording why, or import_page.go
// surfacing it) were removed, this test would fail: the item would import
// with an empty barcode and no warning at all.
func TestImport_UnsupportedBarcodeShapeWarnsButStillImports(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// 4011 is a canonical PLU-format produce code — too short for
	// normalizeBarcode's 6-14 digit window, discarded today with no trace.
	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Bananas,PLU4011,4011,0.59,Produce,0\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var itemID string
	if err := dp.Db.QueryRow(`SELECT id FROM items WHERE sku = 'PLU4011'`).Scan(&itemID); err != nil {
		t.Fatalf("item should still be created despite the unsupported barcode shape: %v", err)
	}
	var barcodeCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = ?`, itemID).Scan(&barcodeCount); err != nil {
		t.Fatalf("count barcodes: %v", err)
	}
	if barcodeCount != 0 {
		t.Fatalf("normalizeBarcode's accepted shapes must not be loosened by this fix, got %d barcodes attached", barcodeCount)
	}
	if !strings.Contains(resp, "4011") || !strings.Contains(resp, "not imported") {
		t.Fatalf("row should warn that the 4011 barcode was not imported, got: %s", resp)
	}
}
