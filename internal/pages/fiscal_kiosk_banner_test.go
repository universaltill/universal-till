package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The self-order kiosk shares completeTender with the cashier path, so the
// German TSE hard gate (ADR-0048) blocks an anonymous kiosk checkout the
// same way — translated inline message on the payment picker pointing the
// customer to the counter, never a modal, no sale row.
func TestFiscalGate_KioskCheckoutBlocked(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	ctx := context.Background()
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	if err := dp.Settings.Set(ctx, fiscal.KeySystemOfRecord, "true"); err != nil {
		t.Fatal(err)
	}
	// Never-configured: the unconditional hard block.

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
		t.Fatalf("fiscal block: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "please order at the counter") {
		t.Fatalf("expected the translated fiscal-blocked message, got: %s", rec.Body.String())
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status = 'completed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a blocked kiosk checkout must record NO sale, got %d", n)
	}
	// The basket survives — staff can finish the order at the counter.
	if len(dp.KioskEngine.Basket().Lines) != 1 {
		t.Fatalf("kiosk basket must be untouched by the refusal, got %d lines", len(dp.KioskEngine.Basket().Lines))
	}
}

// While an override window is active, the sale screen carries a persistent
// banner (ADR-0048: everyone at the till should see sales are being
// recorded without a TSE signature) — and none outside one.
func TestFiscalGate_SaleScreenBannerDuringOverride(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	registerIndex(mux, dp)
	seedFailingConfiguredTSE(t, dp)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Blocked (no override): no banner — the refusal itself rides the
	// tender attempt, not the page.
	if body := get(); strings.Contains(body, "fiscal-override-banner") {
		t.Fatalf("no banner expected without an active override")
	}

	// Active override: banner present.
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideUntil, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, "fiscal-override-banner") {
		t.Fatalf("expected the override banner on the sale screen, got: %s", body)
	}

	// Expired override: banner gone again on the next render.
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideUntil, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if body := get(); strings.Contains(body, "fiscal-override-banner") {
		t.Fatalf("expected the banner to disappear once the window expired")
	}
}
