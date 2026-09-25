package pages

// ut-docs#2702: compact tender panel. The product owner asked for a thinner
// scan row and ONE bottom row -- a Pay {total} button that opens the payment
// panel, plus small icon buttons for Hold, New sale and Open orders (with a
// count badge). The held-sales strip and the standalone quick-pay row are
// gone from the panel; the preferred method keeps its meaning by leading
// (and being highlighted in) the payment panel's own grid.
//
// Rendered-HTML assertions against the real handler + template, same
// fixture as index_quickpay_test.go. Geometry (touch targets, no clipping,
// RTL mirroring) is e2e's job: e2e/tests/compact-tender-panel-2702.spec.ts.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tenderSection isolates the always-visible tender column (everything in
// .tender before the payment overlay dialog opens) so assertions can't be
// satisfied by the overlay's own footer copies of Hold/New sale.
func tenderSection(t *testing.T, home string) string {
	t.Helper()
	start := strings.Index(home, `<div class="tender"`)
	if start == -1 {
		t.Fatalf("tender panel missing from home page")
	}
	end := strings.Index(home[start:], `<dialog id="payment-overlay"`)
	if end == -1 {
		t.Fatalf("payment overlay not found after tender panel")
	}
	return home[start : start+end]
}

// openTag returns the opening tag of the first element carrying marker.
func openTag(t *testing.T, s, marker string) string {
	t.Helper()
	i := strings.Index(s, marker)
	if i == -1 {
		t.Fatalf("%s not found in:\n%s", marker, s)
	}
	lt := strings.LastIndex(s[:i+1], "<")
	gt := strings.Index(s[i:], ">")
	return s[lt : i+gt+1]
}

// buttonHTML returns from the opening tag of the element carrying
// marker to its first </button>.
func buttonHTML(t *testing.T, s, marker string) string {
	t.Helper()
	i := strings.Index(s, marker)
	if i == -1 {
		t.Fatalf("%s not found", marker)
	}
	lt := strings.LastIndex(s[:i+1], "<")
	end := strings.Index(s[i:], "</button>")
	if end == -1 {
		t.Fatalf("%s has no closing </button>", marker)
	}
	return s[lt : i+end+len("</button>")]
}

func TestIndexTender_OneBottomRow(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	tender := tenderSection(t, getHome(t, mux))

	if strings.Contains(tender, "tender-quickpay") || strings.Contains(tender, `data-testid="quick-pay"`) {
		t.Errorf("the standalone quick-pay row must be gone from the tender panel:\n%s", tender)
	}
	if strings.Contains(tender, `id="held-sales"`) || strings.Contains(tender, `hx-get="/ui/held"`) {
		t.Errorf("the held-sales strip must be gone from the tender panel:\n%s", tender)
	}
	if n := strings.Count(tender, `class="tender-default-footer"`); n != 1 {
		t.Fatalf("want exactly one bottom row, got %d", n)
	}

	footer := tender[strings.Index(tender, `class="tender-default-footer"`):]
	// Pay leads, then Hold, New sale, Open orders -- in DOM order, so the
	// row mirrors under RTL with no left/right literals.
	order := []string{`data-testid="payment-open"`, `data-testid="tender-footer-hold"`, `data-testid="kiosk-checkout-start"`, `data-testid="parked-orders-open"`}
	last := -1
	for _, m := range order {
		i := strings.Index(footer, m)
		if i == -1 {
			t.Fatalf("%s missing from the bottom row:\n%s", m, footer)
		}
		if i < last {
			t.Fatalf("%s is out of order in the bottom row (want Pay, Hold, New sale, Open orders)", m)
		}
		last = i
	}

	// Pay keeps its primary styling and its posOpenPayment() entry point
	// (the "at pay" order-type gate lives there, ut-docs#2282).
	pay := openTag(t, footer, `data-testid="payment-open"`)
	for _, want := range []string{"btn primary", "payment-trigger", `onclick="posOpenPayment()"`} {
		if !strings.Contains(pay, want) {
			t.Errorf("Pay button lost %q: %s", want, pay)
		}
	}

	// Each icon button: an icon, a tooltip and an accessible name from real
	// (visually hidden) text -- never an unlabeled glyph.
	for marker, name := range map[string]string{
		`data-testid="tender-footer-hold"`:   "Hold Sale",
		`data-testid="kiosk-checkout-start"`: "New Sale",
		`data-testid="parked-orders-open"`:   "Open orders",
	} {
		btn := buttonHTML(t, footer, marker)
		if !strings.Contains(btn, "tender-icon-btn") {
			t.Errorf("%s must be a compact icon button: %s", marker, btn)
		}
		if !strings.Contains(btn, `title="`+name+`"`) {
			t.Errorf("%s needs a %q tooltip: %s", marker, name, btn)
		}
		if !strings.Contains(btn, `<span class="visually-hidden">`+name+`</span>`) {
			t.Errorf("%s needs %q as its accessible name: %s", marker, name, btn)
		}
		if !strings.Contains(btn, "<svg") {
			t.Errorf("%s must draw an icon: %s", marker, btn)
		}
	}

	// Hold / New sale / Open orders keep their existing behaviour.
	if hold := openTag(t, footer, `data-testid="tender-footer-hold"`); !strings.Contains(hold, "hold-modal") {
		t.Errorf("Hold must still open #hold-modal: %s", hold)
	}
	if ns := openTag(t, footer, `data-testid="kiosk-checkout-start"`); !strings.Contains(ns, `hx-post="/api/pos/reset"`) {
		t.Errorf("New sale must still POST /api/pos/reset: %s", ns)
	}
	oo := openTag(t, footer, `data-testid="parked-orders-open"`)
	if !strings.Contains(oo, `hx-get="/ui/parked-orders"`) || !strings.Contains(oo, "parked-orders-modal") {
		t.Errorf("Open orders must still open the parked-orders popup: %s", oo)
	}

	// The count badge refreshes itself on load and whenever a sale is held
	// or resumed (hold_api.go's HX-Trigger: held-changed).
	badge := openTag(t, footer, `data-testid="open-orders-badge"`)
	// hx-target="this": htmx inherits hx-target, and the badge sits inside
	// the Open orders button whose target is the popup body.
	for _, want := range []string{`hx-get="/ui/open-orders-badge"`, "held-changed from:body", `hx-target="this"`, `hx-swap="outerHTML"`} {
		if !strings.Contains(badge, want) {
			t.Errorf("open-orders badge lost %q: %s", want, badge)
		}
	}
}

