package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// setFiscalTestSettings writes settings and re-derives RuntimeState (the
// gate reads Country off CurrentState, same as production).
func setFiscalTestSettings(t *testing.T, dp *common.Deps, kv map[string]string) {
	t.Helper()
	ctx := context.Background()
	for k, v := range kv {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	dp.SetState(common.LoadState(ctx, dp.Settings, dp.Cfg))
}

func postTenderJSON(mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func countSales(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	return n
}

func TestFiscalGate_NeverConfiguredBlocksCashierTender(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setFiscalTestSettings(t, dp, map[string]string{
		common.KeyCountry:        "DE",
		fiscal.KeySystemOfRecord: "true",
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}

	rec := postTenderJSON(mux, `{"payments":[{"method":"cash","amount":300}]}`)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 hard block, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countSales(t, dp); n != 0 {
		t.Fatalf("no sale row may be created on a hard block, got %d", n)
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 1 {
		t.Fatalf("blocked tender must leave the basket untouched, got %d lines", len(b.Lines))
	}
}

func TestFiscalGate_NonGermanShopUnaffected(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	// GB (the LoadState default) + system_of_record on, TSE never
	// configured: only DE is gated today.
	setFiscalTestSettings(t, dp, map[string]string{
		fiscal.KeySystemOfRecord: "true",
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rec := postTenderJSON(mux, `{"payments":[{"method":"cash","amount":300}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-German shop must tender normally, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countSales(t, dp); n != 1 {
		t.Fatalf("expected 1 sale, got %d", n)
	}
}

func TestFiscalGate_ShadowGermanShopUnaffected(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	// DE but fiscal.system_of_record unset -> false: shadow/trial/demo
	// sales are never gated.
	setFiscalTestSettings(t, dp, map[string]string{
		common.KeyCountry: "DE",
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rec := postTenderJSON(mux, `{"payments":[{"method":"cash","amount":300}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("shadow German shop must tender normally, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFiscalGate_ConfiguredHealthyProceedsEvenOffline(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setFiscalTestSettings(t, dp, map[string]string{
		common.KeyCountry:        "DE",
		fiscal.KeySystemOfRecord: "true",
		fiscal.KeyTSEConfigured:  "true",
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// offline:true is ADR-0044's known-offline path — the gate must never
	// read or derive tse_failing_since from network-offline state, so an
	// offline sale on a healthy-TSE shop completes (proceed-and-declare,
	// untouched by ADR-0048).
	rec := postTenderJSON(mux, `{"payments":[{"method":"cash","amount":300}],"offline":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("known-offline sale must never be blocked by the fiscal gate, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countSales(t, dp); n != 1 {
		t.Fatalf("expected 1 sale, got %d", n)
	}
}

func TestFiscalGate_FailingWithoutOverrideBlocks(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setFiscalTestSettings(t, dp, map[string]string{
		common.KeyCountry:         "DE",
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "true",
		fiscal.KeyTSEFailingSince: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rec := postTenderJSON(mux, `{"payments":[{"method":"cash","amount":300}]}`)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("failing TSE without override must block, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countSales(t, dp); n != 0 {
		t.Fatalf("no sale row may be created while blocked, got %d", n)
	}
}

// The same gate covers the anonymous self-order kiosk checkout (both
// surfaces funnel through completeTender): a hard-blocked German shop's
// kiosk re-renders the payment picker with a "order at the counter"
// notice and creates no sale — and the kiosk basket is left intact.
func TestFiscalGate_KioskCheckoutHardBlocked(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	st := dp.CurrentState()
	st.Country = "DE"
	dp.SetState(st)
	if err := dp.Settings.Set(context.Background(), fiscal.KeySystemOfRecord, "true"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	post("/api/self-order/scan", "code=5000001")

	rec := post("/api/self-order/checkout", "method=card")
	if rec.Code != http.StatusConflict {
		t.Fatalf("kiosk checkout must be hard-blocked with 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), httpx.T("en", "selforder.checkout.fiscal_blocked")) {
		t.Fatalf("kiosk block must show the counter notice, got: %s", rec.Body.String())
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("no sale row may be created on a kiosk hard block, got %d", n)
	}
	if len(dp.KioskEngine.Basket().Lines) != 1 {
		t.Fatal("blocked kiosk checkout must leave the kiosk basket intact")
	}
}

func TestFiscalGate_ActiveOverrideProceedsAndFlagsSale(t *testing.T) {
	// Real locale table so the receipt marker assertion checks the actual
	// rendered string, not an uninitialized-T fallback.
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	mux, dp := newPOSTestDeps(t)
	until := time.Now().UTC().Add(2 * time.Hour)
	setFiscalTestSettings(t, dp, map[string]string{
		common.KeyCountry:         "DE",
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "true",
		fiscal.KeyTSEFailingSince: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
		fiscal.KeyOverrideUntil:   until.Format(time.RFC3339),
		fiscal.KeyOverrideReason:  "dongle failed, replacement ordered",
		fiscal.KeyOverrideActor:   "adm1",
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rec := postTenderJSON(mux, `{"payments":[{"method":"cash","amount":300}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("active override must let the sale proceed, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countSales(t, dp); n != 1 {
		t.Fatalf("expected 1 sale, got %d", n)
	}

	// Every sale completed in the window is flagged in its own audit trail
	// (entity_type sale, action unsigned_override) — ut-docs#715 criterion.
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatalf("read sale id: %v", err)
	}
	var dataJSON string
	if err := dp.Db.QueryRow(
		`SELECT data_json FROM audit_log WHERE entity_type = 'sale' AND entity_id = ? AND action = 'unsigned_override'`,
		saleID,
	).Scan(&dataJSON); err != nil {
		t.Fatalf("expected an unsigned_override audit entry for sale %s: %v", saleID, err)
	}
	if !strings.Contains(dataJSON, "dongle failed, replacement ordered") {
		t.Fatalf("audit payload must carry the override reason, got %s", dataJSON)
	}

	// And the rendered receipt carries the override-window marker line.
	marker := httpx.T("en", "receipt.fiscal.unsigned_override")
	if marker == "" || marker == "receipt.fiscal.unsigned_override" {
		t.Fatalf("receipt marker locale key missing from en.json")
	}
	if !strings.Contains(rec.Body.String(), marker) {
		t.Fatalf("receipt must carry the override marker %q, body: %s", marker, rec.Body.String())
	}
}
