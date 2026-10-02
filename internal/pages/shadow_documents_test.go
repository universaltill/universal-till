package pages

// ADR-0124 (ut-docs#3169): a shadow till in a market whose shipped
// country_settings row says shadow_customer_documents='forbidden' records
// every sale and produces no customer document — on every print path, the
// post-tender view and the designer's test receipt. These tests drive the
// real handlers against a fake network printer that counts jobs, for a
// forbidden market (PT is the one shipped today) and for unaffected
// markets (GB, DE).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// shadowDocsHarness is newFiscalTestDeps (a real migrated DB, the tender
// handler) plus every surface this card gates, on one mux, with a fake
// network receipt printer.
type shadowDocsHarness struct {
	t       *testing.T
	mux     *http.ServeMux
	dp      *common.Deps
	printer *fakeKitchenPrinter
}

func newShadowDocsHarness(t *testing.T, country, policy string) *shadowDocsHarness {
	t.Helper()
	mux, dp := newFiscalTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	registerPrintAPI(mux, dp)
	registerReceiptDesigner(mux, dp)
	registerIndex(mux, dp)
	registerRefund(mux, dp)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	p := newFakeKitchenPrinter(t)
	ctx := context.Background()
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterMode:          "network",
		keyPrinterAddress:       p.addr,
		keyPrinterReceiptPolicy: policy,
		keyStoreCountry:         country,
	}); err != nil {
		t.Fatalf("seed printer settings: %v", err)
	}
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = country })
	return &shadowDocsHarness{t: t, mux: mux, dp: dp, printer: p}
}