func TestIndexTender_ScanRowIsCompactIcons(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	tender := tenderSection(t, getHome(t, mux))
	scan := tender[strings.Index(tender, `<form class="scan-row"`):strings.Index(tender, "</form>")]

	add := buttonHTML(t, scan, `type="submit"`)
	if !strings.Contains(add, "tender-icon-btn") || !strings.Contains(add, "<svg") {
		t.Errorf("Add must be a compact icon button: %s", add)
	}
	if !strings.Contains(add, `<span class="visually-hidden">Add</span>`) {
		t.Errorf("Add must keep the accessible name \"Add\": %s", add)
	}
	for _, marker := range []string{"data-osk-toggle", `id="barcode-scan-open"`} {
		btn := buttonHTML(t, scan, marker)
		if !strings.Contains(btn, "tender-icon-btn") || !strings.Contains(btn, "<svg") {
			t.Errorf("%s must be a compact icon button with a vector icon: %s", marker, btn)
		}
	}
}

// The quick-pay button's job -- one tap on the shop's preferred method --
// moves into the payment panel: the preferred method leads the Pay grid and
// is highlighted there, so payments.default_method still means something.
func TestIndexTender_PreferredMethodLeadsPaymentPanel(t *testing.T) {
	mux, dp := quickPayTestMux(t)
	if err := dp.Settings.Set(t.Context(), "payments.default_method", "cash"); err != nil {
		t.Fatalf("set payments.default_method: %v", err)
	}
	home := getHome(t, mux)
	grid := payGridSnippet(t, home)
	first := openTag(t, grid, "<button")
	if !strings.Contains(first, `data-method="cash"`) {
		t.Fatalf("preferred method must lead the Pay grid: %s", first)
	}
	if !strings.Contains(first, "pay-default") || !strings.Contains(first, `data-testid="pay-default"`) {
		t.Fatalf("preferred method's button must be marked as the default: %s", first)
	}
	if n := strings.Count(grid, "pay-default"); n != 2 { // class + testid, once
		t.Fatalf("only the preferred method may be marked default, found %d markers:\n%s", n, grid)
	}
}

func TestOpenOrdersBadge_CountsHeldSales(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	registerOpenOrdersBadge(mux, d)
	get := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/open-orders-badge", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ui/open-orders-badge = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	empty := get()
	if !strings.Contains(empty, `data-testid="open-orders-badge"`) || !strings.Contains(empty, `data-count="0"`) {
		t.Fatalf("empty badge must still render (as the swap target) with count 0: %s", empty)
	}
	if !strings.Contains(empty, " hidden") {
		t.Fatalf("a zero count must hide the badge: %s", empty)
	}
	if !strings.Contains(empty, `hx-trigger="held-changed from:body"`) || !strings.Contains(empty, `hx-target="this"`) {
		t.Fatalf("the swapped-in badge must keep listening for held-changed: %s", empty)
	}

	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at) VALUES
 ('h1','Table 4',1250,3,'{}',datetime('now')),
 ('h2','Sarah',500,1,'{}',datetime('now'))`); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}
	two := get()
	if !strings.Contains(two, `data-count="2"`) || !strings.Contains(two, ">2<") {
		t.Fatalf("badge must show 2 held sales: %s", two)
	}
	if strings.Contains(two, " hidden") {
		t.Fatalf("a non-zero badge must be visible: %s", two)
	}
}
