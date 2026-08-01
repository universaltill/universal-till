package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newPrintAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerPrintAPI(mux, dp)
	return mux, dp
}

func TestPrinterConfig_Defaults(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	cfg := printerConfig(context.Background(), dp)
	if cfg.Mode != "off" {
		t.Fatalf("expected default mode 'off', got %q", cfg.Mode)
	}
	if cfg.Charset != "utf8" {
		t.Fatalf("expected default charset 'utf8', got %q", cfg.Charset)
	}
	if !cfg.AutoPrint {
		t.Fatal("expected auto-print to default to true")
	}
	if cfg.Enabled() {
		t.Fatal("expected a default (unconfigured) printer to report Enabled()=false")
	}
}

func TestReceiptDesignFromSettings_Defaults(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	rd := receiptDesignFromSettings(context.Background(), dp)
	if rd.Footer != "Thank you!" {
		t.Fatalf("expected the friendly default footer, got %q", rd.Footer)
	}
	if rd.ShowSKU {
		t.Fatal("expected ShowSKU to default to false")
	}
	if !rd.ShowTax {
		t.Fatal("expected ShowTax to default to true")
	}
	if !rd.ShowBarcode {
		t.Fatal("expected ShowBarcode to default to true")
	}
	if len(rd.Header) != 0 {
		t.Fatalf("expected no header lines by default, got %+v", rd.Header)
	}
}

func TestReceiptLogoRaster_NoFileReturnsNilNotError(t *testing.T) {
	rd := receiptDesign{ShowLogo: true}
	// No logo file exists at paths.Data(...) in this fresh temp dir —
	// must degrade to no logo, never block a receipt over a missing file.
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	if raster := receiptLogoRaster(rd); raster != nil {
		t.Fatalf("expected nil raster when no logo file exists, got %d bytes", len(raster))
	}
	if raster := receiptLogoRaster(receiptDesign{ShowLogo: false}); raster != nil {
		t.Fatal("expected nil raster when ShowLogo is false, regardless of any file")
	}
}