func (h *shadowDocsHarness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// sell rings one Apple and tenders it, returning the tender response and
// the new sale's receipt number.
func (h *shadowDocsHarness) sell() (*httptest.ResponseRecorder, string) {
	h.t.Helper()
	if _, err := h.dp.Engine.Scan("ABC"); err != nil {
		h.t.Fatal(err)
	}
	rec := fiscalTender(h.t, h.mux)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	var receiptNo string
	if err := h.dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE sale_type = 'sale' ORDER BY rowid DESC LIMIT 1`).Scan(&receiptNo); err != nil {
		h.t.Fatalf("read receipt_no: %v", err)
	}
	return rec, receiptNo
}

func refusedText(t *testing.T) string {
	t.Helper()
	msg := httpx.T("en", "shadow_documents.refused")
	if msg == "shadow_documents.refused" || msg == "" {
		t.Fatal("shadow_documents.refused is missing from en.json")
	}
	return msg
}

// --- the helper and printerConfig -----------------------------------------

func TestCustomerDocumentsSuppressed_FollowsTheShopsCountry(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	for country, want := range map[string]bool{"PT": true, " pt ": true, "GB": false, "DE": false, "": false} {
		dp.UpdateState(func(s *common.RuntimeState) { s.Country = country })
		if got := customerDocumentsSuppressed(ctx, dp); got != want {
			t.Errorf("country %q: suppressed = %v, want %v", country, got, want)
		}
	}
	// A market whose STORED row says forbidden is suppressed too: the
	// enforcement is the data, not a code list.
	if _, err := dp.Db.Exec(`UPDATE country_settings SET shadow_customer_documents = 'forbidden' WHERE code = 'FR'`); err != nil {
		t.Fatal(err)
	}
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "FR" })
	if !customerDocumentsSuppressed(ctx, dp) {
		t.Fatal("FR with a stored forbidden row should be suppressed")
	}
}

// ADR-0124 §3 / ADR-0089 amendment: suppression forces the effective
// policy to never, after the plugin clamp — so even a plugin-forced
// "always" cannot re-enable printing.
func TestPrinterConfig_ShadowMarketForcesNeverOverPluginAlways(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterMode: "network", keyPrinterAddress: "unused.invalid:9100",
		keyPrinterReceiptPolicy: receiptPolicyAsk,
	}); err != nil {
		t.Fatal(err)
	}
	subscribeReceiptPolicy(t, dp.Db, `{"allowed_policies":["always"]}`, nil)

	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PT" })
	cfg := printerConfig(ctx, dp)
	if cfg.ReceiptPolicy != receiptPolicyNever || cfg.AutoPrint {
		t.Fatalf("PT: policy=%q AutoPrint=%v, want never/false", cfg.ReceiptPolicy, cfg.AutoPrint)
	}

	// DE (and every other market): the plugin-forced always still prints.
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	cfg = printerConfig(ctx, dp)
	if cfg.ReceiptPolicy != receiptPolicyAlways || !cfg.AutoPrint {
		t.Fatalf("DE: policy=%q AutoPrint=%v, want the plugin-forced always/true", cfg.ReceiptPolicy, cfg.AutoPrint)
	}
}

// --- the printReceipt choke point and the async (tender/refund) path -------

func TestPrintReceipt_ShadowMarketReturnsSentinelAndPrintsNothing(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	seedReceiptSale(t, h.dp, "sale-sd-1", "R-SD-1", "sale", "", 120, 0, 0)
	err := printReceipt(context.Background(), h.dp, "R-SD-1")
	if !errors.Is(err, errCustomerDocumentsSuppressed) {
		t.Fatalf("printReceipt err = %v, want errCustomerDocumentsSuppressed", err)
	}
	if got := h.printer.Settle(0); len(got) != 0 {
		t.Fatalf("printer received %d jobs, want 0", len(got))
	}
}

// The async path (tender auto-print, refund auto-print) must neither print
// nor record a print failure: a withheld document is not a broken printer.
func TestPrintReceiptAsync_ShadowMarketNeverCallsThePrinter(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	seedReceiptSale(t, dp, "sale-sd-a", "R-SD-A", "sale", "", 120, 0, 0)
	if err := dp.Settings.SetMany(context.Background(), map[string]string{
		keyPrinterMode: "network", keyPrinterAddress: "unused.invalid:9100",
		keyPrinterReceiptPolicy: receiptPolicyAlways,
	}); err != nil {
		t.Fatal(err)
	}
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PT" })
	restore := printReceiptFn
	t.Cleanup(func() { printReceiptFn = restore })
	called := 0
	printReceiptFn = func(context.Context, *common.Deps, string) error { called++; return nil }

	printReceiptAsync(dp, "R-SD-A", "")
	dp.WaitForAsyncWork()
	if called != 0 {
		t.Fatalf("printReceiptFn called %d times, want 0", called)
	}
	var failures int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id = 'R-SD-A' AND action = 'print_failed'`).Scan(&failures)
	if failures != 0 {
		t.Fatalf("got %d print_failed rows, want 0", failures)
	}
}

