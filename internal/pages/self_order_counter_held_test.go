package pages

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#2703 (product owner, 2026-09-25): "when the customer still didn't
// pay for the order and waits for pay at the counter, it should be exactly
// the same as a hold order, not a placed one." A pay-at-counter kiosk
// checkout now parks the kiosk basket as a HELD SALE -- the same row the
// till's own Hold writes -- so it shows in Open orders, is resumed with the
// existing resume path, and is paid through the normal tender: a real,
// fiscally signed sale instead of a "Mark collected" with no money taken.

// fakeKitchenPrinter is a TCP "printer" that counts the tickets it receives
// and keeps their bytes. Each print is one connection (print's TCP
// transport dials per job).
type fakeKitchenPrinter struct {
	addr    string
	mu      sync.Mutex
	tickets []string
}

func newFakeKitchenPrinter(t *testing.T) *fakeKitchenPrinter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &fakeKitchenPrinter{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			b, _ := io.ReadAll(bufio.NewReader(c))
			_ = c.Close()
			if len(b) == 0 {
				continue
			}
			p.mu.Lock()
			p.tickets = append(p.tickets, string(b))
			p.mu.Unlock()
		}
	}()
	return p
}

// Settle waits (bounded) for at least want tickets, then a short grace
// period so a stray extra ticket is caught too, and returns what arrived.
// The server side records a ticket only after the client closes the
// connection, which can land just after the sender's AsyncWork is done.
func (p *fakeKitchenPrinter) Settle(want int) []string {
	deadline := time.Now().Add(3 * time.Second)
	for len(p.Tickets()) < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	return p.Tickets()
}

func (p *fakeKitchenPrinter) Tickets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.tickets...)
}

// counterHeldHarness is one till serving both the kiosk (KioskEngine) and
// the cashier (Engine), with every surface this flow crosses registered on
// one mux: kiosk shop, hold/resume, Open orders, tender.
type counterHeldHarness struct {
	t   *testing.T
	dp  *common.Deps
	db  *db.DB
	mux *http.ServeMux
}

