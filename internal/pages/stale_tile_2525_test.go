package pages

// ut-docs#2525: a sale-screen tile is a snapshot of the catalog taken when
// the grid last rendered. When the catalog changes somewhere else while the
// sale screen stays open (another tab or device, a my. push, a main-till ->
// replica sync), the tile's code can stop resolving. Tapping it must not end
// in a bare "Item not found" that repeats on every retry. The basket says the
// buttons were out of date, and the grid re-fetches itself via
// buttons-changed. Only a tile sends src=tile; manual entry and the
// suggestion strip keep the plain "Item not found".

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

func postTileScan(t *testing.T, mux *http.ServeMux, code string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"code": {code}, "src": {"tile"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestScanAPI_StaleTileRefreshesGrid(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-gone','GONE1','Seasonal Tart',450,1)`); err != nil {
		t.Fatal(err)
	}
	// The grid rendered the tile while the item was active...
	if rec := postTileScan(t, mux, "GONE1"); len(dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("precondition: an active item's tile adds a line, got %+v (%s)", dp.Engine.Basket().Lines, rec.Body.String())
	}
	dp.Engine.Reset()
	// ...then another device deactivated it.
	if _, err := d.DB.Exec(`UPDATE items SET is_active = 0 WHERE id = 'itm-gone'`); err != nil {
		t.Fatal(err)
	}

	rec := postTileScan(t, mux, "GONE1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with a toast, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if want := httpx.T("en", "pos.toast.tile_stale_refreshed"); !strings.Contains(body, want) {
		t.Fatalf("expected the %q toast, got: %s", want, body)
	}
	if strings.Contains(body, httpx.T("en", "pos.toast.item_not_found")) {
		t.Fatalf("a stale tile must not show the bare item-not-found toast: %s", body)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("HX-Trigger = %q, want buttons-changed so the grid re-fetches", got)
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("no line may be added for a deactivated item, got %d", n)
	}
}

func TestScanAPI_ManualEntryMissKeepsItemNotFound(t *testing.T) {
	mux, _, _ := setupScanBarcodeDeps(t)
	rec := postScanCode(t, mux, "NO-SUCH-CODE")
	if want := httpx.T("en", "pos.toast.item_not_found"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("manual entry must keep %q, got: %s", want, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("manual entry must not refresh the grid, HX-Trigger = %q", got)
	}
}

func TestGetModifiers_StaleTileRefreshesGridInsteadOfOpeningPicker(t *testing.T) {
	mux, dp, _ := setupScanBarcodeDeps(t)
	registerPOSModifiersAPI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/modifiers?item=itm-gone&code=GONE1&src=tile", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with a basket toast, got %d: %s", rec.Code, rec.Body.String())
	}
	for h, want := range map[string]string{
		"HX-Retarget": "#basket",
		"HX-Reswap":   "outerHTML",
		"HX-Trigger":  "buttons-changed",
	} {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	body := rec.Body.String()
	if want := httpx.T("en", "pos.toast.tile_stale_refreshed"); !strings.Contains(body, want) {
		t.Fatalf("expected the %q toast, got: %s", want, body)
	}
	if !strings.Contains(body, `id="basket"`) {
		t.Fatalf("the response must be the basket (it is retargeted there), got: %s", body)
	}
}
