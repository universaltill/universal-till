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

// ut-docs#815 core acceptance criterion: when the self-order session
// started with a valid table param, checkout must ALWAYS produce a
// kiosk_counter_orders row with table_id set and never attempt a sale/
// card payment — regardless of the till's own kiosk.payment_mode. Kiosk
// mode ("kiosk", the till-wide default) is used here deliberately: this is
// exactly the setting a table-bound guest checkout must override.
func TestSelfOrderShop_TableCheckout_AlwaysCounterOrderRegardlessOfPaymentMode(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk // explicit: card/contactless mode
	tableID, err := data.NewPOSRepo(d.DB).CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// Table QR entry — the guest's own phone (ut-docs#2261: a per-session
	// basket reached through the cookie the scan sets, so every request
	// below is threaded through the same jar).
	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableID)
	g.post("/api/self-order/scan", "code=5000001")

	// GET checkout must render the counter-confirm screen, never the
	// payment-method picker — a guest's own phone has no card terminal.
	getRec := g.get("/api/self-order/checkout")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET checkout: want 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	if strings.Contains(getRec.Body.String(), `name="method"`) {
		t.Fatalf("table-bound checkout must never offer a payment method, got: %s", getRec.Body.String())
	}

	var salesBefore int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesBefore)

	rec := g.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST checkout: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Order placed") {
		t.Fatalf("expected the counter-mode confirmation screen: %s", rec.Body.String())
	}

	var salesAfter int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesAfter)
	if salesAfter != salesBefore {
		t.Fatalf("table checkout must never create a sale, even in kiosk payment_mode: before=%d after=%d", salesBefore, salesAfter)
	}

	var gotTableID, displayNo string
	if err := d.DB.QueryRow(`SELECT display_no, COALESCE(table_id, '') FROM kiosk_counter_orders`).Scan(&displayNo, &gotTableID); err != nil {
		t.Fatalf("expected exactly one kiosk_counter_orders row: %v", err)
	}
	if gotTableID != tableID {
		t.Fatalf("kiosk_counter_orders.table_id = %q, want %q", gotTableID, tableID)
	}
	if !strings.HasPrefix(displayNo, "C-") {
		t.Fatalf("counter order display_no %q must be C--prefixed", displayNo)
	}
}

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

// ut-docs#815 review finding, BLOCKER 1: the cart's own dine-in/takeaway
// toggle must not be able to unbind a table-bound session. Before the fix,
// one tap on Takeaway ran SetOrderType -> applyTablePolicyLocked (ADR-0073
// D5: no dine-in line, no table), which cleared the very TableID
// selfOrderForcesCounterCheckout reads — so the guest was dropped back onto
// the card/contactless payment picker on a phone with no card terminal, and
// a real sale row was completed. The session must stay dine-in, keep its
// table, and still check out as a counter order.
func TestSelfOrderShop_TableCheckout_TakeawayToggleCannotUnbindTable(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk // the mode a table session must override
	tableID, err := data.NewPOSRepo(d.DB).CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableID)
	g.post("/api/self-order/scan", "code=5000001")

	// The toggle isn't offered for a table-bound session (the guest sees
	// which table they're ordering to instead) — but this surface is
	// anonymous and auth-exempt, so the POST is reachable regardless and the
	// server is the enforcement point (same pairing as ut-docs#1355).
	cart := g.post("/api/self-order/order-type", "order_type=takeaway")
	if strings.Contains(cart.Body.String(), `"order_type":"takeaway"`) {
		t.Fatalf("a table-bound cart must not offer the takeaway toggle: %s", cart.Body.String())
	}
	if !strings.Contains(cart.Body.String(), "T5") {
		t.Fatalf("a table-bound cart must tell the guest which table it is bound to: %s", cart.Body.String())
	}
	if got := g.engine(dp).TableID(); got != tableID {
		t.Fatalf("session TableID() after a takeaway toggle = %q, want %q (the table must stay bound)", got, tableID)
	}

	getRec := g.get("/api/self-order/checkout")
	if strings.Contains(getRec.Body.String(), `name="method"`) {
		t.Fatalf("a table-bound checkout must never offer a payment method: %s", getRec.Body.String())
	}

	if rec := g.post("/api/self-order/checkout", "method=card"); rec.Code != http.StatusOK {
		t.Fatalf("POST checkout: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var sales, counterOrders int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders WHERE table_id = ?`, tableID).Scan(&counterOrders)
	if sales != 0 {
		t.Fatalf("a table-bound session must never create a sale, got %d", sales)
	}
	if counterOrders != 1 {
		t.Fatalf("want exactly 1 counter order still carrying table_id, got %d", counterOrders)
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

// ut-docs#815 review finding, BLOCKER 2: the shop screen's idle timer
// (ADR-0020, 60s by default) navigates back to /self-order, which Reset()s
// the basket — on a guest's own phone that used to throw away the table
// binding after a minute of not touching the screen, so the next checkout
// silently fell back to the card/contactless path. The bounce must carry
// ?table= so the same table re-binds (and is re-validated) on the way back.
func TestSelfOrderShop_IdleResetKeepsTableBoundSession(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableID, err := data.NewPOSRepo(d.DB).CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableID)

	rec := g.get("/self-order/shop")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order/shop: want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/self-order?table="+tableID) {
		t.Fatalf("the idle-reset bounce must carry the bound table: %s", rec.Body.String())
	}

	// A plain kiosk session (no table, no session cookie) keeps today's
	// bare /self-order bounce.
	plain := httptest.NewRecorder()
	mux.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/self-order/shop", nil))
	if strings.Contains(plain.Body.String(), "/self-order?table=") {
		t.Fatalf("a no-table kiosk session must bounce to a bare /self-order: %s", plain.Body.String())
	}
}
