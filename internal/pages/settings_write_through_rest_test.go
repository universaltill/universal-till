package pages

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2979: the shop-wide settings writes that still went local-only
// after #2791/#2948 -- the receipt designer, the invoice seller details,
// the signing-device confirm/unpair, the printer card (per-till today, one
// batch anyway), the import's currency confirmation and the barcode-type
// checklist -- write through to the main till on an additional till. A
// failure writes nothing locally; on a main till nothing changes.

// newRestWriteThroughReplica is newSettingsSyncReplica plus the handlers
// this card routes through saveShopSettings.
func newRestWriteThroughReplica(t *testing.T, mainURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newSettingsSyncReplica(t, mainURL)
	registerReceiptDesigner(mux, dp)
	registerInvoices(mux, dp)
	registerPrintAPI(mux, dp)
	registerFiscalDeviceTR(mux, dp)
	return mux, dp
}

func hasAudit(t *testing.T, dp *common.Deps, entityType, entityID, action string) bool {
	t.Helper()
	ok, err := data.NewPOSRepo(dp.Db).HasAuditEntry(t.Context(), entityType, entityID, action)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

var receiptDesignForm = url.Values{
	"header1": {"Corner Shop"}, "header2": {"1 High St"}, "header3": {"VAT 123"},
	"footer": {"Thanks!"}, "show_sku": {"on"}, "show_tax": {"on"}, "show_logo": {"on"},
}

var receiptDesignWant = map[string]string{
	keyReceiptHeader1: "Corner Shop", keyReceiptHeader2: "1 High St", keyReceiptHeader3: "VAT 123",
	keyReceiptFooter: "Thanks!", keyReceiptShowSKU: "true", keyReceiptShowTax: "true",
	keyReceiptShowBarcode: "false", keyReceiptShowLogo: "true",
}

func TestSettingsWriteThrough_ReceiptDesignerOneBatchOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newRestWriteThroughReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/receipt-designer/save", receiptDesignForm, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("replica receipt-designer/save = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1 (one batch)", main.calls.Load())
	}
	for k, want := range receiptDesignWant {
		if got := mustSetting(t, main.dp, k); got != want {
			t.Errorf("main till %s = %q, want %q", k, got, want)
		}
		if got := mustSetting(t, dp, k); got != want {
			t.Errorf("replica %s = %q, want the mirrored %q", k, got, want)
		}
	}
	assertSettingSyncAudit(t, main.dp, "m1", keyReceiptFooter, "Thanks!", "Till 2")
	if !hasAudit(t, dp, "settings", "receipt", "receipt_design_saved") {
		t.Fatal("a successful save must still be audited on this till")
	}
}

func TestSettingsWriteThrough_ReceiptDesignerUnreachableWritesNothing(t *testing.T) {
	mux, dp := newRestWriteThroughReplica(t, deadPrimaryURL())

	wantSettingsFragmentRefused(t, postForm(mux, "/api/receipt-designer/save", receiptDesignForm, &mgrUser), settingsUnreachableEN)
	for k := range receiptDesignWant {
		if got := mustSetting(t, dp, k); got != "" {
			t.Fatalf("refused change wrote locally: %s = %q", k, got)
		}
	}
	if hasAudit(t, dp, "settings", "receipt", "receipt_design_saved") {
		t.Fatal("a refused save must not be audited")
	}
}

func TestSettingsWriteThrough_InvoiceSellerOneBatchOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newRestWriteThroughReplica(t, main.srv.URL)
	form := url.Values{"seller_name": {"Corner Shop Ltd"}, "seller_address": {"1 High St"}, "seller_vat_no": {"GB123"}}

	rec := postForm(mux, "/api/settings/invoice", form, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica settings/invoice = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1 (one batch)", main.calls.Load())
	}
	for k, want := range map[string]string{keyInvoiceSellerName: "Corner Shop Ltd", keyInvoiceSellerAddress: "1 High St", keyInvoiceSellerVATNo: "GB123"} {
		if got := mustSetting(t, main.dp, k); got != want {
			t.Errorf("main till %s = %q, want %q", k, got, want)
		}
		if got := mustSetting(t, dp, k); got != want {
			t.Errorf("replica %s = %q, want the mirrored %q", k, got, want)
		}
	}
	if !hasAudit(t, dp, "settings", "invoice", "invoice_seller_updated") {
		t.Fatal("a successful save must still be audited on this till")
	}
}

