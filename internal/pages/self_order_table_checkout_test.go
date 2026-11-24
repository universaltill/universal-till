package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// No table param at all: behaviour is completely unchanged — kiosk payment
// mode still runs the ordinary card/contactless checkout. A strict
// regression pin alongside the existing TestSelfOrderShop_KioskMode_CheckoutUnchanged.
func TestSelfOrderShop_NoTableCheckout_StillUsesCardFlowInKioskMode(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// Plain /self-order (no ?table=) — same as every kiosk visit today.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/self-order", nil))

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	post("/api/self-order/scan", "code=5000001")

	rec := post("/api/self-order/checkout", "method=card")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST checkout: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var salesCount, counterOrderCount int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status = 'completed'`).Scan(&salesCount)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders`).Scan(&counterOrderCount)
	if salesCount != 1 {
		t.Fatalf("no-table checkout in kiosk mode must still create a completed sale, got %d", salesCount)
	}
	if counterOrderCount != 0 {
		t.Fatalf("no-table checkout in kiosk mode must never create a kiosk_counter_orders row, got %d", counterOrderCount)
	}
}

// The kitchen ticket for a table-bound counter order must show the real
// table label — mirrors #820's print.KitchenTicket.Table for a normal sale.
func TestPrintCounterOrderTicketAsync_IncludesTableLabelWhenSet(t *testing.T) {
	order := data.KioskCounterOrder{
		ID:         "co-table-1",
		DisplayNo:  "C-9",
		OrderType:  "",
		TableID:    "t1",
		TableLabel: "T5",
		CreatedAt:  "2026-09-11T10:00:00Z",
		Lines:      []data.KioskCounterOrderLine{{Name: "Flat White", Qty: 1}},
	}
	ticket := kitchenTicketForCounterOrder(order, print.Config{}, "en")
	if ticket.Table != "T5" {
		t.Fatalf("ticket.Table = %q, want %q", ticket.Table, "T5")
	}
}

// A plain (no table) counter order's ticket must render with no table
// line at all — print.KitchenTicket already treats "" as "print nothing",
// this just pins that the counter-order path passes "" through unchanged.
func TestPrintCounterOrderTicketAsync_NoTableLabelWhenUnset(t *testing.T) {
	order := data.KioskCounterOrder{
		ID:        "co-table-2",
		DisplayNo: "C-10",
		CreatedAt: "2026-09-11T10:00:00Z",
		Lines:     []data.KioskCounterOrderLine{{Name: "Tea", Qty: 1}},
	}
	ticket := kitchenTicketForCounterOrder(order, print.Config{}, "en")
	if ticket.Table != "" {
		t.Fatalf("ticket.Table = %q, want \"\" for a plain counter order", ticket.Table)
	}
}

// The clamp above is table-bound sessions ONLY: a plain kiosk self-order
// session (no ?table=) keeps its dine-in/takeaway toggle working exactly as
// ut-docs#260 shipped it.
func TestSelfOrderShop_NoTable_TakeawayToggleStillSwitches(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/self-order", nil))

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	post("/api/self-order/scan", "code=5000001")
	cart := post("/api/self-order/order-type", "order_type=takeaway")
	if !strings.Contains(cart.Body.String(), `"order_type":"takeaway"`) {
		t.Fatalf("a plain kiosk cart must still offer the takeaway toggle: %s", cart.Body.String())
	}
	if got := dp.KioskEngine.OrderType(); got != "takeaway" {
		t.Fatalf("OrderType() = %q, want takeaway (no table bound, nothing to clamp)", got)
	}
}
