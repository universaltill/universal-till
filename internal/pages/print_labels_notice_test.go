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
