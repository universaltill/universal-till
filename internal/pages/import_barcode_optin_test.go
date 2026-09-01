package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// TestImport_TickedOptIn_StaysStickyOnRePreview is a real bug found in
// independent review (2026-08-30, F1): barcodelessCatalog is judged against
// the CURRENT parse's result, and a re-preview submitted with the box
// already ticked parses WITH derived barcodes filled in — so every row now
// has a non-empty Barcode, barcodelessCatalog(res) flips to false, and the
// checkbox that produced the very rows on screen silently disappears. The
// next "Import" click then has nothing to submit and commits barcode-less,
// contradicting the preview the operator just approved. Fixed by keeping the
// checkbox rendered (and ticked) whenever the request itself already carries
// use_item_numbers_as_barcodes=1, not only when the parse result still looks
// barcode-less.
func TestImport_TickedOptIn_StaysStickyOnRePreview(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// First preview: establishes the staged copy.
	stagedID, firstBody := previewAndExtractStagedID(t, mux, barcodelessCSV)
	if !strings.Contains(firstBody, `name="use_item_numbers_as_barcodes"`) {
		t.Fatalf("first preview must offer the checkbox: %s", firstBody)
	}

	// Re-preview WITH the box already ticked — same file, carrying the
	// previous staged_id, exactly what clicking Preview a second time sends.
	body, ct := multipartCSV(t, barcodelessCSV, map[string]string{
		"staged_id": stagedID, "use_item_numbers_as_barcodes": "1",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-preview: code %d body %s", rec.Code, rec.Body.String())
	}
	second := rec.Body.String()
	if !strings.Contains(second, "30005") {
		t.Fatalf("re-preview must show the derived barcode in the table: %s", second)
	}
	if !strings.Contains(second, `name="use_item_numbers_as_barcodes" value="1" form="import-form" checked`) {
		t.Fatalf("re-preview must keep the checkbox rendered AND ticked, or the next Import silently loses the choice: %s", second)
	}
}

// TestImport_TickedOptIn_SurvivesCurrencyConfirmDetour is a real bug found by
// driving this exact sequence in an actual browser (ut-docs#1224 tester
// note), not derived from reading the code: on a till whose currency was
// never confirmed, ticking the barcode opt-in checkbox on Preview and then
// pressing Import hits the (unrelated, pre-existing) ut-docs#970
// currency-confirm gate FIRST — that gate's response fully replaces
// #import-result, including the checkbox itself, an input that only exists
// there because the preview render put it there. Before this fix,
// use_item_numbers_as_barcodes was not one of the fields
// renderImportCurrencyConfirm re-emits (confirmCarriedOverrideField), so the
// operator's tick was silently lost on the "Confirm & Import" resubmit — the
// item imported with no barcode despite the box being ticked. This test
// replicates the full staged, two-gate round trip a browser actually
// performs (preview → tick + Import, detoured by the currency gate → Confirm
// & Import) using only what the SERVER's own responses hand back, exactly as
// an unscripted browser would have nothing else to go on.
func TestImport_TickedOptIn_SurvivesCurrencyConfirmDetour(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDepsWithCurrencyState(t, false) // currency never confirmed
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// 1. Preview.
	stagedID, previewBody := previewAndExtractStagedID(t, mux, barcodelessCSV)
	if !strings.Contains(previewBody, `name="use_item_numbers_as_barcodes"`) {
		t.Fatalf("preview must offer the opt-in checkbox: %s", previewBody)
	}

	// 2. Tick the checkbox, press Import — hits the currency-confirm gate.
	body, ct := multipartFields(t, map[string]string{
		"commit": "1", "staged_id": stagedID, "use_item_numbers_as_barcodes": "1",
	})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	gateBody := rec.Body.String()
	if !strings.Contains(gateBody, "Confirm the currency for this import") {
		t.Fatalf("expected the currency-confirm gate on an unconfirmed till: %s", gateBody)
	}
	// This is the exact assertion that catches the bug: the gate's own
	// response must carry the operator's already-made choice forward.
	if !strings.Contains(gateBody, `name="use_item_numbers_as_barcodes" value="1"`) {
		t.Fatalf("currency-confirm gate must re-emit the ticked opt-in, or it's silently lost on resubmit: %s", gateBody)
	}
	newStagedID := stagedIDPattern.FindStringSubmatch(gateBody)
	if newStagedID == nil {
		t.Fatalf("currency-confirm gate must also re-emit staged_id: %s", gateBody)
	}

	// 3. "Confirm & Import" — a real browser's hx-include="#import-form"
	// picks up every form-associated field automatically (staged_id,
	// use_item_numbers_as_barcodes, confirm_currency); this test forwards
	// exactly what step 2's response handed back, nothing more.
	body, ct = multipartFields(t, map[string]string{
		"commit": "1", "staged_id": newStagedID[1], "confirm_currency": "GBP",
		"use_item_numbers_as_barcodes": "1",
	})
	rec = postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}

	var barcode string
	err := dp.Db.QueryRow(`
		SELECT ib.barcode FROM items i
		JOIN item_barcodes ib ON ib.item_id = i.id
		WHERE i.sku = '30005'`).Scan(&barcode)
	if err != nil {
		t.Fatalf("query committed item's barcode: %v (commit response: %s)", err, rec.Body.String())
	}
	if barcode != "30005" {
		t.Errorf("barcode = %q, want %q — the opt-in survived the currency detour", barcode, "30005")
	}
}

