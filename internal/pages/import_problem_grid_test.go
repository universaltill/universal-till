package pages

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catimport"
)

// ut-docs#601: the import preview's problem rows become interactive — the
// preview stages the uploaded file server-side, renders an include/skip
// checkbox per problem row (plus an inline correction field for the two
// forceable issue types, missing_name and bad_price), and the commit then
// re-reads the STAGED file and applies the submitted corrections through an
// explicit allow-list. The base never-previewed path must stay byte-for-byte
// today's behavior.

// problemCSV: row 0 = missing name (forceable, corrected via row_name_0),
// row 1 = bad price (forceable, corrected via row_price_1), row 2 = clean.
const problemCSV = "Name,SKU,Barcode,Price,Category\n" +
	",NN1,,1.50,Snacks\n" +
	"Pricey,BP1,,not-a-price,Snacks\n" +
	"Clean,CL1,,2.00,Snacks\n"

var stagedIDPattern = regexp.MustCompile(`name="staged_id" value="([0-9a-f]+)"`)

// multipartFields builds a multipart body with ONLY form fields — no file
// part at all. The staged commit path must read the staged server-side copy
// and never require a fresh upload.
func multipartFields(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
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

func postImport(t *testing.T, mux *http.ServeMux, body *bytes.Buffer, ct string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// previewAndExtractStagedID runs a preview of csv and returns the staged_id
// the response carries plus the raw response body.
func previewAndExtractStagedID(t *testing.T, mux *http.ServeMux, csv string) (string, string) {
	t.Helper()
	body, ct := multipartCSV(t, csv, nil)
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()
	m := stagedIDPattern.FindStringSubmatch(resp)
	if m == nil {
		t.Fatalf("preview response carries no staged_id hidden input: %s", resp)
	}
	return m[1], resp
}

// stagedPathFor reads the staged temp-file path behind an id straight from
// the package registry (same-package test).
func stagedPathFor(t *testing.T, id string) string {
	t.Helper()
	stagedCatalogMu.Lock()
	defer stagedCatalogMu.Unlock()
	e, ok := stagedCatalogUploads[id]
	if !ok {
		t.Fatalf("staged_id %q not in registry", id)
	}
	return e.path
}

func stagedRegistrySize() int {
	stagedCatalogMu.Lock()
	defer stagedCatalogMu.Unlock()
	return len(stagedCatalogUploads)
}

// resetStagedCatalog empties the package-global staging registry so a test's
// registry-size assertions aren't polluted by previews other tests in this
// binary staged and never committed (their temp files are removed too).
func resetStagedCatalog(t *testing.T) {
	t.Helper()
	clear := func() {
		stagedCatalogMu.Lock()
		defer stagedCatalogMu.Unlock()
		for k, e := range stagedCatalogUploads {
			_ = os.Remove(e.path)
			delete(stagedCatalogUploads, k)
		}
	}
	clear()
	t.Cleanup(clear)
}

func TestImport_PreviewStagesFileAndRendersProblemControls(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	id, resp := previewAndExtractStagedID(t, mux, problemCSV)

	// The staged copy exists on disk and is byte-identical to the upload.
	path := stagedPathFor(t, id)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("staged file unreadable: %v", err)
	}
	if string(got) != problemCSV {
		t.Fatalf("staged copy differs from upload:\n%q\nwant\n%q", got, problemCSV)
	}

	// Problem rows carry the include checkbox, keyed by their stable index.
	if !strings.Contains(resp, `name="row_include_0"`) {
		t.Fatalf("missing-name row (idx 0) has no include checkbox: %s", resp)
	}
	if !strings.Contains(resp, `name="row_include_1"`) {
		t.Fatalf("bad-price row (idx 1) has no include checkbox: %s", resp)
	}
	// The two forceable issue types get their inline correction field.
	if !strings.Contains(resp, `name="row_name_0"`) {
		t.Fatalf("missing-name row has no corrected-name input: %s", resp)
	}
	if !strings.Contains(resp, `name="row_price_1"`) {
		t.Fatalf("bad-price row has no corrected-price input: %s", resp)
	}
	// The clean row gets no controls at all.
	if strings.Contains(resp, `name="row_include_2"`) {
		t.Fatalf("clean row must not render an include checkbox: %s", resp)
	}
	// Controls are form-associated so the main form's submit carries them.
	if !strings.Contains(resp, `form="import-form"`) {
		t.Fatalf("problem-grid controls must be associated with #import-form: %s", resp)
	}
	// Preview still writes nothing.
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("preview wrote items (n=%d err=%v)", n, err)
	}
}