// Defence in depth: if printerConfig ever stops forcing "never" (or the
// country changes between the config read and the print), printReceipt's
// own refusal reaches printReceiptAsync as errCustomerDocumentsSuppressed.
// That refusal is not a broken printer, so it must leave no print_failed
// audit row and no /orders warning flag.
func TestPrintReceiptAsync_SuppressedRefusalIsNotAPrintFailure(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	seedReceiptSale(t, dp, "sale-sd-b", "R-SD-B", "sale", "", 120, 0, 0)
	if err := dp.Settings.SetMany(context.Background(), map[string]string{
		keyPrinterMode: "network", keyPrinterAddress: "unused.invalid:9100",
		keyPrinterReceiptPolicy: receiptPolicyAlways,
	}); err != nil {
		t.Fatal(err)
	}
	// An allowed market, so the config says AutoPrint and the async path
	// really reaches printReceiptFn.
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "GB" })
	restore := printReceiptFn
	t.Cleanup(func() { printReceiptFn = restore })
	called := 0
	printReceiptFn = func(context.Context, *common.Deps, string) error { called++; return errCustomerDocumentsSuppressed }

	printReceiptAsync(dp, "R-SD-B", "")
	dp.WaitForAsyncWork()
	if called != 1 {
		t.Fatalf("printReceiptFn called %d times, want 1 (fixture: the async path must reach it)", called)
	}
	var failures int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id = 'R-SD-B' AND action = 'print_failed'`).Scan(&failures); err != nil {
		t.Fatalf("count print_failed: %v", err)
	}
	if failures != 0 {
		t.Fatalf("got %d print_failed rows for a withheld document, want 0", failures)
	}
	var flagged string
	if err := dp.Db.QueryRow(`SELECT COALESCE(receipt_print_failed_at, '') FROM sales WHERE receipt_no = 'R-SD-B'`).Scan(&flagged); err != nil {
		t.Fatalf("read receipt_print_failed_at: %v", err)
	}
	if flagged != "" {
		t.Fatalf("receipt flagged as a print failure (%q) on a withheld document", flagged)
	}
}

// --- the tender: no print, no receipt markup, no ask prompt ----------------

func TestTender_ShadowMarketRendersSaleRecordedAndPrintsNothing(t *testing.T) {
	for _, policy := range []string{receiptPolicyAlways, receiptPolicyAsk} {
		t.Run(policy, func(t *testing.T) {
			h := newShadowDocsHarness(t, "PT", policy)
			rec, receiptNo := h.sell()
			body := rec.Body.String()
			if !strings.Contains(body, `data-testid="sale-recorded"`) {
				t.Fatalf("expected the sale-recorded card, got: %s", body)
			}
			if !strings.Contains(body, receiptNo) {
				t.Fatalf("the card must show the receipt number %s for refunds/journal", receiptNo)
			}
			for _, bad := range []string{`class="receipt-card"`, `id="receipt-ask"`, "/api/print/receipt/", "window.print()", `class="receipt-lines"`, `class="receipt-payments"`, `class="receipt-wrap"`} {
				if strings.Contains(body, bad) {
					t.Fatalf("shadow tender response carries receipt markup %q: %s", bad, body)
				}
			}
			// The sale itself is recorded in full.
			if n := countSales(t, h.dp); n != 1 {
				t.Fatalf("sales = %d, want 1", n)
			}
			if got := h.printer.Settle(0); len(got) != 0 {
				t.Fatalf("printer received %d jobs, want 0", len(got))
			}
		})
	}
}

// Control: a GB shop is unchanged — receipt markup and an auto-print.
func TestTender_AllowedMarketStillGetsTheReceipt(t *testing.T) {
	h := newShadowDocsHarness(t, "GB", receiptPolicyAlways)
	rec, _ := h.sell()
	body := rec.Body.String()
	if strings.Contains(body, `data-testid="sale-recorded"`) || !strings.Contains(body, `class="receipt-card"`) {
		t.Fatalf("GB tender must render the normal receipt, got: %s", body)
	}
	if got := h.printer.Settle(1); len(got) != 1 {
		t.Fatalf("GB printer received %d jobs, want 1", len(got))
	}
}

// --- manual print / reprint ------------------------------------------------

func TestReprint_ShadowMarketIs451WithLocalizedRefusal(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyNever)
	seedReceiptSale(t, h.dp, "sale-sd-r", "R-SD-R", "sale", "", 120, 0, 0)
	rec := h.do(http.MethodPost, "/api/print/receipt/R-SD-R", "")
	if rec.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("reprint status = %d, want 451: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), refusedText(t)) {
		t.Fatalf("reprint body lacks the localized refusal: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html (journal_detail swaps it in)", ct)
	}
	if got := h.printer.Settle(0); len(got) != 0 {
		t.Fatalf("printer received %d jobs, want 0", len(got))
	}
	// A refusal is not a printer failure: no /orders warning.
	var flagged string
	_ = h.dp.Db.QueryRow(`SELECT COALESCE(receipt_print_failed_at, '') FROM sales WHERE receipt_no = 'R-SD-R'`).Scan(&flagged)
	if flagged != "" {
		t.Fatalf("receipt flagged as a print failure (%q) on a shadow refusal", flagged)
	}
}

func TestReprint_AllowedMarketStillPrints(t *testing.T) {
	h := newShadowDocsHarness(t, "GB", receiptPolicyNever)
	seedReceiptSale(t, h.dp, "sale-sd-g", "R-SD-G", "sale", "", 120, 0, 0)
	rec := h.do(http.MethodPost, "/api/print/receipt/R-SD-G", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GB reprint status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := h.printer.Settle(1); len(got) != 1 {
		t.Fatalf("GB printer received %d jobs, want 1", len(got))
	}
}

// --- refund ----------------------------------------------------------------

func TestRefund_ShadowMarketAutoPrintsNothing(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	_, receiptNo := h.sell()
	rec := h.do(http.MethodPost, "/api/refund", "receipt="+receiptNo+"&qty_0=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	var returns int
	_ = h.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`).Scan(&returns)
	if returns != 1 {
		t.Fatalf("returns = %d, want the refund recorded", returns)
	}
	if got := h.printer.Settle(0); len(got) != 0 {
		t.Fatalf("printer received %d jobs after sale+refund, want 0", len(got))
	}
}

