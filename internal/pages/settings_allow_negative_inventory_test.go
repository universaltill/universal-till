package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#1843. pos.allow_negative_inventory was complete on the server and
// reachable from nowhere in the product — a merchant who does not track stock
// could not sell at all, and could only fix it by hand-editing the settings
// table. This endpoint is the whole of "a merchant can reach it".
//
// Same manager-gated, elevation-wired, boolean shape as launch-on-startup and
// catalog-import-barcode-default above, with one addition those two do not
// need: this key drives live behaviour (CompleteSale's stock guard reads
// RuntimeState, not the settings table), so BOTH the persisted row and the
// in-memory state are asserted after every write. Persisting without
// updating state would leave the merchant ticking the box and still being
// refused at the till until the next restart.
func TestAllowNegativeInventoryEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if d.CurrentState().AllowNegativeInventory {
		t.Fatal("AllowNegativeInventory should default to false — the stock guard is on out of the box")
	}

	if rec := postForm(mux, "/api/settings/allow-negative-inventory", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier allow-negative-inventory = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	// A cashier's refused attempt must not have changed anything.
	if d.CurrentState().AllowNegativeInventory {
		t.Fatal("an un-elevated cashier POST changed the live stock guard")
	}

	if rec := postForm(mux, "/api/settings/allow-negative-inventory", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed allow-negative-inventory = %d, want 400", rec.Code)
	}

	if rec := postForm(mux, "/api/settings/allow-negative-inventory", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable allow-negative-inventory = %d, want 204", rec.Code)
	}
	if !d.CurrentState().AllowNegativeInventory {
		t.Fatal("live state still has the stock guard on after enabling the setting")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyAllowNegativeInventory); v != "true" {
		t.Fatalf("stored %s = %q, want true", common.KeyAllowNegativeInventory, v)
	}

	if rec := postForm(mux, "/api/settings/allow-negative-inventory", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable allow-negative-inventory = %d, want 204", rec.Code)
	}
	if d.CurrentState().AllowNegativeInventory {
		t.Fatal("live state still has the guard off after disabling the setting")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyAllowNegativeInventory); v != "false" {
		t.Fatalf("stored %s = %q, want false", common.KeyAllowNegativeInventory, v)
	}
}

// The control has to actually be on the page — the defect this card fixes was
// a complete backend with no way to reach it, so a test that only exercises
// the endpoint would have passed on the broken product too.
func TestSettingsPageRendersStockTrackingControl(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="allow-negative-inventory-cb"`) {
		t.Fatal("Settings has no stock-tracking checkbox — the setting is unreachable again")
	}
	if !strings.Contains(body, "/api/settings/allow-negative-inventory") {
		t.Fatal("the stock-tracking checkbox posts nowhere")
	}
	if !strings.Contains(body, "Sell items without tracking stock") {
		t.Fatalf("the checkbox label did not render (missing i18n key?)")
	}
}