// TestImport_BarcodelessCatalog_PreviewOffersOptInCheckbox is ut-docs#1224's
// core case: a source with no barcode column at all gets offered an inline,
// unticked-by-default checkbox on preview (never a blocking round-trip —
// see barcodelessCatalog's own doc comment for why a checkbox, not a gate).
const barcodelessCSV = "Name,SKU,Price,Category\n" +
	"Cappuccino,30005,2.50,Coffee\n"

func TestImport_BarcodelessCatalog_PreviewOffersOptInCheckbox(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, barcodelessCSV, nil) // no commit: preview
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Cappuccino") {
		t.Fatalf("preview must still show parsed rows: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="use_item_numbers_as_barcodes"`) {
		t.Fatalf("barcode-less preview must offer the opt-in checkbox: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "checked") {
		t.Fatalf("the checkbox must be unticked by default (no checked attribute): %s", rec.Body.String())
	}
}

// TestImport_CatalogWithRealBarcodes_PreviewNeverOffersCheckbox: a file that
// already carries real barcodes must never see the opt-in — it would add
// friction to the common case for no reason.
const barcodedCSV = "Name,SKU,Barcode,Price,Category\n" +
	"Coca-Cola 330ml,10001,5449000000996,1.40,Drinks\n"

func TestImport_CatalogWithRealBarcodes_PreviewNeverOffersCheckbox(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, barcodedCSV, nil) // no commit: preview
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `name="use_item_numbers_as_barcodes"`) {
		t.Fatalf("a catalog with real barcodes must never offer the opt-in checkbox: %s", rec.Body.String())
	}
}

