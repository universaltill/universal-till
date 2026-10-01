package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#238: POST /api/print/labels used to write ad-hoc
// <span class="...">...</span> fragments straight into #labels-msg via
// fmt.Fprintf — this migrates it onto the documented .pos-notice pattern
// (docs/sale-screen-notifications.md), the same shape
// web/ui/partials/basket.html renders for the sale screen.

func initLabelsNoticeI18n(t *testing.T) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

func TestPostPrintLabels_NoItemRendersPosNoticeError(t *testing.T) {
	initLabelsNoticeI18n(t)
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=does-not-exist"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `class="pos-notice error"`) {
		t.Fatalf("expected a pos-notice error, got: %s", body)
	}
	if !strings.Contains(body, `role="alert"`) {
		t.Fatalf("error notice must carry role=alert, got: %s", body)
	}
	want := httpx.T("en", "catalog.labels.no_item")
	if !strings.Contains(body, want) {
		t.Fatalf("expected translated no_item message %q, got: %s", want, body)
	}
	if strings.Contains(body, `<span class="muted">`) {
		t.Fatalf("old ad-hoc <span class=\"muted\"> markup must be gone, got: %s", body)
	}
}

func TestPostPrintLabels_NoCodeRendersPosNoticeError(t *testing.T) {
	initLabelsNoticeI18n(t)
	mux, dp := newPrintAPITestDeps(t)

	// An item with neither a SKU nor a barcode has nothing to print as a
	// scannable label's code.
	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('item-no-code', NULL, 'No Code Item', 100)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=item-no-code"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an item with no code, got %d: %s", rec.Code, body)
	}
	if !strings.Contains(body, `class="pos-notice error"`) {
		t.Fatalf("expected a pos-notice error, got: %s", body)
	}
	want := httpx.T("en", "catalog.labels.no_code")
	if !strings.Contains(body, want) {
		t.Fatalf("expected translated no_code message %q, got: %s", want, body)
	}
}

// ut-docs#2062: a label's whole content is a scannable barcode, which a
// "system" (CUPS `lp`, plain-text-only) printer can never render — this
// used to surface as print.NewTransport's generic "unknown printer mode"
// error, bubbling up as a bare "Print failed" with no explanation.
func TestPostPrintLabels_SystemModeRendersPosNoticeError(t *testing.T) {
	initLabelsNoticeI18n(t)
	mux, dp := newPrintAPITestDeps(t)

	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('item-printable', 'SKU1', 'Printable Item', 250)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterMode, "system"); err != nil {
		t.Fatalf("set printer mode: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=item-printable"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a system-mode printer, got %d: %s", rec.Code, body)
	}
	if !strings.Contains(body, `class="pos-notice error"`) {
		t.Fatalf("expected a pos-notice error, got: %s", body)
	}
	want := httpx.T("en", "catalog.labels.system_unsupported")
	if !strings.Contains(body, want) {
		t.Fatalf("expected translated system_unsupported message %q, got: %s", want, body)
	}
	if strings.Contains(body, "unknown printer mode") {
		t.Fatalf("must not leak the generic transport error, got: %s", body)
	}
}

// ut-docs#3343: Price Marking Order 2004 — a weighed item's printed shelf
// label must show its unit price ("per kg"), not just the bare price.
func TestPostPrintLabels_WeighedItemPrintsPricePerUnit(t *testing.T) {
	initLabelsNoticeI18n(t)
	mux, dp := newPrintAPITestDeps(t)

	// ut-docs#3343 review finding 1: a weighed item's OWN `unit` field is
	// "each" in every real case (catalog import always writes "each"
	// regardless of IsWeighed, and the hand-entry form's unit field has no
	// link to the Sold-by-weight checkbox) — this seed deliberately does
	// NOT use unit='kg', to prove the per-kg suffix doesn't depend on it.
	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, unit, is_weighed) VALUES ('item-weighed', 'SKU-W', 'Bananas', 150, 'each', 1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, unit, is_weighed) VALUES ('item-each', 'SKU-E', 'Mug', 500, 'each', 0)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	// A variant of a weighed item (e.g. "Apples Large" under weighed
	// "Apples") must inherit the per-kg suffix too (review finding 2).
	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, unit, is_weighed) VALUES ('item-weighed-parent', 'SKU-WP', 'Apples', 200, 'each', 1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('variant-weighed', 'item-weighed-parent', 'SKU-WP-L', 'Large', 220, 1)`); err != nil {
		t.Fatalf("seed variant: %v", err)
	}

	devicePath := filepath.Join(t.TempDir(), "fake-printer")
	if err := os.WriteFile(devicePath, nil, 0o644); err != nil {
		t.Fatalf("create fake device file: %v", err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterMode, "device"); err != nil {
		t.Fatalf("set printer mode: %v", err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterDevice, devicePath); err != nil {
		t.Fatalf("set printer device: %v", err)
	}

	print := func(form string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %q, got %d: %s", form, rec.Code, rec.Body.String())
		}
		out, err := os.ReadFile(devicePath)
		if err != nil {
			t.Fatalf("read device file: %v", err)
		}
		return string(out)
	}
	printItem := func(itemID string) string { return print("item_id=" + itemID) }

	weighedJob := printItem("item-weighed")
	if !strings.Contains(weighedJob, "£1.50 per kg") {
		t.Fatalf("expected weighed item's label job to contain %q, got: %q", "£1.50 per kg", weighedJob)
	}

	variantOfWeighedJob := print("variant_id=variant-weighed")
	if !strings.Contains(variantOfWeighedJob, "£2.20 per kg") {
		t.Fatalf("expected a variant of a weighed item to inherit the per-kg suffix, got: %q", variantOfWeighedJob)
	}

	eachJob := printItem("item-each")
	if strings.Contains(eachJob, "per ") {
		t.Fatalf("non-weighed item's label job must not carry a unit-price suffix, got: %q", eachJob)
	}
}

func TestPostPrintLabels_SuccessRendersPosNoticeSuccessWithCopiesCount(t *testing.T) {
	initLabelsNoticeI18n(t)
	mux, dp := newPrintAPITestDeps(t)

	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('item-printable', 'SKU1', 'Printable Item', 250)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	// deviceTransport just opens the configured path for writing — a plain
	// regular file stands in for the physical printer here.
	devicePath := filepath.Join(t.TempDir(), "fake-printer")
	if err := os.WriteFile(devicePath, nil, 0o644); err != nil {
		t.Fatalf("create fake device file: %v", err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterMode, "device"); err != nil {
		t.Fatalf("set printer mode: %v", err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterDevice, devicePath); err != nil {
		t.Fatalf("set printer device: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=item-printable&copies=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, body)
	}
	if !strings.Contains(body, `class="pos-notice success"`) {
		t.Fatalf("expected a pos-notice success, got: %s", body)
	}
	if !strings.Contains(body, `role="status"`) {
		t.Fatalf("success notice must carry role=status, got: %s", body)
	}
	wantMsg := httpx.T("en", "catalog.labels.done")
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("expected translated done message %q, got: %s", wantMsg, body)
	}
	if !strings.Contains(body, "(3)") {
		t.Fatalf("expected the copies count (3) in the notice, got: %s", body)
	}
	if strings.Contains(body, `<span>`) {
		t.Fatalf("old ad-hoc <span> markup must be gone, got: %s", body)
	}
}