func TestSettingsWriteThrough_InvoiceSellerUnreachableWritesNothing(t *testing.T) {
	mux, dp := newRestWriteThroughReplica(t, deadPrimaryURL())

	rec := postForm(mux, "/api/settings/invoice", url.Values{"seller_name": {"X Ltd"}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("settings/invoice = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, keyInvoiceSellerName); got != "" {
		t.Fatalf("refused change wrote locally: %q", got)
	}
	if hasAudit(t, dp, "settings", "invoice", "invoice_seller_updated") {
		t.Fatal("a refused save must not be audited")
	}
}

// The printer card is per-till today: saved locally, never sent, even with
// a live main till.
func TestSettingsWriteThrough_PrinterStaysLocal(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newRestWriteThroughReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/printer", url.Values{"mode": {"network"}, "address": {"10.0.0.9:9100"}, "charset": {"utf8"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica settings/printer = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 0 {
		t.Fatalf("main till calls = %d, want 0 — printer settings are per-till", main.calls.Load())
	}
	if got := mustSetting(t, dp, keyPrinterMode); got != "network" {
		t.Fatalf("replica %s = %q, want network", keyPrinterMode, got)
	}
	if got := mustSetting(t, dp, keyPrinterAddress); got != "10.0.0.9:9100" {
		t.Fatalf("replica %s = %q", keyPrinterAddress, got)
	}
	if got := mustSetting(t, main.dp, keyPrinterMode); got != "" {
		t.Fatalf("main till %s = %q, want untouched", keyPrinterMode, got)
	}
}

// fiscalOwner holds fiscal_tse_override on both tills.
var fiscalOwner = auth.User{ID: "o1", Role: "admin", DisplayName: "Owner"}

func newFiscalDeviceReplica(t *testing.T, mainURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newRestWriteThroughReplica(t, mainURL)
	insertTestUserWithPIN(t, dp.Db, "o1", "o1", "Owner", "admin", "")
	seedActiveTaxTrPlugin(t, dp.Db, true)
	setCountry(t, dp, "TR")
	return mux, dp
}

func TestSettingsWriteThrough_FiscalDeviceConfirmUnpairOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	insertTestUserWithPIN(t, main.dp.Db, "o1", "o1", "Owner", "admin", "")
	mux, dp := newFiscalDeviceReplica(t, main.srv.URL)
	key := fiscal.SigningDeviceConfiguredKey("TR")

	rec := postForm(mux, "/api/fiscal-device/confirm", nil, &fiscalOwner)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("replica confirm = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 || mustSetting(t, main.dp, key) != "true" || mustSetting(t, dp, key) != "true" {
		t.Fatalf("confirm: main calls %d, main %q, replica %q; want 1/true/true", main.calls.Load(), mustSetting(t, main.dp, key), mustSetting(t, dp, key))
	}
	if !hasAudit(t, dp, "fiscal_device", "till", fiscalDeviceAuditConfirmed) {
		t.Fatal("a successful confirm must still be audited on this till")
	}

	rec = postForm(mux, "/api/fiscal-device/unpair", nil, &fiscalOwner)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("replica unpair = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 2 || mustSetting(t, main.dp, key) != "false" || mustSetting(t, dp, key) != "false" {
		t.Fatalf("unpair: main calls %d, main %q, replica %q; want 2/false/false", main.calls.Load(), mustSetting(t, main.dp, key), mustSetting(t, dp, key))
	}
}

