package pages

import (
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// POST /api/settings/payments-tips saves the tip switch and the preset list.
func TestPaymentsTipsSettingEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// A cashier without an approver PIN gets the elevation prompt, nothing saved.
	rec := postForm(mux, "/api/settings/payments-tips", url.Values{"enabled": {"on"}, "presets": {"10,15"}}, &cashUser)
	if !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier: want the elevation prompt, got %d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyTipsEnabled); ok {
		t.Fatal("a denied cashier must save nothing")
	}

	// Manager: on with a decimal preset, stored canonically.
	rec = postForm(mux, "/api/settings/payments-tips", url.Values{"enabled": {"on"}, "presets": {" 10 , 12.50,12.5, 20 "}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("manager save: %d %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyTipsEnabled); v != "1" {
		t.Fatalf("tips_enabled = %q, want 1", v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyTipPresets); v != "10,12.5,20" {
		t.Fatalf("tip_presets = %q, want the canonical 10,12.5,20 (duplicate dropped)", v)
	}

	// Switch off: an unchecked box posts nothing, and that is an explicit off,
	// not "unset" (which would turn a service shop back on).
	rec = postForm(mux, "/api/settings/payments-tips", url.Values{"presets": {"10,12.5,20"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("manager off: %d %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyTipsEnabled); v != "0" {
		t.Fatalf("tips_enabled = %q, want an explicit 0", v)
	}

	// Presets with nothing valid are refused and the old list stays.
	rec = postForm(mux, "/api/settings/payments-tips", url.Values{"enabled": {"on"}, "presets": {"abc, 0, 150"}}, &mgrUser)
	if rec.Code != 200 && rec.Code != 400 || strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("invalid presets must be refused: %d %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyTipPresets); v != "10,12.5,20" {
		t.Fatalf("tip_presets = %q after a refused save, want it unchanged", v)
	}

	// An empty list restores the defaults.
	postForm(mux, "/api/settings/payments-tips", url.Values{"enabled": {"on"}, "presets": {""}}, &mgrUser)
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyTipPresets); v != "10,15,20" {
		t.Fatalf("tip_presets = %q, want the default 10,15,20", v)
	}
}