// --- designer test print vs plain printer test -----------------------------

func TestReceiptDesignerTest_ShadowMarketIsRefused_PrinterTestStillPrints(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	rec := h.do(http.MethodPost, "/api/receipt-designer/test", "")
	if rec.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("designer test status = %d, want 451: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), refusedText(t)) {
		t.Fatalf("designer test body lacks the localized refusal: %s", rec.Body.String())
	}
	if got := h.printer.Settle(0); len(got) != 0 {
		t.Fatalf("designer test reached the printer (%d jobs)", len(got))
	}

	rec = h.do(http.MethodPost, "/api/print/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("printer test status = %d, want 200 (a shadow shop still sets up printers): %s", rec.Code, rec.Body.String())
	}
	if got := h.printer.Settle(1); len(got) != 1 {
		t.Fatalf("printer test: printer received %d jobs, want 1", len(got))
	}
}

// --- the sell-screen banner ------------------------------------------------

func TestSellScreen_ShadowDocumentsBannerOnlyWhenSuppressed(t *testing.T) {
	for country, want := range map[string]bool{"PT": true, "GB": false} {
		t.Run(country, func(t *testing.T) {
			h := newShadowDocsHarness(t, country, receiptPolicyAlways)
			rec := h.do(http.MethodGet, "/", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /: %d", rec.Code)
			}
			body := rec.Body.String()
			if got := strings.Contains(body, `data-testid="shadow-documents-banner"`); got != want {
				t.Fatalf("banner present = %v, want %v", got, want)
			}
			if want && !strings.Contains(body, httpx.T("en", "shadow_documents.banner")) {
				t.Fatal("banner text missing")
			}
		})
	}
}

// --- review round 1 (ut-docs#3169) -----------------------------------------

// ADR-0124 §2 "no lift": neither fiscal.system_of_record nor a per-country
// signing-device posture key turns a forbidden market's documents back on.
// Driven through the real settings store and the tender, so it would fail
// if the gate ever started reading either key.
func TestTender_ShadowMarketSystemOfRecordAndPostureKeyDoNotLift(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	if err := h.dp.Settings.SetMany(context.Background(), map[string]string{
		fiscal.KeySystemOfRecord:                "true",
		fiscal.SigningDeviceConfiguredKey("PT"): "true",
	}); err != nil {
		t.Fatal(err)
	}
	if !customerDocumentsSuppressed(context.Background(), h.dp) {
		t.Fatal("PT with system_of_record + the posture key must stay suppressed")
	}
	rec, _ := h.sell()
	if !strings.Contains(rec.Body.String(), `data-testid="sale-recorded"`) {
		t.Fatalf("expected the sale-recorded card, got: %s", rec.Body.String())
	}
	if got := h.printer.Settle(0); len(got) != 0 {
		t.Fatalf("printer received %d jobs, want 0", len(got))
	}
}