// The main till decides with its own staff list (#2979 review): an actor it
// knows only as a manager lacks the owner-only fiscal_tse_override, so a
// replica's confirm is refused there and nothing is written or audited here.
func TestSettingsWriteThrough_FiscalDeviceMainRefusesNonOwner(t *testing.T) {
	main := newSettingsSyncMain(t)
	insertTestUserWithPIN(t, main.dp.Db, "o1", "o1", "Owner", "manager", "")
	mux, dp := newFiscalDeviceReplica(t, main.srv.URL)
	key := fiscal.SigningDeviceConfiguredKey("TR")

	rec := postForm(mux, "/api/fiscal-device/confirm", nil, &fiscalOwner)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("replica confirm by a main-till manager = %d %q, want 403", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, key); got != "" {
		t.Fatalf("main till wrote %s = %q for a non-owner", key, got)
	}
	if got := mustSetting(t, dp, key); got != "" {
		t.Fatalf("refused change wrote locally: %s = %q", key, got)
	}
	if hasAudit(t, dp, "fiscal_device", "till", fiscalDeviceAuditConfirmed) {
		t.Fatal("a refused change must not be audited")
	}
}

func TestSettingsWriteThrough_FiscalDeviceUnreachableWritesNothing(t *testing.T) {
	mux, dp := newFiscalDeviceReplica(t, deadPrimaryURL())
	key := fiscal.SigningDeviceConfiguredKey("TR")

	for _, path := range []string{"/api/fiscal-device/confirm", "/api/fiscal-device/unpair"} {
		rec := postForm(mux, path, nil, &fiscalOwner)
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), html.EscapeString(settingsUnreachableEN)) {
			t.Fatalf("%s = %d, want 502 with the unreachable message (body %q)", path, rec.Code, rec.Body.String())
		}
	}
	if got := mustSetting(t, dp, key); got != "" {
		t.Fatalf("refused change wrote locally: %s = %q", key, got)
	}
	if hasAudit(t, dp, "fiscal_device", "till", fiscalDeviceAuditConfirmed) || hasAudit(t, dp, "fiscal_device", "till", fiscalDeviceAuditUnpaired) {
		t.Fatal("a refused change must not be audited")
	}
}

// The import commit is refused on an additional till before the currency
// gate (ut-docs#1696), so the currency confirmation never reaches a local
// write there -- pinned so the write-through stays unreachable-safe.
func TestSettingsWriteThrough_ImportCurrencyConfirmNeverLocalOnReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	main := newSettingsSyncMain(t)
	dp := newImportTestDepsWithCurrencyState(t, false)
	setReplicaSettings(t, dp.Settings, main.srv.URL, syncSettingsBearer)
	mux := http.NewServeMux()
	registerImport(mux, dp)
	before := mustSetting(t, dp, common.KeyCurrency)

	body, ct := multipartCSV(t, germanCSV, map[string]string{"commit": "1", "confirm_currency": "IRT"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("replica import commit = %d, want 409", rec.Code)
	}
	if main.calls.Load() != 0 {
		t.Fatalf("main till calls = %d, want 0", main.calls.Load())
	}
	if got := mustSetting(t, dp, common.KeyCurrency); got != before {
		t.Fatalf("replica currency = %q, want unchanged %q", got, before)
	}
	if got := mustSetting(t, dp, common.KeyCurrencyConfirmed); got == "true" {
		t.Fatal("replica currency marked confirmed locally")
	}
}

func symbologySet(t *testing.T, raw string) []string {
	t.Helper()
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return ids
}

