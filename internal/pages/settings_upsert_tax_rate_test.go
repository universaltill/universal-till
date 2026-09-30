package pages

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3255: an upsert of store.tax_rate updated RuntimeState but never
// reached the engines, so the cashier Engine, KioskEngine and live
// self-order sessions kept charging the old rate until some other save,
// sync pull or restart. It also persisted any value unchecked.
func TestSettingsUpsertTaxRate_ReachesEveryEngine(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service {
		return pos.NewServiceWithResolver(d.KioskEngine.Config(), stubResolver{})
	})
	_, sess, _ := d.SelfOrderSessions.Create()

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyTaxRate}, "value": {"25"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert %s=25 = %d body=%s, want 204", common.KeyTaxRate, rec.Code, rec.Body.String())
	}
	if got := d.CurrentState().TaxRatePct; got != 25 {
		t.Fatalf("live TaxRatePct = %d, want 25", got)
	}
	assertEnginesOnTaxRate(t, d, sess, 2500)
}

func TestSettingsUpsertTaxRate_RefusesInvalidWithoutPersisting(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setTaxRate(t, d, 20)
	for _, bad := range []string{"-1", "abc", "101", "12.5", ""} {
		rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyTaxRate}, "value": {bad}}, &mgrUser)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("upsert %s=%q = %d, want 400", common.KeyTaxRate, bad, rec.Code)
		}
		if v, _, _ := d.Settings.Get(t.Context(), common.KeyTaxRate); v != "20" {
			t.Fatalf("a refused upsert %q overwrote the stored rate: %q", bad, v)
		}
	}
	// A refused value is a 400 before the elevation gate: a cashier is not
	// walked through an approver's PIN for a request that was always going
	// to fail.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyTaxRate}, "value": {"abc"}}, &cashUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("cashier upsert %s=abc = %d, want 400 before elevation", common.KeyTaxRate, rec.Code)
	}
	// The boundaries are accepted, and a padded value is stored normalised.
	for in, want := range map[string]string{"0": "0", "100": "100", "025": "25"} {
		if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyTaxRate}, "value": {in}}, &mgrUser); rec.Code != http.StatusNoContent {
			t.Fatalf("upsert %s=%s = %d, want 204", common.KeyTaxRate, in, rec.Code)
		}
		if v, _, _ := d.Settings.Get(t.Context(), common.KeyTaxRate); v != want {
			t.Fatalf("upsert %s=%s stored %q, want %q", common.KeyTaxRate, in, v, want)
		}
	}
}