func newCounterHeldHarness(t *testing.T) *counterHeldHarness {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeCounter
	t.Cleanup(dp.WaitForAsyncWork)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	seedShopItem(t, d, "itm-cake", "CAKE", "5000002", "Carrot Cake", 450)
	seedStock(t, d, "itm-cake", 10)
	for _, q := range []string{
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g1','Extras',0,0,2,1)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-coffee','g1',1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o1','g1','Extra shot',50,1)`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	registerHoldAPI(mux, dp)
	registerOpenOrders(mux, dp)
	registerPOSAPI(mux, dp)
	return &counterHeldHarness{t: t, dp: dp, db: d, mux: mux}
}

func (h *counterHeldHarness) post(path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func (h *counterHeldHarness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// kioskOrder rings a coffee with an extra shot (320+50) and a cake (450) on
// the walk-up kiosk as takeaway, then places the pay-at-counter order.
func (h *counterHeldHarness) kioskOrder() *httptest.ResponseRecorder {
	h.t.Helper()
	h.post("/api/self-order/order-type", "order_type=takeaway")
	form := url.Values{"code": {"5000001"}, "itemId": {"itm-coffee"}, "mod_g1": {"o1"}}
	if rec := h.post("/api/self-order/scan-with-modifiers", form.Encode()); rec.Code != http.StatusOK {
		h.t.Fatalf("scan-with-modifiers = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := h.post("/api/self-order/scan", "code=5000002"); rec.Code != http.StatusOK {
		h.t.Fatalf("scan cake = %d: %s", rec.Code, rec.Body.String())
	}
	rec := h.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK {
		h.t.Fatalf("counter checkout = %d: %s", rec.Code, rec.Body.String())
	}
	return rec
}

func (h *counterHeldHarness) heldSales() []data.HeldSale {
	h.t.Helper()
	rows, err := data.NewHeldSalesRepo(h.dp.Db).List(context.Background())
	if err != nil {
		h.t.Fatalf("list held sales: %v", err)
	}
	return rows
}

func (h *counterHeldHarness) onlyHeld() data.HeldSale {
	h.t.Helper()
	rows := h.heldSales()
	if len(rows) != 1 {
		h.t.Fatalf("want exactly one held sale, got %d: %+v", len(rows), rows)
	}
	return rows[0]
}

func (h *counterHeldHarness) tenderCash() *httptest.ResponseRecorder {
	h.t.Helper()
	total := h.dp.Engine.Basket().Total.Minor()
	body := fmt.Sprintf(`{"payments":[{"method":"cash","amount":%d}]}`, total)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func TestCounterCheckout_ParksTheKioskBasketAsAHeldSale(t *testing.T) {
	h := newCounterHeldHarness(t)
	var salesBefore int
	_ = h.db.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesBefore)

	rec := h.kioskOrder()
	// The customer still gets their C-number confirmation.
	if !strings.Contains(rec.Body.String(), "C-1") {
		t.Fatalf("confirmation must show the order number C-1: %s", rec.Body.String())
	}
	// A walk-up order is made once it is paid (the kitchen ticket prints at
	// tender), so the kiosk must not tell the customer it is already on its
	// way to the kitchen.
	if !strings.Contains(html.UnescapeString(rec.Body.String()), httpx.T("en", "selforder.confirm.counter_pay_first_hint")) {
		t.Fatalf("walk-up counter confirmation must say the order is made after payment: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), httpx.T("en", "selforder.confirm.counter_hint")) {
		t.Fatalf("walk-up counter confirmation must not claim the order is already on its way to the kitchen: %s", rec.Body.String())
	}

	held := h.onlyHeld()
	if !strings.HasPrefix(held.Label, "C-1") {
		t.Fatalf("held label = %q, want it to start with the order number C-1", held.Label)
	}
	if !strings.Contains(held.Label, "Takeaway") {
		t.Fatalf("held label = %q, want the order type", held.Label)
	}
	if held.LineCount != 2 {
		t.Fatalf("held line_count = %d, want 2", held.LineCount)
	}
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
		t.Fatalf("payload is not a BasketSnapshot: %v", err)
	}
	if snap.DisplayNo != "C-1" {
		t.Fatalf("snapshot display_no = %q, want C-1", snap.DisplayNo)
	}
	for _, l := range snap.Lines {
		if l.KitchenSentQty != 0 {
			t.Fatalf("a walk-up counter order has not been sent to the kitchen yet: %+v", l)
		}
	}
	if snap.OrderType != pos.OrderTypeTakeaway {
		t.Fatalf("snapshot order_type = %q, want takeaway", snap.OrderType)
	}
	if len(snap.Lines) != 2 {
		t.Fatalf("snapshot lines = %+v", snap.Lines)
	}
	coffee, cake := snap.Lines[0], snap.Lines[1]
	if coffee.ItemID != "itm-coffee" || coffee.PriceCents.Minor() != 370 || coffee.Qty != 1 {
		t.Fatalf("coffee line = %+v, want itm-coffee at 370 (320 + 50 extra shot)", coffee)
	}
	if len(coffee.Modifiers) != 1 || coffee.Modifiers[0].OptionID != "o1" || coffee.Modifiers[0].OptionName != "Extra shot" {
		t.Fatalf("coffee modifiers = %+v, want the extra shot", coffee.Modifiers)
	}
	if cake.ItemID != "itm-cake" || cake.PriceCents.Minor() != 450 {
		t.Fatalf("cake line = %+v", cake)
	}
	if held.TotalMinor != snap.Total.Minor() || held.TotalMinor <= 0 {
		t.Fatalf("held total_minor = %d, snapshot total = %d", held.TotalMinor, snap.Total.Minor())
	}

	// Still no sale: nothing has been paid.
	var salesAfter int
	_ = h.db.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesAfter)
	if salesAfter != salesBefore {
		t.Fatalf("placing a pay-at-counter order must not create a sale: %d -> %d", salesBefore, salesAfter)
	}
	// And it no longer sits on the legacy "pay at counter" board.
	open, err := data.NewKioskCounterOrdersRepo(h.dp.Db).ListOpen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("a new counter order must not be listed as an open legacy counter order: %+v", open)
	}
	if len(h.dp.KioskEngine.Basket().Lines) != 0 {
		t.Fatal("the kiosk basket must be cleared after placing the order")
	}
}

func TestCounterCheckout_OrderShowsInOpenOrdersAndPopup(t *testing.T) {
	h := newCounterHeldHarness(t)
	h.kioskOrder()
	held := h.onlyHeld()

	page := h.get("/open-orders")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), held.Label) {
		t.Fatalf("/open-orders must list %q: %d %s", held.Label, page.Code, page.Body.String())
	}
	popup := h.get("/ui/parked-orders")
	if popup.Code != http.StatusOK || !strings.Contains(popup.Body.String(), held.Label) {
		t.Fatalf("/ui/parked-orders must list %q: %s", held.Label, popup.Body.String())
	}
	if !strings.Contains(popup.Body.String(), `data-held-id="`+held.ID+`"`) {
		t.Fatalf("popup row must resume held id %s: %s", held.ID, popup.Body.String())
	}
}