// Barcode types: the new set travels as one JSON value, both tills' caches
// follow at once, and the last-one refusal still answers without a call.
func TestSettingsWriteThrough_BarcodeSymbologyLandsOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	replicaRepo, mainRepo := data.NewSettingsRepo(dp.Db), data.NewSettingsRepo(main.dp.Db)
	// Prime both caches with the defaults.
	for _, r := range []*data.SettingsRepo{replicaRepo, mainRepo} {
		if ids, err := r.EnabledBarcodeSymbologies(t.Context()); err != nil || !slices.Contains(ids, "EAN13") {
			t.Fatalf("defaults = %v, %v", ids, err)
		}
	}

	rec := postForm(mux, "/api/settings/barcode-symbology", url.Values{"id": {"EAN13"}, "enabled": {"false"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica barcode-symbology = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if ids := symbologySet(t, mustSetting(t, main.dp, data.BarcodeEnabledSymbologiesKey)); slices.Contains(ids, "EAN13") || len(ids) == 0 {
		t.Fatalf("main till set = %v, want the defaults without EAN13", ids)
	}
	for name, r := range map[string]*data.SettingsRepo{"replica": replicaRepo, "main till": mainRepo} {
		ids, err := r.EnabledBarcodeSymbologies(t.Context())
		if err != nil || slices.Contains(ids, "EAN13") {
			t.Fatalf("%s cached set = %v (%v), want it without EAN13 — the cache must follow the write", name, ids, err)
		}
	}
	assertSettingSyncAudit(t, main.dp, "m1", data.BarcodeEnabledSymbologiesKey, "CODE128", "Till 2")
}

func TestSettingsWriteThrough_BarcodeSymbologyUnreachableWritesNothing(t *testing.T) {
	mux, dp := newSettingsSyncReplica(t, deadPrimaryURL())
	if err := dp.Settings.Set(t.Context(), data.BarcodeEnabledSymbologiesKey, `["EAN13","CODE128"]`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/barcode-symbology", url.Values{"id": {"EAN13"}, "enabled": {"false"}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("barcode-symbology = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, data.BarcodeEnabledSymbologiesKey); got != `["EAN13","CODE128"]` {
		t.Fatalf("refused change wrote locally: %q", got)
	}
	if hasAudit(t, dp, "settings", data.BarcodeEnabledSymbologiesKey, "barcode_symbology_changed") {
		t.Fatal("a refused change must not be audited")
	}
}

func TestSettingsWriteThrough_BarcodeSymbologyLastOneRefusedWithoutCall(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	if err := dp.Settings.Set(t.Context(), data.BarcodeEnabledSymbologiesKey, `["EAN13"]`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/barcode-symbology", url.Values{"id": {"EAN13"}, "enabled": {"false"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("last-one disable = %d %q, want 400", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 0 {
		t.Fatalf("main till calls = %d, want 0", main.calls.Load())
	}
	if got := mustSetting(t, dp, data.BarcodeEnabledSymbologiesKey); got != `["EAN13"]` {
		t.Fatalf("replica set = %q, want unchanged", got)
	}
}

// A generic write of the key (the admin pull's settings rows, a mirror, a
// main-till apply) must not leave the cached set stale.
func TestSettingsRepo_GenericWriteInvalidatesBarcodeCache(t *testing.T) {
	_, dp := newSettingsSyncReplica(t, deadPrimaryURL())
	repo := data.NewSettingsRepo(dp.Db)
	if _, err := repo.EnabledBarcodeSymbologies(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.SetMany(t.Context(), map[string]string{data.BarcodeEnabledSymbologiesKey: `["CODE128"]`}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := repo.EnabledBarcodeSymbologies(t.Context()); !slices.Equal(ids, []string{"CODE128"}) {
		t.Fatalf("after SetMany cached set = %v, want [CODE128]", ids)
	}
	if err := dp.Settings.Set(t.Context(), data.BarcodeEnabledSymbologiesKey, `["EAN8"]`); err != nil {
		t.Fatal(err)
	}
	if ids, _ := repo.EnabledBarcodeSymbologies(t.Context()); !slices.Equal(ids, []string{"EAN8"}) {
		t.Fatalf("after Set cached set = %v, want [EAN8]", ids)
	}
}
