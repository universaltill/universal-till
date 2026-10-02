package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#3192: "inventory.cost_optional" baked a literal £ into the locale
// string itself ("Cost (£, optional)"), so every shop saw a pound sign on
// the stock-cost field regardless of its configured currency. The fix
// follows the existing catalog.col.price precedent (web/ui/pages/
// catalog.html): the locale string carries no symbol, and the template
// interpolates the shop's live httpx.ActiveCurrency().Display via the same
// {{ printf (T "key") arg }} shape already used by catalog.routing.inherited.
func TestInventoryPage_CostLabelFollowsActiveCurrency_NotHardcodedGBP(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	httpx.InitCurrency("EUR")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "£") {
		t.Fatalf("EUR shop's inventory page still shows £ — cost label is not following the active currency:\n%s", body)
	}
	if !strings.Contains(body, "€") {
		t.Fatalf("EUR shop's inventory page never shows the € symbol at all — cost label text:\n%s", body)
	}
}

// Tester follow-up on ut-docs#3192 (TDD-discipline finding, not a product
// bug): the test above checks the whole page BODY for "£"/"€", but every
// page rendered through web/ui/layouts/base.html already stamps
// <body data-currency-display="{{ currency.Display }}" ...> regardless of
// whether the stock-cost label itself interpolates the active currency —
// confirmed by temporarily reverting ONLY the inventory.html call site back
// to {{ T "inventory.cost_optional" }} (leaving the %s-placeholder en.json
// in place): the test above still PASSED, because the body still contained
// "€" from that unrelated data attribute, and never contained "£". A fully
// broken call site — one that leaked the raw, un-interpolated "%s" straight
// to the page ("Cost (%s, optional)") — would have shipped invisibly.
//
// This test scopes the assertion to the <label> that actually wraps
// #stock-cost, for both GBP (so a hardcoded £ and a correctly-resolved £
// can't be confused, per the card's own distinguishing requirement) and
// EUR, and explicitly rejects a leaked "%s".
func TestInventoryPage_CostLabelInterpolatesActiveCurrency_ScopedToLabel(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	cases := []struct {
		code string
		want string
	}{
		{"GBP", "Cost (£, optional)"},
		{"EUR", "Cost (€, optional)"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			httpx.InitCurrency(c.code)
			t.Cleanup(func() { httpx.InitCurrency("GBP") })

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /inventory: %d %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()

			idx := strings.Index(body, `id="stock-cost"`)
			if idx == -1 {
				t.Fatalf("stock-cost input not found in body")
			}
			labelStart := strings.LastIndex(body[:idx], "<label>")
			if labelStart == -1 {
				t.Fatalf("could not find enclosing <label> for stock-cost")
			}
			label := body[labelStart:idx]

			if !strings.Contains(label, c.want) {
				t.Fatalf("[%s] stock-cost label = %q, want it to contain %q", c.code, label, c.want)
			}
			if strings.Contains(label, "%s") {
				t.Fatalf("[%s] stock-cost label leaked an un-interpolated %%s placeholder: %q", c.code, label)
			}
			// "%!" catches every Go fmt error marker, not just "%!s(MISSING)":
			// a locale string with NO verb renders "...%!(EXTRA string=£)",
			// which under GBP still contains the wanted "Cost (£, optional)"
			// substring and would otherwise pass (Reviewer finding, #3192).
			if strings.Contains(label, "%!") {
				t.Fatalf("[%s] stock-cost label leaked a Go fmt verb error: %q", c.code, label)
			}
		})
	}
}
