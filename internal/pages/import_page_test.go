package pages

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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
		"":                   "",
		"plain":              "plain",
		"a&b":                "a&amp;b",
		"<script>":           "&lt;script&gt;",
		`say "hi"`:           "say &quot;hi&quot;",
		`<a href="x">&</a>`:  `&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;`,
		"a & b < c > d \" e": "a &amp; b &lt; c &gt; d &quot; e",
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
// satisfy. Currency is pre-confirmed (see newImportTestDepsWithCurrencyState):
// these tests exercise the import pipeline itself, not the ut-docs#970
// first-import currency-confirmation gate.
func newImportTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	return newImportTestDepsWithCurrencyState(t, true)
}

// newImportTestDepsWithCurrencyState is newImportTestDeps parameterized on
// whether the till's currency has been explicitly confirmed
// (common.KeyCurrencyConfirmed, ut-docs#970) — confirmed=false reproduces a
// fresh till that has never had its currency touched, the exact state the
// import currency-confirmation gate exists for.
func newImportTestDepsWithCurrencyState(t *testing.T, confirmed bool) *common.Deps {
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
	store := settings.NewStore(d.DB)
	if confirmed {
		if err := store.Set(t.Context(), common.KeyCurrencyConfirmed, "true"); err != nil {
			t.Fatalf("seed currency confirmed: %v", err)
		}
	}
	return &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: store,
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
	if !strings.HasPrefix(out, "Name,SKU,Barcode,Price,Category,Description,Sold by weight,In stock,Active,Tax rate,Takeaway tax") {
		t.Fatalf("export header wrong: %q", out)
	}
	if !strings.Contains(out, "Exported Item") || !strings.Contains(out, "1.50") {
		t.Fatalf("export missing seeded item: %q", out)
	}
}