// TestImport_BarcodelessCatalog_DirectCommit_NoGate confirms this is never a
// gate: a never-previewed direct commit (no field, exactly the pre-#1224
// request shape) must import straight through — SKU-only, no barcode, same
// as before this card. This is also what protects every pre-existing
// single-shot-commit test in this package from this card's change.
func TestImport_BarcodelessCatalog_DirectCommit_NoGate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, barcodelessCSV, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	var itemCount, barcodeCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = '30005'`).Scan(&itemCount); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if itemCount != 1 {
		t.Fatalf("items with sku=30005 = %d, want 1 (a direct commit must never be blocked by this card)", itemCount)
	}
	if err := dp.Db.QueryRow(`
		SELECT COUNT(*) FROM item_barcodes ib
		JOIN items i ON i.id = ib.item_id
		WHERE i.sku = '30005'`).Scan(&barcodeCount); err != nil {
		t.Fatalf("count barcodes: %v", err)
	}
	if barcodeCount != 0 {
		t.Fatalf("barcode rows for sku=30005 = %d, want 0 (opt-in defaults off)", barcodeCount)
	}
}

// TestImport_BarcodelessCatalog_TickedCheckbox_DerivesBarcode: the operator
// ticks the checkbox before submitting — commit carries
// use_item_numbers_as_barcodes=1 and each item's SKU becomes its barcode.
func TestImport_BarcodelessCatalog_TickedCheckbox_DerivesBarcode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, barcodelessCSV, map[string]string{"commit": "1", "use_item_numbers_as_barcodes": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	var barcode, barcodeType string
	err := dp.Db.QueryRow(`
		SELECT ib.barcode, ib.barcode_type FROM items i
		JOIN item_barcodes ib ON ib.item_id = i.id
		WHERE i.sku = '30005'`).Scan(&barcode, &barcodeType)
	if err != nil {
		t.Fatalf("query committed item's barcode: %v", err)
	}
	if barcode != "30005" {
		t.Errorf("barcode = %q, want the item number %q", barcode, "30005")
	}
	if barcodeType == "" {
		t.Error("barcode_type must be set")
	}
}

// TestImport_ShopDefaultOn_FirstPreviewPreTicksCheckbox is ut-docs#1356's
// Settings toggle (CatalogImportBarcodeFromSKUDefaultKey): a shop that has
// turned it on sees the ut-docs#1224 opt-in checkbox PRE-TICKED the first
// time a barcode-less catalog is previewed — never a gate, still just a
// starting point for the SAME explicit-submit checkbox.
func TestImport_ShopDefaultOn_FirstPreviewPreTicksCheckbox(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if err := data.NewSettingsRepo(dp.Db).Set(t.Context(), data.CatalogImportBarcodeFromSKUDefaultKey, "1"); err != nil {
		t.Fatalf("seed shop default: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, barcodelessCSV, nil) // no commit, no staged_id: first render
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, `name="use_item_numbers_as_barcodes" value="1" form="import-form" checked`) {
		t.Fatalf("shop default on must pre-tick the checkbox on the first preview: %s", got)
	}
}

// TestImport_ShopDefaultOff_FirstPreviewLeavesCheckboxUnticked pins the
// absent/"0" default (unchanged ut-docs#1224 behaviour) — this is the SAME
// assertion TestImport_BarcodelessCatalog_PreviewOffersOptInCheckbox already
// makes with no setting seeded at all; this one seeds an explicit "0" to
// also cover a shop that toggled the setting on then back off.
func TestImport_ShopDefaultOff_FirstPreviewLeavesCheckboxUnticked(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if err := data.NewSettingsRepo(dp.Db).Set(t.Context(), data.CatalogImportBarcodeFromSKUDefaultKey, "0"); err != nil {
		t.Fatalf("seed shop default: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, barcodelessCSV, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "checked") {
		t.Fatalf("shop default off must leave the checkbox unticked: %s", rec.Body.String())
	}
}

// TestImport_ShopDefaultOn_ExplicitUntickOnCommit_NeverAppliesBarcodes is the
// brief's key guardrail: the shop default only ever changes what the
// checkbox SHOWS, never what an import actually does. The operator's own
// explicit choice — here, unticking the pre-ticked box before pressing
// Import — must always win, even with the shop default on.
func TestImport_ShopDefaultOn_ExplicitUntickOnCommit_NeverAppliesBarcodes(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if err := data.NewSettingsRepo(dp.Db).Set(t.Context(), data.CatalogImportBarcodeFromSKUDefaultKey, "1"); err != nil {
		t.Fatalf("seed shop default: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// First preview: pre-ticked by the shop default.
	stagedID, firstBody := previewAndExtractStagedID(t, mux, barcodelessCSV)
	if !strings.Contains(firstBody, `name="use_item_numbers_as_barcodes" value="1" form="import-form" checked`) {
		t.Fatalf("first preview must be pre-ticked by the shop default: %s", firstBody)
	}

	// The operator unticks it and presses Import: commit carries staged_id
	// but NOT use_item_numbers_as_barcodes at all — exactly what a real
	// unticked checkbox submits.
	body, ct := multipartFields(t, map[string]string{"commit": "1", "staged_id": stagedID})
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}

	var barcodeCount int
	if err := dp.Db.QueryRow(`
		SELECT COUNT(*) FROM item_barcodes ib
		JOIN items i ON i.id = ib.item_id
		WHERE i.sku = '30005'`).Scan(&barcodeCount); err != nil {
		t.Fatalf("count barcodes: %v", err)
	}
	if barcodeCount != 0 {
		t.Fatalf("barcode rows for sku=30005 = %d, want 0 — the operator's explicit untick must win over the shop default", barcodeCount)
	}
}