// A skipped-but-NOT-forceable row (barcode already in catalog) renders NO
// interactive controls at all — no include checkbox, no correction input —
// because ticking it could never do anything (forceableImportIssue refuses
// everything but missing_name/bad_price server-side). Rendering an inert
// checkbox with no feedback was the ut-docs#601 review's F3: the row keeps
// today's passive status text instead.
func TestImport_PreviewNonForceableRowHasNoControls(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmB','EX1','Existing',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('5012345678900','itmB',1)`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category\n" +
		"Clash,CL9,5012345678900,1.00,Snacks\n"
	_, resp := previewAndExtractStagedID(t, mux, csv)

	if strings.Contains(resp, `name="row_include_0"`) {
		t.Fatalf("non-forceable (duplicate-barcode) row must not render an inert include checkbox: %s", resp)
	}
	if strings.Contains(resp, `name="row_name_0"`) || strings.Contains(resp, `name="row_price_0"`) {
		t.Fatalf("non-forceable row must not render a correction input: %s", resp)
	}
	// The passive status text still explains WHY the row is skipped.
	if !strings.Contains(resp, "barcode already in catalog") {
		t.Fatalf("non-forceable row must still render its skip reason: %s", resp)
	}
}

func TestImport_CommitWithStagedIDAppliesAllowListedCorrections(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	id, _ := previewAndExtractStagedID(t, mux, problemCSV)
	path := stagedPathFor(t, id)

	// Commit with NO file part: the staged copy is the source of truth.
	body, ct := multipartFields(t, map[string]string{
		"commit":        "1",
		"staged_id":     id,
		"row_include_0": "1",
		"row_name_0":    "Named Now",
		"row_include_1": "1",
		"row_price_1":   "2.50",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("staged commit: code %d body %s", rec.Code, rec.Body.String())
	}

	// The corrected rows imported with the corrections applied.
	var name string
	var price int64
	if err := dp.Db.QueryRow(`SELECT name, base_price FROM items WHERE sku = 'NN1'`).Scan(&name, &price); err != nil {
		t.Fatalf("corrected missing-name row not imported: %v", err)
	}
	if name != "Named Now" || price != 150 {
		t.Fatalf("corrected row = (%q, %d), want (Named Now, 150)", name, price)
	}
	if err := dp.Db.QueryRow(`SELECT name, base_price FROM items WHERE sku = 'BP1'`).Scan(&name, &price); err != nil {
		t.Fatalf("corrected bad-price row not imported: %v", err)
	}
	if price != 250 {
		t.Fatalf("corrected price = %d minor units, want 250", price)
	}
	// The clean row imported as always.
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'CL1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("clean row not imported (n=%d err=%v)", n, err)
	}

	// The staged file is consumed: gone from disk and from the registry.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged file must be removed after commit, stat err=%v", err)
	}
	if got := stagedRegistrySize(); got != 0 {
		t.Fatalf("staged registry not cleaned up, %d entries left", got)
	}
}