func TestCounterCheckout_ResumeAndCashTenderIsASignedSale(t *testing.T) {
	h := newCounterHeldHarness(t)
	var signCalls atomic.Int32
	var signedSaleID atomic.Value
	subscribeFiscalSignHandler(t, h.dp, "com.test.fiscal-sign-counter", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		signCalls.Add(1)
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		if id, ok := p["sale_id"].(string); ok {
			signedSaleID.Store(id)
		}
		return json.RawMessage(`{"status":"approved"}`), nil
	})

	h.kioskOrder()
	held := h.onlyHeld()
	var snap pos.BasketSnapshot
	_ = json.Unmarshal([]byte(held.Payload), &snap)

	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	lines := h.dp.Engine.Lines()
	if len(lines) != 2 || lines[0].ItemID != "itm-coffee" || lines[0].PriceCents.Minor() != 370 ||
		len(lines[0].Modifiers) != 1 || lines[1].ItemID != "itm-cake" {
		t.Fatalf("resumed basket lines = %+v", lines)
	}
	if got := h.dp.Engine.Basket().Total; got != snap.Total {
		t.Fatalf("resumed total = %d, want the order-time total %d", got.Minor(), snap.Total.Minor())
	}
	if len(h.heldSales()) != 0 {
		t.Fatal("resuming the order must take it off Open orders, like any held sale")
	}

	rec := h.tenderCash()
	if rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	var saleID, displayNo, status, orderType string
	var total int64
	if err := h.db.DB.QueryRow(`SELECT id, display_no, status, COALESCE(order_type,''), total FROM sales`).Scan(&saleID, &displayNo, &status, &orderType, &total); err != nil {
		t.Fatalf("want exactly one sale: %v", err)
	}
	if status != "completed" {
		t.Fatalf("sale status = %q", status)
	}
	if total != snap.Total.Minor() {
		t.Fatalf("sale total = %d, want the order-time total %d", total, snap.Total.Minor())
	}
	if displayNo != "C-1" {
		t.Fatalf("sale display_no = %q, want the customer's order number C-1", displayNo)
	}
	if orderType != pos.OrderTypeTakeaway {
		t.Fatalf("sale order_type = %q, want takeaway", orderType)
	}
	if n := signCalls.Load(); n != 1 {
		t.Fatalf("fiscal.sign.ask dispatched %d times, want exactly once for the paid order", n)
	}
	if got, _ := signedSaleID.Load().(string); got != saleID {
		t.Fatalf("signed sale id = %q, want %q", got, saleID)
	}
	if n := countAuditRows(t, h.dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("approved signing must leave no unsigned marker, got %d", n)
	}
	if len(h.dp.Engine.Lines()) != 0 || h.dp.Engine.OrderDisplayNo() != "" {
		t.Fatal("the next sale must start clean: no lines, no carried order number")
	}
}

