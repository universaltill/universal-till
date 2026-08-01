package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	_ "modernc.org/sqlite"
)

// ut-docs#213: one consistent sale-screen notification surface. Server
// messages render as a .pos-notice with an explicit level; errors carry
// role="alert" and a dismiss control, info carries role="status". The old
// path wrote hardcoded English straight into ToastMessage, invisible to
// guard-i18n.
func newNoticeTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS customers (id TEXT PRIMARY KEY, name TEXT, loyalty_no TEXT, phone TEXT);`)
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 100},
	}
	dp := &common.Deps{
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver),
		Db:     db,
	}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	return mux
}

func postScan(t *testing.T, mux *http.ServeMux, body, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestScanUnknownItemRendersPersistentErrorNotice(t *testing.T) {
	mux := newNoticeTestMux(t)
	rec := postScan(t, mux, "code=NOPE&qty=1", "")
	body := rec.Body.String()
	if !strings.Contains(body, `pos-notice error`) {
		t.Fatalf("expected an error-level pos-notice, got: %s", body)
	}
	if !strings.Contains(body, `role="alert"`) {
		t.Fatalf("error notice must carry role=alert, got: %s", body)
	}
	if !strings.Contains(body, `notice-dismiss`) {
		t.Fatalf("error notice must be dismissible, got: %s", body)
	}
	if !strings.Contains(body, "Item not found") {
		t.Fatalf("expected localized en message, got: %s", body)
	}
}

func TestScanEmptyCodeRendersInfoNotice(t *testing.T) {
	mux := newNoticeTestMux(t)
	rec := postScan(t, mux, "code=&qty=1", "")
	body := rec.Body.String()
	if !strings.Contains(body, `pos-notice info`) {
		t.Fatalf("expected an info-level pos-notice, got: %s", body)
	}
	if !strings.Contains(body, `role="status"`) {
		t.Fatalf("info notice must carry role=status, got: %s", body)
	}
}

// The message must come from the locale files, not a Go string literal:
// request Farsi and assert the fa.json translation is what renders.
func TestScanUnknownItemNoticeIsTranslated(t *testing.T) {
	mux := newNoticeTestMux(t)

	raw, err := os.ReadFile(filepath.Join("web", "locales", "fa.json"))
	if err != nil {
		t.Fatalf("read fa.json: %v", err)
	}
	var fa map[string]string
	if err := json.Unmarshal(raw, &fa); err != nil {
		t.Fatalf("parse fa.json: %v", err)
	}
	want := fa["pos.toast.item_not_found"]
	if want == "" {
		t.Fatalf("fa.json is missing pos.toast.item_not_found")
	}

	rec := postScan(t, mux, "code=NOPE&qty=1", "?lang=fa")
	body := rec.Body.String()
	if strings.Contains(body, "Item not found") {
		t.Fatalf("hardcoded English leaked into a fa response: %s", body)
	}
	if !strings.Contains(body, want) {
		t.Fatalf("expected fa translation %q in response, got: %s", want, body)
	}
}

func TestBasketRendersItemCountBadge(t *testing.T) {
	mux := newNoticeTestMux(t)
	rec := postScan(t, mux, "code=ABC&qty=3", "")
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="basket-count"`) {
		t.Fatalf("expected basket-count badge, got: %s", body)
	}
	if !strings.Contains(body, `>3</span>`) {
		t.Fatalf("expected count 3 in badge, got: %s", body)
	}
}