// Defense in depth: "include" on any issue type outside the allow-list
// (here: barcode already in catalog, and a clean-but-skipped duplicate) must
// stay force-skipped no matter what the client submits.
func TestImport_CommitStagedNeverForcesNonForceableRows(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmB','EX1','Existing',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('5012345678900','itmB',1)`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// Row 0: barcode already in catalog. Row 1: SKU already in catalog.
	csv := "Name,SKU,Barcode,Price,Category\n" +
		"Clash,CL9,5012345678900,1.00,Snacks\n" +
		"SkuClash,EX1,,2.00,Snacks\n"
	id, _ := previewAndExtractStagedID(t, mux, csv)

	// A hostile client force-includes both, and even supplies correction
	// fields that mean nothing for these issue types.
	body, ct := multipartFields(t, map[string]string{
		"commit":        "1",
		"staged_id":     id,
		"row_include_0": "1",
		"row_name_0":    "Sneaky",
		"row_price_0":   "9.99",
		"row_include_1": "1",
		"row_name_1":    "Sneaky Too",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("staged commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 { // only the pre-seeded item
		t.Fatalf("non-forceable rows were forced through: %d items, want 1", n)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM item_barcodes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate barcode row must not land (n=%d err=%v)", n, err)
	}
}

// Include ticked but the required correction blank/unparseable: the row
// stays skipped, and the rendered status says why (translated).
func TestImport_CommitStagedValidatesCorrections(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	id, _ := previewAndExtractStagedID(t, mux, problemCSV)
	body, ct := multipartFields(t, map[string]string{
		"commit":        "1",
		"staged_id":     id,
		"row_include_0": "1",
		"row_name_0":    "   ", // blank after trim
		"row_include_1": "1",
		"row_price_1":   "wat",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("staged commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku IN ('NN1','BP1')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("invalid corrections must keep the rows skipped (n=%d err=%v)", n, err)
	}
	if !strings.Contains(resp, "no corrected name was given") {
		t.Fatalf("blank corrected name should surface the name_required message, got: %s", resp)
	}
	if !strings.Contains(resp, "wat") || !strings.Contains(resp, "could not be read as an amount") {
		t.Fatalf("unparseable corrected price should surface the price_invalid message with the raw value, got: %s", resp)
	}
	// The clean row still imports — a neighbour's bad correction never
	// blocks the rest of the file.
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'CL1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("clean row should still import (n=%d err=%v)", n, err)
	}
	// A blank price correction (distinct from unparseable) also stays skipped.
	id2, _ := previewAndExtractStagedID(t, mux, "Name,SKU,Price\nPricey2,BP2,nope\n")
	body2, ct2 := multipartFields(t, map[string]string{
		"commit": "1", "staged_id": id2, "row_include_0": "1", "row_price_0": "",
	})
	rec2 := postImport(t, mux, body2, ct2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("staged commit 2: code %d body %s", rec2.Code, rec2.Body.String())
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'BP2'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("blank corrected price must keep the row skipped (n=%d err=%v)", n, err)
	}
	if !strings.Contains(rec2.Body.String(), "no corrected price was given") {
		t.Fatalf("blank corrected price should surface the price_required message, got: %s", rec2.Body.String())
	}
}

// Commit with a staged_id the registry doesn't know (expired/bogus) fails
// with a translated message and writes nothing — it must NOT silently fall
// back to importing without the operator's corrections.
func TestImport_CommitUnknownStagedIDFailsCleanly(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartFields(t, map[string]string{
		"commit": "1", "staged_id": "deadbeefdeadbeefdeadbeefdeadbeef",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown staged_id: code %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("unknown staged_id should explain the preview expired, got: %s", rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unknown staged_id must write nothing (n=%d err=%v)", n, err)
	}
}

// Base-path regression pin (design point 5): a commit that never previewed —
// no staged_id — behaves EXACTLY as today, even when a client smuggles in
// override fields: the fresh upload is parsed, problem rows are skipped
// unconditionally, clean rows import.
func TestImport_CommitWithoutStagedIDIgnoresOverrides(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, problemCSV, map[string]string{
		"commit":        "1",
		"row_include_0": "1",
		"row_name_0":    "Should Not Land",
		"row_include_1": "1",
		"row_price_1":   "2.50",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("base commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku IN ('NN1','BP1')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("base path must skip problem rows regardless of override fields (n=%d err=%v)", n, err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'CL1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("base path must still import the clean row (n=%d err=%v)", n, err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE name = 'Should Not Land'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("base path applied a client-submitted correction (n=%d err=%v)", n, err)
	}
	// And no interactive controls render on a commit response.
	if strings.Contains(rec.Body.String(), `name="row_include_`) {
		t.Fatalf("commit response must not render problem-grid controls: %s", rec.Body.String())
	}
}

// A re-preview consumes the previous staged copy (the old id is submitted
// alongside the new file) so abandoned previews don't pile up per re-click.
func TestImport_RePreviewConsumesPreviousStagedCopy(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	firstID, _ := previewAndExtractStagedID(t, mux, problemCSV)
	firstPath := stagedPathFor(t, firstID)

	body, ct := multipartCSV(t, problemCSV, map[string]string{"staged_id": firstID})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-preview: code %d body %s", rec.Code, rec.Body.String())
	}
	m := stagedIDPattern.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("re-preview carries no staged_id: %s", rec.Body.String())
	}
	if m[1] == firstID {
		t.Fatalf("re-preview must stage a fresh copy, got the same id %s", firstID)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("previous staged copy must be removed on re-preview, stat err=%v", err)
	}
	if got := stagedRegistrySize(); got != 1 {
		t.Fatalf("registry should hold exactly the fresh copy, got %d entries", got)
	}
}

// A staged commit that hits ut-docs#970's currency-confirm gate (till
// currency never confirmed) must NOT destroy the staged copy or the
// operator's ticked overrides: the first response is the confirm prompt —
// still carrying the staged_id and the submitted override fields as hidden
// form-associated inputs (the originals lived inside the swapped
// #import-result div, so the prompt must re-emit them) — and the confirmed
// resubmit then lands back on the STAGED path, applies the overrides, and
// finally consumes the staged copy. Before this fix, the detour deleted the
// staged file and dropped its id, so the resubmit either 400'd on an unknown
// staged_id or (in a real browser, where no staged_id survived the swap)
// silently committed WITHOUT the operator's corrections.
func TestImport_StagedCommitSurvivesCurrencyConfirmDetour(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDepsWithCurrencyState(t, false)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	id, _ := previewAndExtractStagedID(t, mux, problemCSV)
	path := stagedPathFor(t, id)

	overrides := map[string]string{
		"row_include_0": "1",
		"row_name_0":    "Named Now",
		"row_include_1": "1",
		"row_price_1":   "2.50",
	}

	// First commit attempt: overrides ticked, currency never confirmed.
	fields := map[string]string{"commit": "1", "staged_id": id}
	for k, v := range overrides {
		fields[k] = v
	}
	body, ct := multipartFields(t, fields)
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("first staged commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	// The response is the currency-confirm prompt — not results, not an error.
	if !strings.Contains(resp, `name="confirm_currency"`) {
		t.Fatalf("unconfirmed-currency staged commit should render the confirm prompt, got: %s", resp)
	}
	// The prompt still carries the SAME staged_id...
	m := stagedIDPattern.FindStringSubmatch(resp)
	if m == nil {
		t.Fatalf("confirm prompt carries no staged_id hidden input — the resubmit would lose the staged copy: %s", resp)
	}
	if m[1] != id {
		t.Fatalf("confirm prompt staged_id = %s, want the original %s", m[1], id)
	}
	// ...and re-emits every submitted override field as a hidden input, since
	// the originals were destroyed by the #import-result swap.
	for k, v := range overrides {
		want := `name="` + k + `" value="` + v + `"`
		if !strings.Contains(resp, want) {
			t.Fatalf("confirm prompt must re-emit override field %s=%s as a hidden input, got: %s", k, v, resp)
		}
	}

	// The staged copy survived the detour: still registered, still on disk.
	if got := stagedRegistrySize(); got != 1 {
		t.Fatalf("staged registry after confirm detour: %d entries, want 1 (the copy must survive)", got)
	}
	if stagedPathFor(t, id) != path {
		t.Fatalf("staged_id %s re-registered under a different path", id)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged file must survive the confirm detour, stat err=%v", err)
	}
	// And nothing was written.
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("confirm detour wrote items (n=%d err=%v)", n, err)
	}

	// The "Confirm & Import" resubmit: commit=1 + staged_id + confirm_currency
	// + the re-emitted override fields, NO file part — exactly the set
	// hx-include gathers from #import-form after the fix.
	fields2 := map[string]string{"commit": "1", "staged_id": id, "confirm_currency": "GBP"}
	for k, v := range overrides {
		fields2[k] = v
	}
	body2, ct2 := multipartFields(t, fields2)
	rec2 := postImport(t, mux, body2, ct2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("confirmed staged resubmit: code %d body %s", rec2.Code, rec2.Body.String())
	}

	// The overrides WERE applied — the corrected rows actually imported.
	var name string
	var price int64
	if err := dp.Db.QueryRow(`SELECT name, base_price FROM items WHERE sku = 'NN1'`).Scan(&name, &price); err != nil {
		t.Fatalf("corrected missing-name row not imported after confirm detour: %v", err)
	}
	if name != "Named Now" || price != 150 {
		t.Fatalf("corrected row = (%q, %d), want (Named Now, 150)", name, price)
	}
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE sku = 'BP1'`).Scan(&price); err != nil {
		t.Fatalf("corrected bad-price row not imported after confirm detour: %v", err)
	}
	if price != 250 {
		t.Fatalf("corrected price = %d minor units, want 250", price)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'CL1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("clean row not imported after confirm detour (n=%d err=%v)", n, err)
	}

	// The staged copy is finally consumed once the commit actually completed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged file must be removed after the confirmed commit, stat err=%v", err)
	}
	if got := stagedRegistrySize(); got != 0 {
		t.Fatalf("staged registry not cleaned up after the confirmed commit, %d entries left", got)
	}
}

// ut-docs#601 review F1: on the .bkp path, a row that is BOTH missing a name
// AND duplicates another row's PLU in the same file used to be reported as
// missing_name only — a forceable issue — so a corrected-name override could
// smuggle an in-file duplicate past the guard that exists precisely to stop
// duplicates being forced through. Worse, when the duplicate's CLEAN twin
// came later in the file, the forced row won the SKU and the legitimate row
// failed with a generic item_failed. This is the genuine integration-level
// regression pin the review asked for (F2): it exercises the real .bkp
// preview → staged-commit path, not just the pure allow-list unit test.
func TestImport_BkpStagedCommitNeverForcesInFileDuplicatePLU(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// Base fixture rows: idx 0 = Latte (clean), idx 1 = deleted. Extra rows
	// cover every ordering of the duplicate-PLU + missing-name overlap:
	//  idx 2: clean "First A" on 40001, idx 3: missing-name dup of 40001
	//         (clean row FIRST — the parser's seen-set catches this one);
	//  idx 4: missing-name on 50001, idx 5: clean "Second B" on 50001
	//         (clean row LAST — only the commit-time guard can catch this);
	//  idx 6/7: two missing-name rows sharing 60001, both forced — only the
	//         first may land.
	zipBytes := buildBkpZipForPagesTestWithTaxRows(t, []bkpTaxRow{
		{ProductNumber: "40001", Name: "First A", Category: "Coffee", Price: 2.00},
		{ProductNumber: "40001", Name: "", Category: "Coffee", Price: 3.00},
		{ProductNumber: "50001", Name: "", Category: "Coffee", Price: 4.00},
		{ProductNumber: "50001", Name: "Second B", Category: "Coffee", Price: 5.00},
		{ProductNumber: "60001", Name: "", Category: "Coffee", Price: 6.00},
		{ProductNumber: "60001", Name: "", Category: "Coffee", Price: 7.00},
	})
	body, ct := multipartFile(t, "Backup 2026-08-09.bkp", zipBytes, nil) // preview
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("bkp preview: code %d body %s", rec.Code, rec.Body.String())
	}
	m := stagedIDPattern.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("bkp preview carries no staged_id: %s", rec.Body.String())
	}
	id := m[1]

	// The operator (or a hostile client) ticks every missing-name row and
	// supplies corrected names for all of them.
	body2, ct2 := multipartFields(t, map[string]string{
		"commit":        "1",
		"staged_id":     id,
		"row_include_3": "1",
		"row_name_3":    "Fixed Name",
		"row_include_4": "1",
		"row_name_4":    "Sneaky",
		"row_include_6": "1",
		"row_name_6":    "Twin One",
		"row_include_7": "1",
		"row_name_7":    "Twin Two",
	})
	rec2 := postImport(t, mux, body2, ct2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("bkp staged commit: code %d body %s", rec2.Code, rec2.Body.String())
	}
	resp := rec2.Body.String()

	// Exactly ONE row per duplicated PLU ever lands — and it's the clean one
	// where a clean one exists, regardless of file order.
	assertOne := func(sku, wantName string) {
		t.Helper()
		var n int
		if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = ?`, sku).Scan(&n); err != nil || n != 1 {
			t.Fatalf("PLU %s: %d items landed, want exactly 1 (err=%v)", sku, n, err)
		}
		var name string
		if err := dp.Db.QueryRow(`SELECT name FROM items WHERE sku = ?`, sku).Scan(&name); err != nil || name != wantName {
			t.Fatalf("PLU %s landed as %q (err=%v), want %q", sku, name, err, wantName)
		}
	}
	assertOne("40001", "First A")
	assertOne("50001", "Second B")
	assertOne("60001", "Twin One") // no clean twin: the first forced row wins, the second stays out
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE name IN ('Fixed Name','Sneaky','Twin Two')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a forced in-file duplicate landed in the DB (n=%d err=%v)", n, err)
	}

	// The rows that stayed out say WHY — the duplicate status, never a
	// generic item_failed from the SKU UNIQUE constraint firing late.
	if !strings.Contains(resp, "duplicate item number in this file") {
		t.Fatalf("skipped duplicate rows must show the duplicate_sku_in_file status: %s", resp)
	}
	if strings.Contains(resp, "item could not be created") {
		t.Fatalf("an in-file duplicate leaked into the write loop and failed on the DB constraint instead of being refused up front: %s", resp)
	}
}

// ut-docs#601 review F5: the currency-confirm block's FIRST early return (no
// confirm_currency yet) preserves the staged copy — but its other early
// returns (invalid currency code, settings-write failure, re-parse failure)
// used to destroy it, silently losing the operator's corrections. The
// invalid-code case is the easily-reachable one: a staged commit that
// submits a bogus confirm_currency must keep the staged copy alive so a
// subsequent legitimate resubmit still applies the corrections.
func TestImport_StagedCommitSurvivesInvalidCurrencyCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDepsWithCurrencyState(t, false)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	id, _ := previewAndExtractStagedID(t, mux, problemCSV)
	path := stagedPathFor(t, id)

	overrides := map[string]string{
		"row_include_0": "1",
		"row_name_0":    "Named Now",
		"row_include_1": "1",
		"row_price_1":   "2.50",
	}

	// Staged commit with corrections ticked and a bogus currency code.
	fields := map[string]string{"commit": "1", "staged_id": id, "confirm_currency": "XYZ"}
	for k, v := range overrides {
		fields[k] = v
	}
	body, ct := multipartFields(t, fields)
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus confirm_currency: code %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected currency code must write nothing (n=%d err=%v)", n, err)
	}

	// The staged copy SURVIVED the rejected attempt: still registered under
	// the same id, still on disk.
	if got := stagedRegistrySize(); got != 1 {
		t.Fatalf("staged registry after rejected currency code: %d entries, want 1 (the copy must survive)", got)
	}
	if stagedPathFor(t, id) != path {
		t.Fatalf("staged_id %s re-registered under a different path", id)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged file must survive a rejected confirm_currency, stat err=%v", err)
	}

	// A legitimate resubmit — same staged_id, same corrections, valid code —
	// still applies the operator's corrections.
	fields2 := map[string]string{"commit": "1", "staged_id": id, "confirm_currency": "GBP"}
	for k, v := range overrides {
		fields2[k] = v
	}
	body2, ct2 := multipartFields(t, fields2)
	rec2 := postImport(t, mux, body2, ct2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("legitimate resubmit after rejected code: code %d body %s", rec2.Code, rec2.Body.String())
	}
	var name string
	var price int64
	if err := dp.Db.QueryRow(`SELECT name, base_price FROM items WHERE sku = 'NN1'`).Scan(&name, &price); err != nil {
		t.Fatalf("corrected missing-name row not imported on the resubmit: %v", err)
	}
	if name != "Named Now" || price != 150 {
		t.Fatalf("corrected row = (%q, %d), want (Named Now, 150)", name, price)
	}
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE sku = 'BP1'`).Scan(&price); err != nil || price != 250 {
		t.Fatalf("corrected bad-price row not imported on the resubmit (price=%d err=%v)", price, err)
	}
	// And the successful commit finally consumed the copy.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged file must be removed after the successful commit, stat err=%v", err)
	}
	if got := stagedRegistrySize(); got != 0 {
		t.Fatalf("staged registry not cleaned up after the successful commit, %d entries left", got)
	}
}

// forceableImportIssue is an explicit allow-list: only missing_name and
// bad_price are ever forceable; anything else — including a future new issue
// code — defaults to skip-only.
func TestForceableImportIssueAllowList(t *testing.T) {
	if f, ok := forceableImportIssue(catimport.IssueMissingName); !ok || f != "name" {
		t.Fatalf("missing_name = (%q,%v), want (name,true)", f, ok)
	}
	if f, ok := forceableImportIssue(catimport.IssueBadPrice); !ok || f != "price" {
		t.Fatalf("bad_price = (%q,%v), want (price,true)", f, ok)
	}
	for _, issue := range []string{
		"", catimport.IssueSourceDeleted, catimport.IssueNotSellable,
		catimport.IssueDuplicateSKUInFile, "some_future_issue_code",
	} {
		if _, ok := forceableImportIssue(issue); ok {
			t.Fatalf("issue %q must not be forceable", issue)
		}
	}
}