func TestCounterCheckout_WalkUpKitchenTicketPrintsOnceAtPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	h.kioskOrder()
	h.dp.WaitForAsyncWork()
	if n := len(printer.Settle(0)); n != 0 {
		t.Fatalf("a pay-at-counter order must not reach the kitchen before it is paid, got %d tickets", n)
	}
	held := h.onlyHeld()
	h.post("/api/pos/resume", "id="+held.ID)
	// park -> resume again: still nothing has been sent, still nothing prints.
	if rec := h.post("/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-park = %d", rec.Code)
	}
	held = h.onlyHeld()
	h.post("/api/pos/resume", "id="+held.ID)
	h.dp.WaitForAsyncWork()
	if n := len(printer.Settle(0)); n != 0 {
		t.Fatalf("resuming must not print, got %d tickets", n)
	}
	if rec := h.tenderCash(); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	tickets := printer.Settle(1)
	if len(tickets) != 1 {
		t.Fatalf("want exactly one kitchen ticket at payment, got %d", len(tickets))
	}
	if !strings.Contains(tickets[0], "C-1") || !strings.Contains(tickets[0], "Flat White") {
		t.Fatalf("kitchen ticket must carry the customer's order number and items: %q", tickets[0])
	}
}

func TestCounterCheckout_TableOrderPrintsAtCheckoutNeverAgain(t *testing.T) {
	h := newCounterHeldHarness(t)
	h.dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk // a table always forces the counter path
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	tableID, err := data.NewPOSRepo(h.db.DB).CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	g := newSelfOrderGuest(t, h.mux)
	g.get("/self-order?table=" + tableID)
	g.post("/api/self-order/scan", "code=5000002")
	rec := g.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("table checkout = %d: %s", rec.Code, rec.Body.String())
	}
	// A table order IS on its way to the kitchen: keep that copy.
	if !strings.Contains(rec.Body.String(), httpx.T("en", "selforder.confirm.counter_hint")) {
		t.Fatalf("table-QR confirmation must keep the on-its-way-to-the-kitchen copy: %s", rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	if n := len(printer.Settle(1)); n != 1 {
		t.Fatalf("a dine-in table order goes to the kitchen at once: want 1 ticket, got %d", n)
	}

	held := h.onlyHeld()
	if held.TableID != tableID {
		t.Fatalf("held table_id = %q, want %q", held.TableID, tableID)
	}
	var snap pos.BasketSnapshot
	_ = json.Unmarshal([]byte(held.Payload), &snap)
	if len(snap.Lines) != 1 || snap.Lines[0].KitchenSentQty != snap.Lines[0].Qty {
		t.Fatalf("the held table order must remember every line was already sent: %+v", snap.Lines)
	}

	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d", rec.Code)
	}
	if h.dp.Engine.TableID() != tableID {
		t.Fatalf("resumed basket table = %q, want %q", h.dp.Engine.TableID(), tableID)
	}
	// Park it again and resume it again: the flag must survive a re-park.
	if rec := h.post("/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-park = %d", rec.Code)
	}
	held = h.onlyHeld()
	h.post("/api/pos/resume", "id="+held.ID)
	if rec := h.tenderCash(); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	if n := len(printer.Settle(1)); n != 1 {
		t.Fatalf("paying a table order must not reprint its kitchen ticket: got %d tickets in total", n)
	}
}

// tableOrder places a table-QR order for one Carrot Cake at table T5.
func (h *counterHeldHarness) tableOrder() (*httptest.ResponseRecorder, string) {
	h.t.Helper()
	h.dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk // a table always forces the counter path
	tableID, err := data.NewPOSRepo(h.db.DB).CreateTable(h.t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	g := newSelfOrderGuest(h.t, h.mux)
	g.get("/self-order?table=" + tableID)
	g.post("/api/self-order/scan", "code=5000002")
	rec := g.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK {
		h.t.Fatalf("table checkout = %d: %s", rec.Code, rec.Body.String())
	}
	return rec, tableID
}

// F1 (review of ut-docs#2703): the kitchen tracks what it was sent PER
// LINE. A table order printed at checkout, then two mains added by the
// cashier after recalling it: payment prints ONLY the two mains -- before,
// a basket-level "already sent" flag swallowed them entirely.
func TestCounterCheckout_TableOrderItemsAddedAtTillPrintAtPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	seedShopItem(t, h.db, "itm-burger", "BURGER", "5000003", "Burger", 1200)
	seedStock(t, h.db, "itm-burger", 10)
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	h.tableOrder()
	h.dp.WaitForAsyncWork()
	if n := len(printer.Settle(1)); n != 1 {
		t.Fatalf("the table order prints at checkout: want 1 ticket, got %d", n)
	}
	held := h.onlyHeld()
	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d", rec.Code)
	}
	if rec := h.post("/api/pos/scan", "code=5000003&qty=2"); rec.Code != http.StatusOK {
		t.Fatalf("scan burgers = %d: %s", rec.Code, rec.Body.String())
	}
	// Park and resume once more: the per-line record must survive it.
	if rec := h.post("/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-park = %d", rec.Code)
	}
	held = h.onlyHeld()
	h.post("/api/pos/resume", "id="+held.ID)
	if rec := h.tenderCash(); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	tickets := printer.Settle(2)
	if len(tickets) != 2 {
		t.Fatalf("want the checkout ticket plus ONE ticket at payment, got %d", len(tickets))
	}
	paid := tickets[1]
	if !strings.Contains(paid, "2 x Burger") {
		t.Fatalf("the payment ticket must carry the two mains added at the till: %q", paid)
	}
	if strings.Contains(paid, "Carrot Cake") {
		t.Fatalf("the payment ticket must not repeat what the kitchen already has: %q", paid)
	}
	if !strings.Contains(paid, "C-1") {
		t.Fatalf("the payment ticket must carry the order number: %q", paid)
	}
}

