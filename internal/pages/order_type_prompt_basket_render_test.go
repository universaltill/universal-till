package pages

import (
	"regexp"
	"testing"
)

// ut-docs#2282: the sale screen's own JS (web/public/app.js) decides
// whether an item-add/Pay action still owes the cashier the "before first
// item"/"at Pay" intercept modal by reading #basket's data-lines-count and
// data-order-type-chosen attributes (basket.html) -- these Go tests pin
// the server-side half of that contract: the values actually rendered
// under real basket state transitions. The client-side interception itself
// is covered by the e2e spec (e2e/tests/order-type-prompt-placement-2282.spec.ts).

var linesCountAttrRE = regexp.MustCompile(`data-lines-count="(\d+)"`)
var orderTypeChosenAttrRE = regexp.MustCompile(`data-order-type-chosen="(true|false)"`)

func TestBasketRender_LinesCountAndOrderTypeChosenAttributes(t *testing.T) {
	mux, _ := newPOSTestDeps(t)

	// Fresh basket: no lines, never asked.
	rec := posPostForm(mux, "/api/pos/scan", "code=")
	body := rec.Body.String()
	if m := linesCountAttrRE.FindStringSubmatch(body); m == nil || m[1] != "0" {
		t.Fatalf("fresh basket data-lines-count = %v, want 0: %s", m, body)
	}
	if m := orderTypeChosenAttrRE.FindStringSubmatch(body); m == nil || m[1] != "false" {
		t.Fatalf("fresh basket data-order-type-chosen = %v, want false: %s", m, body)
	}

	// One item added: lines count goes to 1, still never explicitly asked.
	rec = posPostForm(mux, "/api/pos/scan", "code=PLAIN")
	body = rec.Body.String()
	if m := linesCountAttrRE.FindStringSubmatch(body); m == nil || m[1] != "1" {
		t.Fatalf("after one scan data-lines-count = %v, want 1: %s", m, body)
	}
	if m := orderTypeChosenAttrRE.FindStringSubmatch(body); m == nil || m[1] != "false" {
		t.Fatalf("after one scan (no order-type choice yet) data-order-type-chosen = %v, want false: %s", m, body)
	}

	// Choosing an order type (the basket-top toggle, or the new intercept
	// modal -- both POST here) flips the chosen flag for the rest of the sale.
	rec = posPostForm(mux, "/api/pos/order-type", "order_type=takeaway")
	body = rec.Body.String()
	if m := orderTypeChosenAttrRE.FindStringSubmatch(body); m == nil || m[1] != "true" {
		t.Fatalf("after SetOrderType data-order-type-chosen = %v, want true: %s", m, body)
	}

	// A new sale (reset) clears it again.
	rec = posPostForm(mux, "/api/pos/reset", "")
	body = rec.Body.String()
	if m := orderTypeChosenAttrRE.FindStringSubmatch(body); m == nil || m[1] != "false" {
		t.Fatalf("after reset data-order-type-chosen = %v, want false: %s", m, body)
	}
	if m := linesCountAttrRE.FindStringSubmatch(body); m == nil || m[1] != "0" {
		t.Fatalf("after reset data-lines-count = %v, want 0: %s", m, body)
	}
}