func seedReceiptSale(t *testing.T, dp *common.Deps, id, receiptNo, saleType, originalReceiptFor string, total, tip, changeGiven int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', ?, 'GBP', ?, 0, 0, ?, datetime('now'), datetime('now'))`, id, receiptNo, saleType, total, total); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, ?, 0, 0, ?, ?)`, id+"-line1", id, total, total, total); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES(?,?,?,?,'GBP',?,?,datetime('now'))`,
		id+"-pay", id, "cash", total, changeGiven, tip); err != nil {
		t.Fatal(err)
	}
	if originalReceiptFor != "" {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES(?,?,?,'test')`, id+"-link", id, originalReceiptFor); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildReceiptDoc_NormalSale(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if doc.StoreName == "" {
		t.Fatal("expected a store name")
	}
	if len(doc.Lines) != 1 || doc.Lines[0].Name != "Apple" {
		t.Fatalf("expected the sale's line, got %+v", doc.Lines)
	}
	if doc.Barcode != "R001" {
		t.Fatalf("expected the receipt number as the scan-to-refund barcode (ShowBarcode defaults true), got %q", doc.Barcode)
	}
	if !doc.KickDrawer {
		t.Fatal("expected the drawer to kick for a cash payment")
	}
	for _, m := range doc.Meta {
		if strings.Contains(m, "REFUND") {
			t.Fatalf("expected no REFUND marker on a plain sale, got %+v", doc.Meta)
		}
	}
}

func TestBuildReceiptDoc_RefundReferencesOriginalReceipt(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	seedReceiptSale(t, dp, "return1", "R002", "return", "sale1", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R002")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(doc.Meta, "\n")
	if !strings.Contains(joined, "REFUND") {
		t.Fatalf("expected a REFUND marker, got %+v", doc.Meta)
	}
	if !strings.Contains(joined, "R001") {
		t.Fatalf("expected the original receipt number referenced, got %+v", doc.Meta)
	}
}

func TestBuildReceiptDoc_TipAndChangeShowAsPaymentLines(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 1000, 150, 200)

	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	var change, tip *string
	for i, p := range doc.Payments {
		if p.Label == "Change" {
			change = &doc.Payments[i].Amount
		}
		if p.Label == "Tip" {
			tip = &doc.Payments[i].Amount
		}
	}
	if change == nil {
		t.Fatalf("expected a Change payment line, got %+v", doc.Payments)
	}
	if tip == nil {
		t.Fatalf("expected a Tip payment line, got %+v", doc.Payments)
	}
	// Assert the actual printed amounts, not just that a line with the
	// right label exists — a regression that mislabeled or swapped the
	// minor-unit values would still pass a label-only check.
	if *change != "£2.00" {
		t.Fatalf("expected Change £2.00 (change_given=200), got %q", *change)
	}
	if *tip != "£1.50" {
		t.Fatalf("expected Tip £1.50 (tip_amount=150), got %q", *tip)
	}
}

// ut-docs#72: a non-zero service charge shows as its own receipt line,
// distinct from Tax/Discount/TOTAL and from a payment's Tip line.
func TestBuildReceiptDoc_ServiceChargeShowsAsDistinctTotalsLine(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, created_at, completed_at)
VALUES('sale-sc', 'R-SC1', 'completed', 'sale', 'GBP', 1000, 0, 0, 1100, 100, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('sale-sc-line1', 'sale-sc', 1, 'itm1', 'Apple', 'ABC', 1, 1000, 0, 0, 1000, 1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES('sale-sc-pay','sale-sc','cash',1100,'GBP',0,0,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	doc, err := buildReceiptDoc(ctx, dp, "R-SC1")
	if err != nil {
		t.Fatal(err)
	}
	var serviceCharge *string
	for i, kv := range doc.Totals {
		if kv.Label == "Service Charge" {
			serviceCharge = &doc.Totals[i].Amount
		}
	}
	if serviceCharge == nil {
		t.Fatalf("expected a Service Charge totals line, got %+v", doc.Totals)
	}
	if *serviceCharge != "£1.00" {
		t.Fatalf("expected Service Charge £1.00 (service_charge_amount=100), got %q", *serviceCharge)
	}
}

// A sale with no service charge (the common case, service_charge_amount
// defaults to 0) must NOT show a Service Charge line at all.
func TestBuildReceiptDoc_NoServiceChargeLineWhenZero(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range doc.Totals {
		if kv.Label == "Service Charge" {
			t.Fatalf("expected no Service Charge line when service_charge_amount is 0, got %+v", doc.Totals)
		}
	}
}

func TestBuildReceiptDoc_UnknownReceipt(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	if _, err := buildReceiptDoc(context.Background(), dp, "NO-SUCH-RECEIPT"); err == nil {
		t.Fatal("expected an error for an unknown receipt number")
	}
}

// --- HTTP handlers ---

func TestPostSettingsPrinter_ValidatesModeAndCharset(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=bogus"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid mode, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=network&address=192.168.1.50:9100&charset=weird&autoPrint=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a valid mode, got %d: %s", rec.Code, rec.Body.String())
	}
	cfg := printerConfig(context.Background(), dp)
	if cfg.Mode != "network" || cfg.Address != "192.168.1.50:9100" {
		t.Fatalf("expected the settings persisted, got %+v", cfg)
	}
	// An unrecognized charset silently falls back to utf8 rather than
	// storing garbage.
	if cfg.Charset != "utf8" {
		t.Fatalf("expected charset to fall back to utf8, got %q", cfg.Charset)
	}
}

func TestPostSettingsPrinter_RequiresManager(t *testing.T) {
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=off"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostPrintTest_FailsCleanlyWithNoPrinterConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/test", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 with printer off, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostPrintTest_RequiresManager(t *testing.T) {
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/test", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostPrintLabels_NoItemIDFails(t *testing.T) {
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=does-not-exist"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown item, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestClampCopies(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1}, {-5, 1}, {1, 1}, {50, 50}, {51, 50}, {999, 50}, {3, 3},
	}
	for _, c := range cases {
		if got := clampCopies(c.in); got != c.want {
			t.Errorf("clampCopies(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPostPrintLabels_HandlesOutOfRangeCopiesGracefully(t *testing.T) {
	// The real clamped value is only observable on the success path (needs
	// a working print transport, not available here) — clampCopies itself
	// is directly unit-tested above. This just confirms an out-of-range
	// copies value never crashes or short-circuits validation before the
	// item-not-found check runs.
	mux, _ := newPrintAPITestDeps(t)
	for _, copies := range []string{"0", "-5", "999", "3"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=does-not-exist&copies="+copies))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("copies=%s: expected 404 (item not found), got %d: %s", copies, rec.Code, rec.Body.String())
		}
	}
}

func TestPostPrintReceipt_ReprintFailsCleanlyWithNoPrinter(t *testing.T) {
	mux, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/receipt/R001", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 with no printer configured, got %d: %s", rec.Code, rec.Body.String())
	}

	// An audit entry is recorded either way (ok=false here).
	var count int
	if err := dp.Db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE entity_id = 'R001' AND action = 'receipt_reprint'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one receipt_reprint audit entry, got %d", count)
	}
}