// F1: a printer outage at checkout must not lose the ticket. Nothing is
// marked sent unless the print succeeded, so the whole order prints at
// payment, and the guest is not told it is already on its way.
func TestCounterCheckout_TableOrderPrinterDownAtCheckoutPrintsAtPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := ln.Addr().String()
	_ = ln.Close() // nothing listens here: every print fails fast
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, deadAddr); err != nil {
		t.Fatal(err)
	}
	rec, _ := h.tableOrder()
	if !strings.Contains(html.UnescapeString(rec.Body.String()), httpx.T("en", "selforder.confirm.counter_pay_first_hint")) {
		t.Fatalf("with the kitchen ticket not sent, the guest must not be told it is on its way: %s", rec.Body.String())
	}
	held := h.onlyHeld()
	var snap pos.BasketSnapshot
	_ = json.Unmarshal([]byte(held.Payload), &snap)
	for _, l := range snap.Lines {
		if l.KitchenSentQty != 0 {
			t.Fatalf("a failed checkout print must not mark the line sent: %+v", l)
		}
	}

	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d", rec.Code)
	}
	if rec := h.tenderCash(); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	tickets := printer.Settle(1)
	if len(tickets) != 1 || !strings.Contains(tickets[0], "Carrot Cake") {
		t.Fatalf("the whole order must print at payment after a failed checkout print, got %q", tickets)
	}
}

// F1: the tender-time filter prints only the per-line delta, drops a line
// reduced to (or below) what was already sent -- never a negative qty --
// prints a line in full when the positions do not line up (a duplicate is
// recoverable, a lost ticket is not), and is nil for a basket nothing was
// ever sent from, so a normal sale prints exactly as before.
func TestKitchenDeltaFilter(t *testing.T) {
	if f := kitchenDeltaFilter([]pos.BasketLine{{SKU: "A", Qty: 2}}); f != nil {
		t.Fatal("nothing sent: the filter must be nil (unchanged full print)")
	}
	f := kitchenDeltaFilter([]pos.BasketLine{
		{SKU: "A", Qty: 3, KitchenSentQty: 1}, // two more added after sending
		{SKU: "B", Qty: 1, KitchenSentQty: 2}, // voided down after sending
		{SKU: "C", Qty: 1, KitchenSentQty: 1}, // fully sent
		{SKU: "D", Qty: 1},                    // new line
	})
	cases := []struct {
		i       int
		sku     string
		wantQty float64
		keep    bool
	}{
		{0, "A", 2, true},
		{1, "B", 0, false},
		{2, "C", 0, false},
		{3, "D", 1, true},
		{2, "X", 5, true}, // position/SKU mismatch: print in full
		{9, "Z", 4, true}, // beyond the basket: print in full
	}
	for _, c := range cases {
		in := data.SaleDetailLine{SKU: c.sku, Qty: 5}
		if c.i == 9 {
			in.Qty = 4
		}
		got, keep := f(c.i, in)
		if keep != c.keep || (keep && got.Qty != c.wantQty) {
			t.Fatalf("line %d %s: got qty %v keep %v, want %v %v", c.i, c.sku, got.Qty, keep, c.wantQty, c.keep)
		}
	}
}