// Invoices are customer documents too (ADR-0124 §3). Issuing one in a
// forbidden market is refused with the same 451, and nothing is stored or
// printed. (Rendering an older invoice and self-order are #3170.)
func TestInvoiceIssue_ShadowMarketIs451AndIssuesNothing(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PT" })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/invoices/issue", strings.NewReader("receipt_no=R001&customer_name=Jane+Doe"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("invoice issue status = %d, want 451: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), refusedText(t)) {
		t.Fatalf("invoice issue body lacks the localized refusal: %s", rec.Body.String())
	}
	if _, found, err := data.NewInvoiceRepo(dp.Db).BySale(context.Background(), "sale1", "invoice"); err != nil || found {
		t.Fatalf("an invoice was stored in a shadow market (found=%v err=%v)", found, err)
	}
}

// A refund of a sale invoiced before the gate applied must not auto-issue a
// credit note (a customer document) in a forbidden market.
func TestMaybeIssueCreditNote_ShadowMarketIssuesNone(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	ctx := context.Background()
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	sale, _, err := data.NewPOSRepo(dp.Db).GetSaleDetail(ctx, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1"); err != nil {
		t.Fatal(err)
	}
	seedInvoiceableSale(t, dp, "return1", "R002", 120, 20)
	if _, err := dp.Db.ExecContext(ctx, `UPDATE sales SET sale_type = 'return' WHERE id = 'return1'`); err != nil {
		t.Fatal(err)
	}

	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PT" })
	maybeIssueCreditNote(ctx, dp, "R002", "sale1", "user1")
	if _, found, err := data.NewInvoiceRepo(dp.Db).BySale(ctx, "return1", "credit_note"); err != nil || found {
		t.Fatalf("a credit note was issued in a shadow market (found=%v err=%v)", found, err)
	}
}

// The settings page shows the shop's own stored policy, not the gate's
// forced "never" — otherwise saving any printer setting would silently
// overwrite the stored choice (review m2). The page explains the gate.
func TestSettingsPage_ShadowMarketShowsStoredPolicyAndExplains(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	plugins.SharedBus(d.Db).ResetSubscribers()
	ctx := context.Background()
	if err := d.Settings.SetMany(ctx, map[string]string{
		keyPrinterReceiptPolicy: receiptPolicyAsk,
		common.KeyCountry:       "PT",
	}); err != nil {
		t.Fatal(err)
	}
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "PT" })
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="ask" selected`) {
		t.Fatal("PT shop: the stored policy (ask) must stay pre-selected, not the gate's forced never")
	}
	if !strings.Contains(body, `data-testid="receipt-policy-shadow-note"`) {
		t.Fatal("PT shop: the receipt policy control must explain that shadow mode withholds receipts")
	}

	d.UpdateState(func(s *common.RuntimeState) { s.Country = "GB" })
	if err := d.Settings.Set(ctx, common.KeyCountry, "GB"); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser))
	if strings.Contains(rec.Body.String(), `data-testid="receipt-policy-shadow-note"`) {
		t.Fatal("GB shop must not see the shadow note")
	}
}

// A settings-read failure in a forbidden market is not a print failure:
// that receipt could never print, so no print_failed row and no /orders
// warning (review m3). The GB behaviour is pinned by
// TestAsyncPrintFailureIsRecordedWhenSettingsReadFails.
func TestPrintReceiptAsync_ShadowMarketSettingsReadErrorIsNotAPrintFailure(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	seedReceiptSale(t, dp, "sale-sd-e", "R-SD-E", "sale", "", 120, 0, 0)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PT" })
	if _, err := dp.Db.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("drop settings table: %v", err)
	}
	printReceiptAsync(dp, "R-SD-E", "")
	dp.WaitForAsyncWork()
	var failures int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id = 'R-SD-E' AND action = 'print_failed'`).Scan(&failures); err != nil {
		t.Fatalf("count print_failed: %v", err)
	}
	if failures != 0 {
		t.Fatalf("got %d print_failed rows in a shadow market, want 0", failures)
	}
}
