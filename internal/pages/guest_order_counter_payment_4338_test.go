package pages

// ut-docs#4338 review: the LAN table-QR checkout these end-to-end tests used
// to drive (/self-order?table=) is gone, but the cloud guest_order directive
// (ut-docs#4337) parks the same kind of held table order with the kitchen
// ticket already sent. These are the three table-order cases of
// self_order_counter_held_test.go (F1 of the ut-docs#2703 review), driven
// through cloudGuestOrder instead: payment never reprints what the kitchen
// has, items added at the till print at payment, and a ticket that failed
// on arrival prints in full at payment.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// guestTableOrder places one Carrot Cake at a new table T5 through the cloud
// guest_order hook and waits for its asynchronous kitchen ticket.
func (h *counterHeldHarness) guestTableOrder() string {
	h.t.Helper()
	ctx := context.Background()
	tableID, err := data.NewPOSRepo(h.db.DB).CreateTable(ctx, "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	tok, err := data.NewPOSRepo(h.db.DB).EnsureTableGuestToken(ctx, tableID)
	if err != nil || tok == "" {
		h.t.Fatalf("guest token: %q, %v", tok, err)
	}
	out, err := cloudGuestOrder(ctx, h.dp, cloudsync.GuestOrder{
		ID: "g1", DirectiveID: "dir-g1", TableToken: tok,
		Lines: []cloudsync.GuestOrderLine{{ItemID: "itm-cake", Qty: 1}},
	})
	if err != nil || out.Refusal != "" {
		h.t.Fatalf("cloudGuestOrder: %+v, %v", out, err)
	}
	h.dp.WaitForAsyncWork()
	return tableID
}

func TestGuestTableOrder_PrintsOnArrivalNeverAgainAtPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	tableID := h.guestTableOrder()
	if n := len(printer.Settle(1)); n != 1 {
		t.Fatalf("a guest table order goes to the kitchen on arrival: want 1 ticket, got %d", n)
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
	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != 200 {
		t.Fatalf("resume = %d", rec.Code)
	}
	if h.dp.Engine.TableID() != tableID {
		t.Fatalf("resumed basket table = %q, want %q", h.dp.Engine.TableID(), tableID)
	}
	// Park and resume again: the per-line record must survive a re-park.
	if rec := h.post("/api/pos/hold", ""); rec.Code != 200 {
		t.Fatalf("re-park = %d", rec.Code)
	}
	h.post("/api/pos/resume", "id="+h.onlyHeld().ID)
	if rec := h.tenderCash(); rec.Code != 200 {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	if n := len(printer.Settle(1)); n != 1 {
		t.Fatalf("paying a guest table order must not reprint its kitchen ticket: got %d tickets in total", n)
	}
}

func TestGuestTableOrder_ItemsAddedAtTillPrintAtPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	seedShopItem(t, h.db, "itm-burger", "BURGER", "5000003", "Burger", 1200)
	seedStock(t, h.db, "itm-burger", 10)
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	h.guestTableOrder()
	if n := len(printer.Settle(1)); n != 1 {
		t.Fatalf("the guest table order prints on arrival: want 1 ticket, got %d", n)
	}
	if rec := h.post("/api/pos/resume", "id="+h.onlyHeld().ID); rec.Code != 200 {
		t.Fatalf("resume = %d", rec.Code)
	}
	if rec := h.post("/api/pos/scan", "code=5000003&qty=2"); rec.Code != 200 {
		t.Fatalf("scan burgers = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := h.post("/api/pos/hold", ""); rec.Code != 200 {
		t.Fatalf("re-park = %d", rec.Code)
	}
	h.post("/api/pos/resume", "id="+h.onlyHeld().ID)
	if rec := h.tenderCash(); rec.Code != 200 {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	tickets := printer.Settle(2)
	if len(tickets) != 2 {
		t.Fatalf("want the arrival ticket plus ONE ticket at payment, got %d", len(tickets))
	}
	paid := tickets[1]
	if !strings.Contains(paid, "2 x Burger") {
		t.Fatalf("the payment ticket must carry the two mains added at the till: %q", paid)
	}
	if strings.Contains(paid, "Carrot Cake") {
		t.Fatalf("the payment ticket must not repeat what the kitchen already has: %q", paid)
	}
}

func TestGuestTableOrder_PrinterDownOnArrivalPrintsAtPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, unreachableAddr()); err != nil {
		t.Fatal(err)
	}
	h.guestTableOrder()
	held := h.onlyHeld()
	var snap pos.BasketSnapshot
	_ = json.Unmarshal([]byte(held.Payload), &snap)
	for _, l := range snap.Lines {
		if l.KitchenSentQty != 0 {
			t.Fatalf("a failed arrival print must not mark the line sent: %+v", l)
		}
	}
	printer := newFakeKitchenPrinter(t)
	if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
		t.Fatal(err)
	}
	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != 200 {
		t.Fatalf("resume = %d", rec.Code)
	}
	if rec := h.tenderCash(); rec.Code != 200 {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()
	tickets := printer.Settle(1)
	if len(tickets) != 1 || !strings.Contains(tickets[0], "Carrot Cake") {
		t.Fatalf("the whole order must print at payment after a failed arrival print, got %q", tickets)
	}
}