// F2: on a till with a sync.receipt_prefix the C-number carries it, and the
// customer's confirmation, the Open orders label, the paid sale's
// display_no and the kitchen ticket all show the SAME prefixed number.
func TestCounterCheckout_OrderNumberCarriesTillPrefixEverywhere(t *testing.T) {
	h := newCounterHeldHarness(t)
	if err := h.dp.Settings.Set(t.Context(), "sync.receipt_prefix", "T2-"); err != nil {
		t.Fatal(err)
	}
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	rec := h.kioskOrder()
	if !strings.Contains(rec.Body.String(), "C-T2-1") {
		t.Fatalf("confirmation must show the prefixed number C-T2-1: %s", rec.Body.String())
	}
	held := h.onlyHeld()
	if !strings.HasPrefix(held.Label, "C-T2-1 ") {
		t.Fatalf("held label = %q, want it to start with C-T2-1", held.Label)
	}
	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d", rec.Code)
	}
	if rec := h.tenderCash(); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	var displayNo string
	if err := h.db.DB.QueryRow(`SELECT display_no FROM sales`).Scan(&displayNo); err != nil {
		t.Fatal(err)
	}
	if displayNo != "C-T2-1" {
		t.Fatalf("sale display_no = %q, want C-T2-1", displayNo)
	}
	tickets := printer.Settle(1)
	if len(tickets) != 1 || !strings.Contains(tickets[0], "C-T2-1") {
		t.Fatalf("kitchen ticket must carry C-T2-1, got %q", tickets)
	}
}

// Round-2 review (MEDIUM): the table-QR checkout reads the basket ONCE. The
// kitchen ticket's lines and the "already sent" marks both come from the
// same snapshot, so a line the ticket never showed can never be marked
// sent. Built 1:1 by position from snap.Lines, marking sets exactly those.
func TestCounterOrderTicket_LinesAndMarksFromOneSnapshot(t *testing.T) {
	snap := pos.BasketSnapshot{Lines: []pos.SnapshotLine{
		{SKU: "A", Name: "Flat White", Qty: 2, Modifiers: []data.SelectedModifier{{OptionName: "Oat"}}},
		{SKU: "B", Name: "Carrot Cake", Qty: 1},
	}}
	ticket, markSent := counterOrderTicketFromSnapshot(&snap)
	if len(ticket) != len(snap.Lines) {
		t.Fatalf("ticket has %d lines, snapshot %d", len(ticket), len(snap.Lines))
	}
	for i, l := range ticket {
		s := snap.Lines[i]
		if l.Name != s.Name || l.Qty != s.Qty || len(l.Modifiers) != len(s.Modifiers) {
			t.Fatalf("ticket line %d = %+v, not built from snapshot line %+v", i, l, s)
		}
	}
	if ticket[0].Modifiers[0] != "Oat" {
		t.Fatalf("modifiers not carried: %+v", ticket[0])
	}
	for _, s := range snap.Lines {
		if s.KitchenSentQty != 0 {
			t.Fatal("nothing is marked before the ticket printed")
		}
	}
	markSent()
	for i, s := range snap.Lines {
		if s.KitchenSentQty != ticket[i].Qty {
			t.Fatalf("line %d marked %v sent, ticket printed %v", i, s.KitchenSentQty, ticket[i].Qty)
		}
	}
}

// Round-2 review (LOW): the kiosk card checkout builds its sale lines from
// the slice the handler read, never from a second engine read -- so the
// kitchen filter handed to completeTender describes the same lines.
func TestKioskSaleLinesAndTotal_UsesGivenLines(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	eng := pos.NewServiceWithResolver(pos.Config{}, nil) // empty engine: any line must come from the slice
	lines := []pos.BasketLine{{SKU: "A", Name: "Flat White", Qty: 1, PriceCents: money.FromMinor(300)}}
	saleLines, _, blocked := kioskSaleLinesAndTotal(dp, eng, lines, "loc")
	if blocked || len(saleLines) != 1 || saleLines[0].SKU != "A" {
		t.Fatalf("sale lines = %+v blocked=%v, want the given line", saleLines, blocked)
	}
}