// TestCatalogExport_RoundTripsTaxColumns pins ut-docs#512's migration-path
// acceptance criterion: export a catalog with mixed tax pairs — including
// the two distinct 19% groups — and parsing the CSV back through our own
// importer must reproduce the same (dine-in, takeaway) pairs losslessly.
func TestCatalogExport_RoundTripsTaxColumns(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	repo := data.NewCatalogRepo(dp.Db)
	ta7 := 700
	overrideCode, _, err := repo.FindOrCreateTaxCode(t.Context(), 1900, &ta7)
	if err != nil {
		t.Fatal(err)
	}
	flatCode, _, err := repo.FindOrCreateTaxCode(t.Context(), 1900, nil)
	if err != nil {
		t.Fatal(err)
	}
	fracCode, _, err := repo.FindOrCreateTaxCode(t.Context(), 1950, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id, sku, name, taxCode string) {
		t.Helper()
		if _, err := dp.Db.Exec(
			`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES(?,?,?,150,?,1)`,
			id, sku, name, taxCode); err != nil {
			t.Fatalf("seed %s: %v", sku, err)
		}
	}
	seed("itmA", "RT1", "Override Drink", overrideCode)
	seed("itmB", "RT2", "Flat Mug", flatCode)
	seed("itmC", "RT3", "Fraction Widget", fracCode)
	if _, err := dp.Db.Exec(
		`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmD','RT4','No Tax Item',150,1)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerImport(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: code %d body %s", rec.Code, rec.Body.String())
	}

	res, err := catimport.Parse(strings.NewReader(rec.Body.String()), 2, data.DefaultEnabledBarcodeSymbologyIDs())
	if err != nil {
		t.Fatalf("re-parse of our own export failed: %v", err)
	}
	bySKU := map[string]catimport.ImportItem{}
	for _, it := range res.Items {
		bySKU[it.SKU] = it
	}
	drink := bySKU["RT1"]
	if !drink.HasTax || drink.TaxRateBP != 1900 || !drink.HasTakeaway || drink.TakeawayRateBP != 700 {
		t.Errorf("override pair did not round-trip: %+v", drink)
	}
	mug := bySKU["RT2"]
	if !mug.HasTax || mug.TaxRateBP != 1900 || mug.HasTakeaway {
		t.Errorf("flat 19%% did not round-trip (takeaway must stay unset): %+v", mug)
	}
	frac := bySKU["RT3"]
	if !frac.HasTax || frac.TaxRateBP != 1950 {
		t.Errorf("fractional rate did not round-trip: %+v", frac)
	}
	none := bySKU["RT4"]
	if none.HasTax || none.HasTakeaway || none.TaxIssue != "" || none.TakeawayTaxIssue != "" {
		t.Errorf("item with no tax code must export blank tax cells: %+v", none)
	}
}

// cafeTaxCSV mirrors ut-docs#512's real café distribution: four (dine-in,
// takeaway) VAT pairs (ut-docs#512): (19,7) needs a takeaway override,
// (7,7)/(19,19)/(0,0) don't. Two extra rows pin the grouping edges: Tea is a
// second (7,7) row (shares Cake's code) and Poster carries a dine-in rate
// with NO takeaway cell at all (must share Logo Mug's code — an explicit
// equal pair and an absent takeaway column both mean "no override").
const cafeTaxCSV = "Name,SKU,Price,Tax rate,Takeaway tax\n" +
	"Latte,T1,3.50,19,7\n" +
	"Cake Slice,T2,4.00,7,7\n" +
	"Logo Mug,T3,9.00,19,19\n" +
	"Gift Voucher,T4,10.00,0,0\n" +
	"Tea,T5,2.00,7,7\n" +
	"Poster,T6,5.00,19,\n"

// installTaxDePlugin registers com.universaltill.tax-de as installed and
// active — the same direct-seed shape tax_hook_cache_test.go uses.
func installTaxDePlugin(t *testing.T, db *sql.DB) {
	t.Helper()
	// plugins(id, version) references plugin_catalog(id, version), so the
	// catalog row comes first.
	if _, err := db.Exec(
		`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		 VALUES ('com.universaltill.tax-de', '1.0.0', 'German Tax', 'wasm', 'plugin.wasm', 'https://example.invalid/tax-de.zip', 'deadbeef', '0.1.0', '1', datetime('now'))`); err != nil {
		t.Fatalf("seed tax-de catalog row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active)
		 VALUES ('com.universaltill.tax-de', 'German Tax', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatalf("seed tax-de plugin: %v", err)
	}
}

// installTaxDePluginDisabled seeds ut-plugin-tax-de installed but disabled
// (is_active = 0) — the ut-docs#531 case: a merchant who imports before
// enabling the plugin, as opposed to never installing it at all.
func installTaxDePluginDisabled(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		 VALUES ('com.universaltill.tax-de', '1.0.0', 'German Tax', 'wasm', 'plugin.wasm', 'https://example.invalid/tax-de.zip', 'deadbeef', '0.1.0', '1', datetime('now'))`); err != nil {
		t.Fatalf("seed tax-de catalog row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active)
		 VALUES ('com.universaltill.tax-de', 'German Tax', '1.0.0', 'plugin.wasm', 'wasm', 0)`); err != nil {
		t.Fatalf("seed disabled tax-de plugin: %v", err)
	}
}

// itemTaxPair reads the (rate, takeaway) pair behind one imported item's tax
// code. takeaway is nil when takeaway_rate_basis_points is NULL.
func itemTaxPair(t *testing.T, db *sql.DB, sku string) (codeID string, rateBP int, takeawayBP *int) {
	t.Helper()
	var ta sql.NullInt64
	err := db.QueryRow(`
SELECT t.id, t.rate_basis_points, t.takeaway_rate_basis_points
FROM items i JOIN tax_codes t ON t.id = i.tax_code_id WHERE i.sku = ?`, sku).
		Scan(&codeID, &rateBP, &ta)
	if err != nil {
		t.Fatalf("read tax pair for %s: %v", sku, err)
	}
	if ta.Valid {
		v := int(ta.Int64)
		takeawayBP = &v
	}
	return codeID, rateBP, takeawayBP
}

// TestImport_TaxColumnsGroupOntoTaxCodesAndPopulateOverrides is ut-docs#512's
// core acceptance test: items group onto tax codes by (dine-in, takeaway)
// pair — including the two distinct 19% groups, which must NOT collide — and
// the (19,7) override pair lands in ut-plugin-tax-de's
// takeaway_rate_overrides setting so the merchant never hand-writes it.
func TestImport_TaxColumnsGroupOntoTaxCodesAndPopulateOverrides(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	installTaxDePlugin(t, dp.Db)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	var taxCodesBefore int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&taxCodesBefore); err != nil {
		t.Fatal(err)
	}

	// Preview writes nothing — including tax codes and plugin settings.
	body, ct := multipartCSV(t, cafeTaxCSV, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	var taxCodesAfterPreview int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&taxCodesAfterPreview); err != nil {
		t.Fatal(err)
	}
	if taxCodesAfterPreview != taxCodesBefore {
		t.Fatalf("preview created tax codes: %d -> %d", taxCodesBefore, taxCodesAfterPreview)
	}

	body, ct = multipartCSV(t, cafeTaxCSV, map[string]string{"commit": "1"})
	req = httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	latteCode, latteRate, latteTA := itemTaxPair(t, dp.Db, "T1")
	if latteRate != 1900 || latteTA == nil || *latteTA != 700 {
		t.Fatalf("Latte pair = (%d,%v), want (1900,&700)", latteRate, latteTA)
	}
	cakeCode, cakeRate, cakeTA := itemTaxPair(t, dp.Db, "T2")
	if cakeRate != 700 || cakeTA != nil {
		t.Fatalf("Cake pair = (%d,%v), want (700,nil) — an equal pair needs no override", cakeRate, cakeTA)
	}
	mugCode, mugRate, mugTA := itemTaxPair(t, dp.Db, "T3")
	if mugRate != 1900 || mugTA != nil {
		t.Fatalf("Mug pair = (%d,%v), want (1900,nil)", mugRate, mugTA)
	}
	// The two distinct 19% groups must both work without colliding.
	if mugCode == latteCode {
		t.Fatal("(19,19) and (19,7) must not collapse onto one tax code")
	}
	voucherCode, voucherRate, voucherTA := itemTaxPair(t, dp.Db, "T4")
	if voucherRate != 0 || voucherTA != nil {
		t.Fatalf("Voucher pair = (%d,%v), want (0,nil)", voucherRate, voucherTA)
	}
	// (0,0) re-finds the seeded 'Zero-rated' code — idempotent against
	// pre-existing tax codes, not just this import's own creations.
	if voucherCode != "tax_zero" {
		t.Fatalf("Voucher should reuse the seeded zero-rated code, got %q", voucherCode)
	}
	// Grouping, not one-code-per-item: Tea shares Cake's code, and Poster
	// (dine-in 19, no takeaway cell) shares Mug's (explicit 19,19).
	teaCode, _, _ := itemTaxPair(t, dp.Db, "T5")
	if teaCode != cakeCode {
		t.Fatalf("Tea must share Cake's tax code, got %q vs %q", teaCode, cakeCode)
	}
	posterCode, _, _ := itemTaxPair(t, dp.Db, "T6")
	if posterCode != mugCode {
		t.Fatalf("Poster (19, no takeaway cell) must share Mug's (19,19) code, got %q vs %q", posterCode, mugCode)
	}

	// The override setting carries exactly the one genuine override pair.
	var raw string
	if err := dp.Db.QueryRow(
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil {
		t.Fatalf("takeaway_rate_overrides not written: %v", err)
	}
	var overrides map[string]int
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		t.Fatalf("overrides not valid JSON %q: %v", raw, err)
	}
	if len(overrides) != 1 || overrides[latteCode] != 700 {
		t.Fatalf("overrides = %v, want {%q: 700}", overrides, latteCode)
	}

	// Observability: the audit payload counts what tax work happened.
	audit := lastImportAudit(t, dp.Db)
	// 3, not 4: (19,7), (7,-) and (19,-) are new; (0,-) reused tax_zero.
	if got, ok := audit["tax_codes_created"].(float64); !ok || got != 3 {
		t.Fatalf("audit tax_codes_created = %v, want 3 (19/7, 7, 19 — the 0%% group reuses tax_zero)", audit["tax_codes_created"])
	}
	if got, ok := audit["takeaway_overrides_set"].(float64); !ok || got != 1 {
		t.Fatalf("audit takeaway_overrides_set = %v, want 1", audit["takeaway_overrides_set"])
	}

	// Idempotency: re-running the import creates no further tax codes.
	var taxCodesAfter int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&taxCodesAfter); err != nil {
		t.Fatal(err)
	}
	body2, ct2 := multipartCSV(t, cafeTaxCSV, map[string]string{"commit": "1"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-commit: code %d", rec2.Code)
	}
	var taxCodesFinal int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&taxCodesFinal); err != nil {
		t.Fatal(err)
	}
	if taxCodesFinal != taxCodesAfter {
		t.Fatalf("re-import duplicated tax codes: %d -> %d", taxCodesAfter, taxCodesFinal)
	}
}

// TestHandCreatedEqualPairTaxCode_MatchesFreshImportNoChurn is ut-docs#1013's
// second acceptance criterion, read correctly: NOT the plugin's
// takeaway_rate_overrides JSON (which has no export/import round-trip at
// all — that shape is covered separately by
// TestResolve_NoOpOverrideStableAcrossReserialization in ut-plugin-tax-de),
// but a hand-created TAX CODE whose takeaway rate equals its dine-in rate —
// the exact shape ut-docs#536 already fixed on the CSV-import side (pinned
// above by cafeTaxCSV's "Cake Slice,T2,4.00,7,7" row).
//
// Before this test (ut-docs#1013 review finding), only the IMPORT path
// canonicalized an equal pair to "no override" (nil, see the
// it.HasTakeaway && it.TakeawayRateBP != it.TaxRateBP check above). A code
// hand-created via the tax-code management UI (POST /api/catalog/tax-codes)
// stored an explicit takeaway_rate_basis_points == rate_basis_points, which
// FindOrCreateTaxCode's (rate, takeaway) lookup could never match against a
// later import's canonicalized (rate, nil) query — silently creating a
// SECOND "Imported 7%" code and migrating every subsequently-imported item
// onto it, abandoning the merchant's own hand-created code. Fixed by
// canonicalizing at the one place a human creates/edits a tax code
// (parseTaxCodeForm), so both paths agree on what "no override" means.
func TestHandCreatedEqualPairTaxCode_MatchesFreshImportNoChurn(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerTaxCodes(mux, dp)
	registerImport(mux, dp)

	// A German café hand-creates "Speisen 7% (Haus & Ausser Haus)" via the
	// tax-code management UI — food is 7% in both consumption modes, and a
	// merchant reading the form literally could fill in the SAME rate for
	// both fields rather than leaving takeaway blank.
	form := url.Values{"name": {"Speisen 7%"}, "rate": {"7"}, "takeawayRate": {"7"}}
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create tax code: code %d body %s", rec.Code, rec.Body.String())
	}

	var handCreatedID string
	var takeawayNull bool
	if err := dp.Db.QueryRow(
		`SELECT id, takeaway_rate_basis_points IS NULL FROM tax_codes WHERE name = ?`, "Speisen 7%").
		Scan(&handCreatedID, &takeawayNull); err != nil {
		t.Fatalf("read created tax code: %v", err)
	}
	if !takeawayNull {
		t.Fatal("hand-created equal-pair (7,7) tax code must canonicalize takeaway_rate_basis_points to NULL, same as the CSV-import path — a real export of this code would otherwise echo an explicit takeaway column a later re-import can't match back")
	}

	var taxCodesBefore int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&taxCodesBefore); err != nil {
		t.Fatal(err)
	}

	// Import a FRESH item whose CSV row declares the SAME (7, 7) pair —
	// exactly what exporting this shop's own catalog (or a second till's
	// backup) and re-importing it would produce.
	const freshCakeCSV = "Name,SKU,Price,Tax rate,Takeaway tax\n" +
		"Cake Slice,CAKE-FRESH,4.00,7,7\n"
	body, ct := multipartCSV(t, freshCakeCSV, map[string]string{"commit": "1"})
	importReq := httptest.NewRequest(http.MethodPost, "/api/import", body)
	importReq.Header.Set("Content-Type", ct)
	importRec := httptest.NewRecorder()
	mux.ServeHTTP(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import: code %d body %s", importRec.Code, importRec.Body.String())
	}

	gotID, gotRate, gotTakeaway := itemTaxPair(t, dp.Db, "CAKE-FRESH")
	if gotID != handCreatedID {
		t.Fatalf("fresh (7,7) import landed on tax code %s, not the hand-created %s — churn: the merchant's own code was silently abandoned", gotID, handCreatedID)
	}
	if gotRate != 700 || gotTakeaway != nil {
		t.Fatalf("pair after import = (%d,%v), want (700,nil)", gotRate, gotTakeaway)
	}

	var taxCodesAfter int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&taxCodesAfter); err != nil {
		t.Fatal(err)
	}
	if taxCodesAfter != taxCodesBefore {
		t.Fatalf("import created a duplicate tax code for an already-existing (rate,takeaway) pair: %d -> %d", taxCodesBefore, taxCodesAfter)
	}
}

// Without the German tax plugin installed, core still does its whole job —
// correct-rate tax codes, items assigned — and the missing plugin is a valid
// outcome, not an error: no overrides row, no failed rows, no complaint.
func TestImport_TaxOverridesSkippedWhenPluginNotInstalled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, cafeTaxCSV, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	_, latteRate, latteTA := itemTaxPair(t, dp.Db, "T1")
	if latteRate != 1900 || latteTA == nil || *latteTA != 700 {
		t.Fatalf("Latte pair = (%d,%v), want (1900,&700) even with no plugin", latteRate, latteTA)
	}
	var n int
	if err := dp.Db.QueryRow(
		`SELECT COUNT(*) FROM plugin_settings WHERE key = 'takeaway_rate_overrides'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("no plugin installed: takeaway_rate_overrides must not be written, got %d rows", n)
	}
	audit := lastImportAudit(t, dp.Db)
	if got, ok := audit["failed"].(float64); !ok || got != 0 {
		t.Fatalf("audit failed = %v, want 0 — a missing tax plugin is not a row failure", audit["failed"])
	}
}

// ut-docs#531: installed-but-disabled must warn distinctly from
// not-installed-at-all (which stays silent, per TestImport_
// TaxOverridesSkippedWhenPluginNotInstalled above) — a merchant who
// imports before enabling the plugin needs to know the overrides step
// didn't run, not just see nothing.
func TestImport_TaxOverridesWarnsDistinctlyWhenPluginDisabled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	installTaxDePluginDisabled(t, dp.Db)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, cafeTaxCSV, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	// Tax codes still land correctly — only the plugin-setting step is affected.
	_, latteRate, latteTA := itemTaxPair(t, dp.Db, "T1")
	if latteRate != 1900 || latteTA == nil || *latteTA != 700 {
		t.Fatalf("Latte pair = (%d,%v), want (1900,&700) even with plugin disabled", latteRate, latteTA)
	}
	var n int
	if err := dp.Db.QueryRow(
		`SELECT COUNT(*) FROM plugin_settings WHERE key = 'takeaway_rate_overrides'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("plugin disabled: takeaway_rate_overrides must not be written, got %d rows", n)
	}
	// The distinct warning fires, not the generic "could not be saved" one
	// (that's reserved for an actual write failure).
	if !strings.Contains(resp, "installed but disabled") {
		t.Fatalf("summary should warn the plugin is installed but disabled, got: %s", resp)
	}
	if strings.Contains(resp, "could not be saved") {
		t.Fatalf("summary should use the disabled-plugin warning, not the generic failure one: %s", resp)
	}
	audit := lastImportAudit(t, dp.Db)
	if got, ok := audit["failed"].(float64); !ok || got != 0 {
		t.Fatalf("audit failed = %v, want 0 — a disabled tax plugin is not a row failure", audit["failed"])
	}
	if got, ok := audit["takeaway_overrides_plugin_disabled"].(bool); !ok || !got {
		t.Fatalf("audit takeaway_overrides_plugin_disabled = %v, want true", audit["takeaway_overrides_plugin_disabled"])
	}
}

// A merchant's existing hand-set override for a tax code id must never be
// clobbered by a re-import — merge adds only keys that aren't already there.
func TestImport_TaxOverridesMergePreservesExistingEntries(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	installTaxDePlugin(t, dp.Db)

	// The (19,7) code already exists AND the merchant has hand-tuned its
	// override to 550 (not the 700 this import would derive).
	ta7 := 700
	repo := data.NewCatalogRepo(dp.Db)
	existingCode, _, err := repo.FindOrCreateTaxCode(t.Context(), 1900, &ta7)
	if err != nil {
		t.Fatal(err)
	}
	pluginRepo := data.NewPluginRepo(dp.Db)
	handSet := fmt.Sprintf(`{"%s":550}`, existingCode)
	if err := pluginRepo.UpsertPluginSetting(t.Context(), "com.universaltill.tax-de", "takeaway_rate_overrides", handSet); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerImport(mux, dp)
	// Latte re-finds the existing (19,7) code; Smoothie's (19,5) pair is
	// new and must be merged in alongside the preserved hand-set entry.
	csv := "Name,SKU,Price,Tax rate,Takeaway tax\n" +
		"Latte,T1,3.50,19,7\n" +
		"Smoothie,T7,4.50,19,5\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	smoothieCode, _, smoothieTA := itemTaxPair(t, dp.Db, "T7")
	if smoothieTA == nil || *smoothieTA != 500 {
		t.Fatalf("Smoothie takeaway = %v, want 500", smoothieTA)
	}
	var raw string
	if err := dp.Db.QueryRow(
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var overrides map[string]int
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		t.Fatalf("overrides not valid JSON %q: %v", raw, err)
	}
	if overrides[existingCode] != 550 {
		t.Fatalf("hand-set override clobbered: %v, want %q kept at 550", overrides, existingCode)
	}
	if overrides[smoothieCode] != 500 {
		t.Fatalf("new pair not merged in: %v, want %q -> 500", overrides, smoothieCode)
	}
}

// If the stored overrides value is not valid JSON (a merchant's hand edit
// gone wrong), the best-effort settings step must not clobber it — the
// import itself still commits, and the summary line says the overrides step
// failed so someone knows to look at the plugin's settings.
func TestImport_TaxOverridesInvalidExistingJSONWarnsInSummary(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	installTaxDePlugin(t, dp.Db)
	pluginRepo := data.NewPluginRepo(dp.Db)
	if err := pluginRepo.UpsertPluginSetting(t.Context(), "com.universaltill.tax-de", "takeaway_rate_overrides", `{not json`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerImport(mux, dp)
	csv := "Name,SKU,Price,Tax rate,Takeaway tax\n" +
		"Latte,T1,3.50,19,7\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	// The item and its tax code still land — a settings hiccup never rolls
	// back committed inventory.
	_, latteRate, latteTA := itemTaxPair(t, dp.Db, "T1")
	if latteRate != 1900 || latteTA == nil || *latteTA != 700 {
		t.Fatalf("Latte pair = (%d,%v), want (1900,&700)", latteRate, latteTA)
	}
	// The broken hand-edited value is preserved, not overwritten.
	var raw string
	if err := dp.Db.QueryRow(
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `{not json` {
		t.Fatalf("invalid existing JSON must be left untouched, got %q", raw)
	}
	// And the summary tells the operator the overrides step failed.
	if !strings.Contains(resp, "takeaway tax overrides could not be saved") {
		t.Fatalf("summary should warn the overrides step failed, got: %s", resp)
	}
}

// A present-but-unparseable tax cell must warn and import the row at the
// till's default rate — never block the row, never drop the cell silently
// (compliance-sensitive, unlike stock).
func TestImport_UnparseableTaxCellWarnsButStillImports(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Tax rate,Takeaway tax\n" +
		"Oddball,T8,1.00,abc,\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var taxCodeID sql.NullString
	if err := dp.Db.QueryRow(`SELECT tax_code_id FROM items WHERE sku = 'T8'`).Scan(&taxCodeID); err != nil {
		t.Fatalf("item should still be created despite the bad tax cell: %v", err)
	}
	if taxCodeID.Valid {
		t.Fatalf("unparseable tax cell must leave the item on the default rate, got tax_code_id=%q", taxCodeID.String)
	}
	if !strings.Contains(resp, "abc") || !strings.Contains(resp, "percentage") {
		t.Fatalf("row should warn that the tax rate was not imported, got: %s", resp)
	}
	if !strings.Contains(resp, `class="row-warn"`) {
		t.Fatalf("bad-tax row must carry the warned visual treatment, got: %s", resp)
	}
}

// Review finding N1 (ut-docs#512, 2026-08-09): a row with a parseable
// takeaway-tax cell but a BLANK dine-in cell never reaches
// FindOrCreateTaxCode (gated on it.HasTax) — the item silently lands on the
// till's default rate with no warning at all, the exact class of silent
// VAT loss this card exists to prevent, just via an odd column combination.
func TestImport_TakeawayOnlyWithNoDineInRateWarns(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Tax rate,Takeaway tax\n" +
		"TakeawayOnly,T9,1.00,,7\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var taxCodeID sql.NullString
	if err := dp.Db.QueryRow(`SELECT tax_code_id FROM items WHERE sku = 'T9'`).Scan(&taxCodeID); err != nil {
		t.Fatalf("item should still be created: %v", err)
	}
	if taxCodeID.Valid {
		t.Fatalf("a takeaway-only row can't resolve a pair, must leave the item on the default rate, got tax_code_id=%q", taxCodeID.String)
	}
	if !strings.Contains(resp, `class="row-warn"`) {
		t.Fatalf("takeaway-only row must warn, not silently drop the rate — got: %s", resp)
	}
}

// A genuine DB-level failure creating the tax code fails the row (like a
// category-creation failure) — it must not silently import at the wrong rate.
func TestImport_TaxCodeCreationFailureFailsRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	// Force the tax_codes insert to fail deterministically for one rate.
	if _, err := dp.Db.Exec(`
CREATE TRIGGER import_test_taxcode_fail BEFORE INSERT ON tax_codes
WHEN NEW.rate_basis_points = 1234
BEGIN SELECT RAISE(ABORT, 'simulated tax code insert failure'); END;`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Tax rate\n" +
		"Cursed,T9,1.00,12.34\n"
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
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'T9'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("row must fail rather than import at the wrong rate (n=%d err=%v)", n, err)
	}
	if !strings.Contains(resp, "tax code could not be created") {
		t.Fatalf("row should report the tax code failure, got: %s", resp)
	}
}

// TestImport_FailedItemRowDoesNotContributeTakeawayOverride is ut-docs#535's
// acceptance test: a row whose tax pairing resolves fine but whose item
// insert then fails (an unexpected DB-level failure, same failure-injection
// shape as TestImport_UnexpectedItemInsertFailureRollsBackWholeRow) must not
// leave an inert entry in ut-plugin-tax-de's takeaway_rate_overrides for a
// tax code nothing actually ends up using — mirroring how
// stockWarning/stockRecorded are decided inside the loop but only acted on
// after tx.Commit() succeeds.
func TestImport_FailedItemRowDoesNotContributeTakeawayOverride(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	installTaxDePlugin(t, dp.Db)
	// Force the item insert itself to fail deterministically for one
	// specific SKU — the row's tax pairing (FindOrCreateTaxCode) runs and
	// succeeds first, so takeawayOverrides would (pre-fix) already carry
	// this row's candidate entry by the time the item insert then fails.
	if _, err := dp.Db.Exec(`
CREATE TRIGGER import_test_item_fail BEFORE INSERT ON items
WHEN NEW.sku = 'FORCE-FAIL'
BEGIN SELECT RAISE(ABORT, 'simulated item insert failure'); END;`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// A genuine (19,7) dine-in/takeaway pair — the exact shape that
	// populates takeawayOverrides — on the SKU whose item insert the
	// trigger above forces to fail.
	csv := "Name,SKU,Price,Tax rate,Takeaway tax\n" +
		"Latte,FORCE-FAIL,3.50,19,7\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	audit := lastImportAudit(t, dp.Db)
	if got, ok := audit["failed"].(float64); !ok || got != 1 {
		t.Fatalf("audit failed = %v, want 1 (the forced item insert failure)", audit["failed"])
	}
	if got, ok := audit["takeaway_overrides_set"].(float64); !ok || got != 0 {
		t.Fatalf("audit takeaway_overrides_set = %v, want 0 — the only candidate row failed", audit["takeaway_overrides_set"])
	}

	// The tax code itself may still have been created (FindOrCreateTaxCode
	// runs, and succeeds, before the item insert) — what must NOT happen is
	// an inert entry for it in the plugin's takeaway_rate_overrides setting.
	var raw sql.NullString
	if err := dp.Db.QueryRow(
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil && err != sql.ErrNoRows {
		t.Fatalf("query takeaway_rate_overrides: %v", err)
	}
	if raw.Valid && raw.String != "" {
		var overrides map[string]int
		if err := json.Unmarshal([]byte(raw.String), &overrides); err != nil {
			t.Fatalf("overrides not valid JSON %q: %v", raw.String, err)
		}
		if len(overrides) != 0 {
			t.Fatalf("overrides = %v, want none — the failed row's tax code must not be merged in", overrides)
		}
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

// TestImport_ShortPLUBarcodeImportsAttached is ut-docs#936's own acceptance
// criteria run through the full HTTP import path: under the shop's DEFAULT
// enabled symbologies (every permissive catch-all on, ADR-0059 §2), a
// legitimate 4-digit produce PLU now matches CODE128's structural catch-all
// via the shared internal/barcode registry and imports WITH its barcode
// attached — superseding this test's own pre-#936 shape (formerly
// TestImport_UnsupportedBarcodeShapeWarnsButStillImports), which asserted
// the OLD ad hoc 6-14-digit rule's rejection of this exact code. See
// TestImport_NoSymbologyMatchWarnsButStillImports below for the
// still-real "never silently drop" case, now correctly triggered by a
// narrowed enabled set rather than by code length.
func TestImport_ShortPLUBarcodeImportsAttached(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// 4011 is a canonical PLU-format produce code.
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
		t.Fatalf("item should be created: %v", err)
	}
	var storedBarcode string
	if err := dp.Db.QueryRow(`SELECT barcode FROM item_barcodes WHERE item_id = ?`, itemID).Scan(&storedBarcode); err != nil {
		t.Fatalf("4-digit PLU must attach as a barcode under the default enabled set (ut-docs#936): %v", err)
	}
	if storedBarcode != "4011" {
		t.Errorf("attached barcode = %q, want %q", storedBarcode, "4011")
	}
	if strings.Contains(resp, "not imported") {
		t.Fatalf("row must not warn a dropped barcode when it was actually attached, got: %s", resp)
	}

	// Round-trip coverage per ADR-0059/ut-docs#936's own acceptance
	// criteria: scan the same code on the sale screen and resolve the
	// right item, not just check the stored row.
	posRepo := data.NewPOSRepo(dp.Db)
	enabledIDs, err := data.NewSettingsRepo(dp.Db).EnabledBarcodeSymbologies(t.Context())
	if err != nil {
		t.Fatalf("enabled symbologies: %v", err)
	}
	line, _, ok := posRepo.ResolveScanLine(t.Context(), "4011", enabledIDs)
	if !ok {
		t.Fatal("scanning the imported PLU's own code must resolve, got no match")
	}
	if line.Name != "Bananas" {
		t.Errorf("resolved item name = %q, want %q", line.Name, "Bananas")
	}
}

// TestImport_NoSymbologyMatchWarnsButStillImports covers the still-real
// "never silently drop a barcode" case (ut-docs#293's original defect
// class) — now only reachable once a shop has narrowed its enabled
// symbology set away from the default catch-alls (ADR-0059 §2), rather
// than by any code shorter than 6 digits.
//
// If the BarcodeIssue plumbing (catimport recording why, or import_page.go
// surfacing it) were removed, this test would fail: the item would import
// with an empty barcode and no warning at all.
func TestImport_NoSymbologyMatchWarnsButStillImports(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	// Narrow the shop's enabled set to checksum-validated plain symbologies
	// only, disabling the permissive catch-alls (CODE128/CODE39/
	// INTERNAL_PLU) — the only way ADR-0059's registry-based matching ever
	// rejects a non-empty code.
	if err := data.NewSettingsRepo(dp.Db).SetEnabledBarcodeSymbologies(t.Context(),
		[]string{"EAN13", "EAN8", "UPCA", "UPCE", "GTIN14"}); err != nil {
		t.Fatalf("set enabled symbologies: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// 4011: 4 digits, matches none of EAN13/EAN8/UPCA/UPCE/GTIN14's fixed
	// lengths.
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
		t.Fatalf("item should still be created despite the unmatched barcode shape: %v", err)
	}
	var barcodeCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = ?`, itemID).Scan(&barcodeCount); err != nil {
		t.Fatalf("count barcodes: %v", err)
	}
	if barcodeCount != 0 {
		t.Fatalf("a barcode matching no enabled symbology must not be attached, got %d barcodes attached", barcodeCount)
	}
	if !strings.Contains(resp, "4011") || !strings.Contains(resp, "not imported") {
		t.Fatalf("row should warn that the 4011 barcode was not imported, got: %s", resp)
	}
}

// TestImport_EmbeddedSymbologyImportDecodesOnce guards ut-docs#936 review
// finding F1: catimport stores the decoded LookupKey (not the raw code) in
// it.Barcode, so import_page.go must pass the decoded symbology as an
// explicit BarcodeType to AddBarcode — otherwise AddBarcode re-runs
// registry inference on the already-zeroed LookupKey and mis-types it
// (a weight-embedded key re-decodes to CODE128) or, under a narrowed
// enabled set, fails the second match entirely and drops the barcode.
//
// If import_page.go stopped passing it.BarcodeType, this test fails: the
// stored barcode_type would be CODE128, not EAN13_WEIGHT_PREFIX2X.
func TestImport_EmbeddedSymbologyImportDecodesOnce(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	// Enable a weight-embedded symbology alongside plain EAN13 — the exact
	// #935 shop configuration under which F1's double-decode would bite.
	if err := data.NewSettingsRepo(dp.Db).SetEnabledBarcodeSymbologies(t.Context(),
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatalf("set enabled symbologies: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// 2012345067899: prefix 20 (weight-embedded), item code 12345, weight
	// 06789, valid EAN-13 check digit. Its zeroed LookupKey is 2012345000000.
	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Loose Apples,PLU-APPLE,2012345067899,0.00,Produce,0\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var itemID string
	if err := dp.Db.QueryRow(`SELECT id FROM items WHERE sku = 'PLU-APPLE'`).Scan(&itemID); err != nil {
		t.Fatalf("item should be created: %v", err)
	}
	var storedBarcode, storedType string
	if err := dp.Db.QueryRow(`SELECT barcode, barcode_type FROM item_barcodes WHERE item_id = ?`, itemID).
		Scan(&storedBarcode, &storedType); err != nil {
		t.Fatalf("weight-embedded barcode must be attached (decode-once, F1): %v", err)
	}
	if storedBarcode != "2012345000000" {
		t.Errorf("stored barcode (LookupKey) = %q, want %q", storedBarcode, "2012345000000")
	}
	if storedType != "EAN13_WEIGHT_PREFIX2X" {
		t.Errorf("stored barcode_type = %q, want EAN13_WEIGHT_PREFIX2X (a second decode would mis-type it CODE128)", storedType)
	}

	// Round-trip: scanning the actual weight label resolves the item.
	posRepo := data.NewPOSRepo(dp.Db)
	enabledIDs, err := data.NewSettingsRepo(dp.Db).EnabledBarcodeSymbologies(t.Context())
	if err != nil {
		t.Fatalf("enabled symbologies: %v", err)
	}
	line, dec, ok := posRepo.ResolveScanLine(t.Context(), "2012345067899", enabledIDs)
	if !ok {
		t.Fatal("scanning the weight-embedded label must resolve the imported item")
	}
	if line.Name != "Loose Apples" {
		t.Errorf("resolved item name = %q, want %q", line.Name, "Loose Apples")
	}
	if !dec.HasEmbeddedWeight {
		t.Errorf("scan decode should carry the embedded weight, got %+v", dec)
	}
}

// TestImport_UnexpectedItemInsertFailureRollsBackWholeRow covers ut-docs#310:
// a genuine unexpected DB-level failure in the item insert itself must fail
// the row and must never reach the barcode/stock-movement steps for it.
//
// Note on what this test does and doesn't prove: an item-insert failure
// already produced this exact outcome before #310 (pos.CreateItem's own
// autocommit insert failing meant nothing was ever created, so there was
// nothing to roll back) — this is a same-observable-behavior regression
// test for that path under the new transaction-based code, not evidence
// that the transaction itself changed anything here. See
// TestImport_StockRecordingFailureLaterInTheMovementRollsBackJustTheMovement
// below for a failure the transaction genuinely newly protects against.
func TestImport_UnexpectedItemInsertFailureRollsBackWholeRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	// Force the item insert itself to fail deterministically for one
	// specific SKU, simulating a genuine unexpected DB-level error (a
	// constraint, a full disk) rather than any of the expected business
	// cases (barcode conflict, unsupported shape, no stock location).
	if _, err := dp.Db.Exec(`
CREATE TRIGGER import_test_item_fail BEFORE INSERT ON items
WHEN NEW.sku = 'FORCE-FAIL'
BEGIN SELECT RAISE(ABORT, 'simulated item insert failure'); END;`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,FORCE-FAIL,5012345678900,1.50,Snacks,7\n"
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
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'FORCE-FAIL'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("item must not exist after an unexpected DB-level failure (n=%d err=%v)", n, err)
	}
	// "failed:" was the pre-i18n literal (ut-docs#310's original commit,
	// before ut-docs#303's translation pass landed on top of it); the row's
	// status is now the translated "item could not be created" message,
	// distinguished from a clean row by the row-warn/muted class rather
	// than an English substring (ut-docs#303).
	if !strings.Contains(resp, "item could not be created") || !strings.Contains(resp, `class="muted"`) {
		t.Fatalf("row should report as failed, not created/warned, got: %s", resp)
	}
	// The barcode and stock-movement steps run after the transaction
	// commits — a rolled-back item must mean neither was ever attempted.
	var barcodeCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = '5012345678900'`).Scan(&barcodeCount); err != nil {
		t.Fatalf("count barcodes: %v", err)
	}
	if barcodeCount != 0 {
		t.Fatalf("barcode must not be attached when the item it belongs to was rolled back, got %d rows", barcodeCount)
	}
	var movementCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE type = 'receive' AND quantity = 7`).Scan(&movementCount); err != nil {
		t.Fatalf("count stock movements: %v", err)
	}
	if movementCount != 0 {
		t.Fatalf("stock movement must not have been attempted for a rolled-back row, got %d rows", movementCount)
	}
}

// TestImport_StockRecordingFailureLaterInTheMovementRollsBackJustTheMovement
// covers the actual gap ut-docs#310's review found: RecordStockMovement
// writes FOUR statements (stock_movements insert, inventory update, a
// conditional inventory insert, then an audit insert), and — unlike the
// pre-#310 code path, which gave RecordStockMovement its own tx to fully
// roll back on any failure — this row's stock-movement call now runs
// against the row's OWN shared *sql.Tx. If that call still failed on a
// statement AFTER the first without its own compensating rollback, whatever
// already landed (e.g. the stock_movements row) would ride along into the
// row's tx.Commit() as an orphan: the row would read "stock not carried"
// while a stock_movements row silently existed with no matching inventory
// change. TestImport_StockRecordingFailureWarnsAndDoesNotPublish only
// forces failure on the FIRST statement (the stock_movements insert
// itself), so it can't catch this — nothing has been written yet at that
// point, orphan or not.
//
// This test forces the SECOND statement (the inventory UPDATE) to fail
// instead, via a trigger keyed to the exact post-update quantity this row
// produces (a fresh item's inventory row starts at 0, so "In stock" = 13
// updates it to exactly 13) — deterministic without needing to know the
// item's generated id ahead of time.
//
// If RecordStockMovementSavepoint's SAVEPOINT/ROLLBACK TO were removed
// (reverting the import loop to calling RecordStockMovement directly on the
// shared tx), this test would fail: the stock_movements row would survive
// into the committed row, orphaned, and the count assertion below would be 1
// instead of 0.
func TestImport_StockRecordingFailureLaterInTheMovementRollsBackJustTheMovement(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if _, err := dp.Db.Exec(`
CREATE TRIGGER import_test_inventory_update_fail BEFORE UPDATE ON inventory
WHEN NEW.quantity = 13
BEGIN SELECT RAISE(ABORT, 'simulated inventory update failure'); END;`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W-SAVEPOINT,,1.50,Snacks,13\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	// The item itself must still be created (this is a warn-and-continue
	// case, same as every other stock-recording failure) — with its
	// inventory row present but untouched (quantity 0, not 13 and not some
	// other partial value).
	var itemID string
	if err := dp.Db.QueryRow(`SELECT id FROM items WHERE sku = 'W-SAVEPOINT'`).Scan(&itemID); err != nil {
		t.Fatalf("item should still be created despite the forced inventory-update failure: %v", err)
	}
	if !strings.Contains(resp, "stock not carried") {
		t.Fatalf("row should warn that stock was not carried, got: %s", resp)
	}
	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(quantity,0) FROM inventory WHERE item_id = ?`, itemID).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 0 {
		t.Fatalf("inventory quantity must be untouched (0), got %v — a partial update survived", qty)
	}
	// The whole point: the stock_movements row from the failed attempt
	// must NOT have survived into the committed transaction as an orphan.
	var movementCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE item_id = ?`, itemID).Scan(&movementCount); err != nil {
		t.Fatalf("count stock movements: %v", err)
	}
	if movementCount != 0 {
		t.Fatalf("stock_movements must not retain an orphaned row when the update after it failed, got %d rows", movementCount)
	}
}

// ut-docs#924: GET /api/catalog/export used to leak the raw repo error via
// http.Error(w, err.Error(), ...) if ExportRows failed. Force a real query
// error (drop the underlying table) and assert the localized fallback shows,
// never the raw SQL text.
func TestExport_RepoErrorIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	if _, err := dp.Db.Exec(`DROP TABLE items`); err != nil {
		t.Fatalf("drop items table: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("export with broken items table = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not save the CSV") {
		t.Fatalf("export error body = %q, want the localized export-save-failed message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("export error body leaked raw SQL error text: %q", rec.Body.String())
	}
}
