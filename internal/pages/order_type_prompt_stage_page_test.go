package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// --- POST /api/settings/sale-order-type-prompt -----------------------------

// Each of the three real values round-trips through the real settings row.
func TestPostSaleOrderTypePrompt_RoundTrips(t *testing.T) {
	for _, stage := range []string{
		data.OrderTypePromptStageCartTop,
		data.OrderTypePromptStageBeforeSale,
		data.OrderTypePromptStageAtPay,
	} {
		t.Run(stage, func(t *testing.T) {
			mux, _, dp := newFullAuthDeps(t)
			rec := postForm(mux, "/api/settings/sale-order-type-prompt", url.Values{"stage": {stage}}, &mgrUser)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "\u2713") {
				t.Fatalf("manager save: code=%d body=%s", rec.Code, rec.Body.String())
			}
			got, ok, err := dp.Settings.Get(context.Background(), data.OrderTypePromptStageKey)
			if err != nil || !ok {
				t.Fatalf("setting not persisted: ok=%v err=%v", ok, err)
			}
			if got != stage {
				t.Fatalf("stored stage = %q, want %q", got, stage)
			}
		})
	}
}

// A garbage/unknown value is rejected outright (400) and never persisted --
// same "reject at the boundary" convention as printer.receipt_policy's
// plugin-clamp rejection and order-no-scheme's own enum guard.
func TestPostSaleOrderTypePrompt_RejectsUnknownValue(t *testing.T) {
	mux, _, dp := newFullAuthDeps(t)
	if err := dp.Settings.Set(context.Background(), data.OrderTypePromptStageKey, data.OrderTypePromptStageAtPay); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := postForm(mux, "/api/settings/sale-order-type-prompt", url.Values{"stage": {"sometimes"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown stage, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _, _ := dp.Settings.Get(context.Background(), data.OrderTypePromptStageKey); got != data.OrderTypePromptStageAtPay {
		t.Fatalf("rejected save must not overwrite the existing setting, got %q", got)
	}
}

// A cashier session (not manager/admin) gets the in-place PIN re-auth
// prompt instead of the save landing -- same elevation gate every other
// settings write on this page goes through (checkOrElevate).
func TestPostSaleOrderTypePrompt_RequiresElevation(t *testing.T) {
	mux, _, dp := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/sale-order-type-prompt", url.Values{"stage": {data.OrderTypePromptStageAtPay}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
		!strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Fatalf("cashier save: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if got, ok, _ := dp.Settings.Get(context.Background(), data.OrderTypePromptStageKey); ok && got != "" {
		t.Fatalf("a cashier's un-elevated request must not persist, got %q", got)
	}

	rec = postForm(mux, "/api/settings/sale-order-type-prompt", url.Values{"stage": {data.OrderTypePromptStageAtPay}}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager save: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _, _ := dp.Settings.Get(context.Background(), data.OrderTypePromptStageKey); got != data.OrderTypePromptStageAtPay {
		t.Fatalf("manager save did not persist: got %q", got)
	}
}

// --- Settings page ----------------------------------------------------------

// The settings page renders the three-way <select>, pre-selecting the
// stored stage (an unset row pre-selects cart_top, the default).
func TestSettingsPage_OrderTypePromptStageControl(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := get()
	if !strings.Contains(body, `name="stage"`) {
		t.Fatalf("settings page has no order-type-prompt stage control:\n%s", body)
	}
	if !strings.Contains(body, `value="cart_top" `) || !strings.Contains(body, `value="cart_top" selected`) {
		t.Fatalf("unset stage must pre-select cart_top (the default):\n%s", body)
	}

	if err := d.Settings.Set(context.Background(), data.OrderTypePromptStageKey, data.OrderTypePromptStageBeforeSale); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body = get()
	if !strings.Contains(body, `value="before_sale" selected`) {
		t.Fatalf("stored before_sale is not pre-selected:\n%s", body)
	}
}
