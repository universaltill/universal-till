package pages

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#7206: a sell-screen overlay view draws signed add_to_basket
// buttons; the pick adds each as its own line with its opaque ref.

const overlayBasketDoc = `{"document":{"version":1,"components":[
	{"type":"text","text":{"literal":"Sam, 10:30"}},
	{"type":"suggestions","items":[
	 {"label":{"literal":"Skin fade"},"effect":{"add_to_basket":{"sku":"SVC-1","qty":1,"ref":"appt:1:0"}}},
	 {"label":{"literal":"Beard trim"},"effect":{"add_to_basket":{"sku":"SVC-1","qty":1,"ref":"appt:1:1"}}}]}]}}`

var overlayValsRe = regexp.MustCompile(`hx-vals='([^']*)'`)

func newOverlayBasketHarness(t *testing.T, grantViewSales bool) *viewHarness {
	t.Helper()
	h := newViewHarness(t)
	if _, err := h.d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('itm-svc','SVC-1','Service',2500,1)`); err != nil {
		t.Fatal(err)
	}
	if grantViewSales {
		if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('vs',?,?,1)`, viewPluginID, overlayViewSalesPerm); err != nil {
			t.Fatal(err)
		}
	}
	h.d.Engine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{
		"SVC-1": {SKU: "SVC-1", Name: "Service", Qty: 1, PriceCents: 2500, ItemID: "itm-svc"},
	})
	registerPOSAPI(h.mux, h.d)
	h.answerWith(overlayBasketDoc)
	return h
}

// pickForms returns the signed form of each button the overlay drew.
func pickForms(t *testing.T, body string) []url.Values {
	t.Helper()
	var out []url.Values
	for _, m := range overlayValsRe.FindAllStringSubmatch(body, -1) {
		raw := html.UnescapeString(m[1])
		// {"plugin":"..","sku":"..","qty":1,"ref":"..","sig":".."}
		f := url.Values{}
		for _, k := range []string{"plugin", "sku", "ref", "sig"} {
			if mm := regexp.MustCompile(`"` + k + `":"([^"]*)"`).FindStringSubmatch(raw); mm != nil {
				f.Set(k, mm[1])
			}
		}
		if mm := regexp.MustCompile(`"qty":(\d+)`).FindStringSubmatch(raw); mm != nil {
			f.Set("qty", mm[1])
		}
		out = append(out, f)
	}
	return out
}

var overlayHdr = map[string]string{"HX-Request": "true", "HX-Target": pluginOverlayTargetID}

func TestOverlayBasket_TwoRefLines_7206(t *testing.T) {
	h := newOverlayBasketHarness(t, true)
	rec := h.doAs(http.MethodGet, "/plugin/views/next", nil, nil, overlayHdr)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "plugin.view.unavailable") {
		t.Fatalf("overlay GET = %d:\n%s", rec.Code, rec.Body.String())
	}
	forms := pickForms(t, rec.Body.String())
	if len(forms) != 2 {
		t.Fatalf("drew %d signed buttons, want 2:\n%s", len(forms), rec.Body.String())
	}
	for _, f := range forms {
		if r := h.doAs(http.MethodPost, pluginViewPickRoute, f, nil, map[string]string{"HX-Request": "true"}); r.Code != http.StatusOK {
			t.Fatalf("pick = %d:\n%s", r.Code, r.Body.String())
		}
	}
	b := h.d.Engine.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("basket has %d lines, want 2 service lines", len(b.Lines))
	}
	for i, want := range []string{"appt:1:0", "appt:1:1"} {
		l := b.Lines[i]
		if l.PluginRef != want || l.PluginRefBy != viewPluginID || l.PriceCents.Minor() != 2500 {
			t.Errorf("line %d = ref %q by %q price %d, want %q by %q at the catalogue price", i, l.PluginRef, l.PluginRefBy, l.PriceCents.Minor(), want, viewPluginID)
		}
	}
}

func TestOverlayBasket_RefusedWithoutViewSales_7206(t *testing.T) {
	h := newOverlayBasketHarness(t, false)
	rec := h.doAs(http.MethodGet, "/plugin/views/next", nil, nil, overlayHdr)
	if !strings.Contains(rec.Body.String(), "plugin-view-notice") || len(pickForms(t, rec.Body.String())) != 0 {
		t.Fatalf("a plugin without view:sales drew buttons:\n%s", rec.Body.String())
	}
}

func TestOverlayBasket_PickRefusals_7206(t *testing.T) {
	h := newOverlayBasketHarness(t, true)
	forms := pickForms(t, h.doAs(http.MethodGet, "/plugin/views/next", nil, nil, overlayHdr).Body.String())
	if len(forms) == 0 {
		t.Fatal("no buttons")
	}
	hdr := map[string]string{"HX-Request": "true"}
	tamper := func(name string, mut func(url.Values)) {
		f := url.Values{}
		for k, v := range forms[0] {
			f[k] = append([]string(nil), v...)
		}
		mut(f)
		if r := h.doAs(http.MethodPost, pluginViewPickRoute, f, nil, hdr); r.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, r.Code)
		}
	}
	tamper("other ref", func(f url.Values) { f.Set("ref", "appt:evil") })
	tamper("other sku", func(f url.Values) { f.Set("sku", "OTHER") })
	tamper("other qty", func(f url.Values) { f.Set("qty", "9") })
	tamper("other plugin", func(f url.Values) { f.Set("plugin", "com.test.other") })
	tamper("no sig", func(f url.Values) { f.Del("sig") })
	if n := len(h.d.Engine.Basket().Lines); n != 0 {
		t.Fatalf("a refused pick added %d lines", n)
	}
	// Revoking view:sales stops an already-drawn button at once.
	if _, err := h.d.Db.Exec(`UPDATE plugin_permissions SET granted = 0 WHERE plugin_id = ? AND permission = ?`, viewPluginID, overlayViewSalesPerm); err != nil {
		t.Fatal(err)
	}
	if r := h.doAs(http.MethodPost, pluginViewPickRoute, forms[0], nil, hdr); r.Code != http.StatusForbidden {
		t.Errorf("revoked consent: status %d, want 403", r.Code)
	}
}

// Outside the overlay a page still refuses suggestions.
func TestOverlayBasket_PageStillRefusesSuggestions_7206(t *testing.T) {
	h := newOverlayBasketHarness(t, true)
	rec := h.do(http.MethodGet, "/plugin/views/next", nil, false)
	if len(pickForms(t, rec.Body.String())) != 0 || strings.Contains(rec.Body.String(), "Skin fade") {
		t.Fatalf("a plain page drew basket suggestions:\n%s", rec.Body.String())
	}
}
