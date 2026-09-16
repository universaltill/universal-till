package pages

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#1181 / ADR-0073 — page-layer wiring for per-line order type.
//
// ut-docs#2282/#2309 (product-owner scope change, outcome 1 taken): the
// cashier-facing per-line control and its POST /api/pos/line-order-type
// HTTP endpoint are REMOVED (order type is sale-level only now) — the
// handler-level tests that drove that endpoint directly
// (TestLineOrderTypeHandler_FlipsOneLineByKey,
// TestLineOrderTypeHandler_SplitsOneUnitOfMultiQtyLine,
// TestTender_PersistsPerLineOrderTypes,
// TestTender_DefaultTakeawayButLineDineIn_PersistsDineIn,
// TestLineOrderType_ClaimReleasedWhenBasketTurnsAllTakeaway) went with it.
// Everything below this point exercises the DATA MODEL the issue comment
// says to keep (sale_lines.order_type, journal replay, kitchen tickets,
// receipts, sale.completed) — still reachable via pos.Service.SetLineOrderType
// directly (internal/pos/order_type_line_test.go) for a resumed held sale or
// a synced legacy peer, just no longer from a cashier-facing HTTP control.

// Review (ut-docs#2282): the removal above deleted tests, it never added one
// that PINS the removal — a re-added handler (or a revert of that hunk)
// would have gone unnoticed by the Go suite, since the e2e spec only checks
// that no markup renders a control, not that the route is gone. This is the
// endpoint half of that contract: POST /api/pos/line-order-type must 404
// (nothing registers it any more), while /api/pos/order-type — the
// sale-level endpoint the basket-top toggle AND the new #2282 intercept
// modal both post to — must still be registered and working. Kept as one
// test so a future "let's put the per-line control back" lands on a single,
// self-explaining failure.
func TestLineOrderTypeEndpointIsGone_SaleLevelEndpointRemains(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	posPostForm(mux, "/api/pos/scan", "code=PLAIN")

	rec := posPostForm(mux, "/api/pos/line-order-type",
		"key="+dp.Engine.Basket().Lines[0].LineKey+"&order_type=takeaway")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/pos/line-order-type = %d, want 404: the per-line "+
			"cashier control was withdrawn by ut-docs#2282/#2309 (order type is "+
			"sale-level only) — re-registering this route needs a product-owner "+
			"decision and a superseding ADR, not just a handler", rec.Code)
	}
	// ...and the line it names is untouched by that rejected request.
	if got := dp.Engine.Basket().Lines[0].OrderType; got != "" {
		t.Fatalf("line order type = %q after a 404'd request, want unchanged", got)
	}

	// The sale-level endpoint is still there and still flips every line.
	if rec = posPostForm(mux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("POST /api/pos/order-type = %d, want 200", rec.Code)
	}
	if got := dp.Engine.Basket().Lines[0].OrderType; got != pos.OrderTypeTakeaway {
		t.Fatalf("line order type after the sale-level toggle = %q, want takeaway", got)
	}
}

// LAN replay: per-line values ride the journal and are persisted verbatim;
// a legacy header-only takeaway payload restores takeaway lines; a bad line
// value is normalized, never rejected (ADR-0065 poison-entry rule).
func TestApplyJournal_PerLineOrderTypeAndLegacyHeader(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	j := seedJournalSale("remote-mixed", "T2-MIX", "sale", "", "itm1", 1, 100)
	j.Sale.OrderType = pos.OrderTypeMixed
	j.Sale.Lines = []data.SaleDetailLine{
		{Name: "Apple", SKU: "ABC", ItemID: "itm1", UnitPrice: 100, Qty: 1, LineTotal: 100, OrderType: ""},
		{Name: "Apple", SKU: "ABC", ItemID: "itm1", UnitPrice: 100, Qty: 1, LineTotal: 100, OrderType: pos.OrderTypeTakeaway},
	}
	j.Sale.Subtotal, j.Sale.Total = 200, 200
	j.Sale.Payments = []data.SaleDetailPayment{{Method: "cash", Amount: 200}}
	if applied, _, err := applyJournal(ctx, dp, "till-1", j); err != nil || !applied {
		t.Fatalf("mixed replay: applied=%v err=%v", applied, err)
	}
	var header string
	_ = dp.Db.QueryRowContext(ctx, `SELECT order_type FROM sales WHERE id='remote-mixed'`).Scan(&header)
	if header != pos.OrderTypeMixed {
		t.Fatalf("replayed header = %q, want mixed", header)
	}
	var lineModes []string
	rows, _ := dp.Db.QueryContext(ctx, `SELECT order_type FROM sale_lines WHERE sale_id='remote-mixed' ORDER BY line_no`)
	for rows.Next() {
		var ot string
		_ = rows.Scan(&ot)
		lineModes = append(lineModes, ot)
	}
	rows.Close()
	if len(lineModes) != 2 || lineModes[0] != "" || lineModes[1] != pos.OrderTypeTakeaway {
		t.Fatalf("replayed line modes = %v", lineModes)
	}

	// Legacy: header takeaway, lines carry no value.
	legacy := seedJournalSale("remote-legacy", "T2-LEG", "sale", "", "itm1", 1, 100)
	legacy.Sale.OrderType = pos.OrderTypeTakeaway
	if applied, _, err := applyJournal(ctx, dp, "till-1", legacy); err != nil || !applied {
		t.Fatalf("legacy replay: applied=%v err=%v", applied, err)
	}
	var legacyLine string
	_ = dp.Db.QueryRowContext(ctx, `SELECT order_type FROM sale_lines WHERE sale_id='remote-legacy'`).Scan(&legacyLine)
	if legacyLine != pos.OrderTypeTakeaway {
		t.Fatalf("legacy line mode = %q, want takeaway inherited from header", legacyLine)
	}

	// Bad value on a line: normalized to dine-in, applied, not 422.
	bad := seedJournalSale("remote-bad", "T2-BAD", "sale", "", "itm1", 1, 100)
	bad.Sale.Lines[0].OrderType = "mixed"
	if applied, _, err := applyJournal(ctx, dp, "till-1", bad); err != nil || !applied {
		t.Fatalf("bad-value replay must apply (normalized), got applied=%v err=%v", applied, err)
	}
	var badLine string
	_ = dp.Db.QueryRowContext(ctx, `SELECT order_type FROM sale_lines WHERE sale_id='remote-bad'`).Scan(&badLine)
	if badLine != "" {
		t.Fatalf("bad line value normalized to %q, want dine-in", badLine)
	}

	// Wire: a mixed sale's journal carries the line key.
	raw, _ := json.Marshal(j)
	if !strings.Contains(string(raw), `"order_type":"takeaway"`) {
		t.Fatalf("expected per-line order_type on the wire, got %s", raw)
	}
}

// Kitchen ticket: a mixed sale prints a translated MIXED header and a mode
// marker per line; a uniform sale's ticket is unchanged.
func TestBuildKitchenTicket_MixedSaleMarksLines(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	seedKitchenSale(t, dbase, "R-MIX", "itm-steak", "itm-cola")
	if _, err := dbase.DB.Exec(`UPDATE sales SET order_type='mixed' WHERE receipt_no='R-MIX'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbase.DB.Exec(`UPDATE sale_lines SET order_type='takeaway' WHERE sale_id='sale-R-MIX' AND line_no=2`); err != nil {
		t.Fatal(err)
	}
	ticket, err := buildKitchenTicket(context.Background(), dp, "R-MIX")
	if err != nil {
		t.Fatal(err)
	}
	if ticket.OrderType != "Mixed" {
		t.Fatalf("ticket.OrderType = %q, want Mixed", ticket.OrderType)
	}
	if len(ticket.Items) != 2 {
		t.Fatalf("items = %d", len(ticket.Items))
	}
	if ticket.Items[0].Mode != "Dine in" || ticket.Items[1].Mode != "Takeaway" {
		t.Fatalf("item modes = %q/%q, want Dine in/Takeaway", ticket.Items[0].Mode, ticket.Items[1].Mode)
	}
	// Uniform sale: no per-line marker at all.
	seedKitchenSale(t, dbase, "R-UNI", "itm-steak")
	uni, err := buildKitchenTicket(context.Background(), dp, "R-UNI")
	if err != nil {
		t.Fatal(err)
	}
	if uni.Items[0].Mode != "" {
		t.Fatalf("uniform sale line mode = %q, want empty", uni.Items[0].Mode)
	}
}

// sale.completed carries the derived header and each line's mode.
func TestSaleCompletedEventFor_CarriesOrderTypes(t *testing.T) {
	detail := data.SaleDetail{ID: "s1", ReceiptNo: "R1", SaleType: "sale", OrderType: pos.OrderTypeMixed, Currency: "GBP",
		Lines: []data.SaleDetailLine{
			{SKU: "ABC", ItemID: "itm1", Qty: 1, UnitPrice: 100, OrderType: ""},
			{SKU: "VAR", VariantID: "var1", Qty: 1, UnitPrice: 150, OrderType: pos.OrderTypeTakeaway},
		}}
	ev := saleCompletedEventFor(detail)
	if ev.OrderType != pos.OrderTypeMixed {
		t.Fatalf("event order_type = %q, want mixed", ev.OrderType)
	}
	if ev.LineItems[0].OrderType != "" || ev.LineItems[1].OrderType != pos.OrderTypeTakeaway {
		t.Fatalf("event line modes = %q/%q", ev.LineItems[0].OrderType, ev.LineItems[1].OrderType)
	}
	raw, _ := json.Marshal(ev)
	if !strings.Contains(string(raw), `"order_type":"mixed"`) || !strings.Contains(string(raw), `"order_type":"takeaway"`) {
		t.Fatalf("expected snake_case order_type keys on the wire, got %s", raw)
	}
	// Dine-in is omitted (additive, pre-ADR-0073 payload shape for uniform dine-in sales).
	uni := saleCompletedEventFor(data.SaleDetail{ID: "s2", Lines: []data.SaleDetailLine{{SKU: "ABC", Qty: 1}}})
	raw, _ = json.Marshal(uni)
	if strings.Contains(string(raw), `"order_type"`) {
		t.Fatalf("dine-in sale must omit order_type keys, got %s", raw)
	}
}

// Held-table policy follows the LINES: a mixed held sale keeps/moves its
// table; an all-takeaway one does not.
func TestHeldTable_MixedHeldSaleMayMoveTable(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Build a mixed basket with the one seeded SKU: set the default to
	// takeaway, scan (takeaway line), flip that line to dine-in, scan again
	// (default is still takeaway -> a second, distinct takeaway line).
	dp.Engine.SetOrderType(pos.OrderTypeTakeaway)
	_, _ = dp.Engine.Scan("ABC")
	b := dp.Engine.Basket()
	dp.Engine.SetLineOrderType(b.Lines[0].LineKey, "")
	_, _ = dp.Engine.Scan("ABC")
	dp.Engine.SetTable("tbl-1", "T1")
	if dp.Engine.Basket().OrderType != pos.OrderTypeMixed {
		t.Fatalf("seed basket summary = %q, want mixed", dp.Engine.Basket().OrderType)
	}
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	var id, payload string
	if err := dp.Db.QueryRow(`SELECT id, payload FROM held_sales`).Scan(&id, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"order_type":"mixed"`) {
		t.Fatalf("held payload header = %s, want mixed summary", payload)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/held", nil))
	if !strings.Contains(rec.Body.String(), `"table_id":"tbl-2"`) {
		t.Fatalf("mixed held sale must offer a move target, got: %s", rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id=tbl-2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	var tableID string
	_ = dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id=?`, id).Scan(&tableID)
	if tableID != "tbl-2" {
		t.Fatalf("mixed held sale table after move = %q, want tbl-2", tableID)
	}
	// Resume keeps the table and the mixed lines.
	resume := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resume.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), resume)
	rb := dp.Engine.Basket()
	if rb.TableID != "tbl-2" || rb.OrderType != pos.OrderTypeMixed {
		t.Fatalf("resumed table/summary = %q/%q, want tbl-2/mixed", rb.TableID, rb.OrderType)
	}
}

// Receipt: a mixed sale marks lines + a Mixed header; a uniform sale's
// receipt has no marker at all (ADR-0073 Decision 7).
func TestRenderReceipt_MixedSaleMarksLines_UniformUnchanged(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return "x" },
		"qty":        func(v any) string { return "x" },
		"barcodesvg": func(s string) template.HTML { return "" },
		"bpPercent":  func(bp int64) string { return "" },
		"T":          func(key string) string { return key },
	}
	mixed := []pos.SaleLineInput{
		{Name: "Coffee", Qty: 1, UnitPrice: 100, OrderType: ""},
		{Name: "Coffee", Qty: 1, UnitPrice: 100, OrderType: pos.OrderTypeTakeaway},
	}
	html, err := renderReceipt(funcs, "1", mixed, nil, 200, 0, 200, false, 0, "", 0, nil, false, false, false, false, nil, nil, "S", receiptDesign{}, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-testid="receipt-mixed"`) || !strings.Contains(html, "basket.order_type.takeaway") || !strings.Contains(html, "basket.order_type.dine_in") {
		t.Fatalf("mixed receipt lacks markers: %s", html)
	}
	uniform := []pos.SaleLineInput{{Name: "Coffee", Qty: 1, UnitPrice: 100, OrderType: pos.OrderTypeTakeaway}}
	html, err = renderReceipt(funcs, "2", uniform, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "S", receiptDesign{}, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "receipt-mixed") || strings.Contains(html, "receipt-line-mode") {
		t.Fatalf("uniform receipt must carry no mode markers: %s", html)
	}
}

// Second-round review BLOCKER-1 (ut-docs#1181): a pre-ADR-0073 peer's
// journaled RETURN has header "" and untyped lines. Its lines must inherit
// the original sale's persisted modes on replay, or the per-mode refund
// pool never sees the returned unit and the primary allows a second refund.
func TestApplyJournal_LegacyReturnInheritsOriginalLineModes(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	sale := seedJournalSale("remote-ta-sale", "T2-TA-1", "sale", "", "itm1", 1, 100)
	sale.Sale.OrderType = pos.OrderTypeTakeaway // old-build header-only takeaway
	if applied, _, err := applyJournal(ctx, dp, "till-1", sale); err != nil || !applied {
		t.Fatalf("sale replay: %v %v", applied, err)
	}
	ret := seedJournalSale("remote-ta-ret", "T2-TA-2", "return", "remote-ta-sale", "itm1", 1, 100)
	ret.Sale.OrderType = "" // old-build return: no header, no line modes
	if applied, _, err := applyJournal(ctx, dp, "till-1", ret); err != nil || !applied {
		t.Fatalf("return replay: %v %v", applied, err)
	}
	var retLine, retHeader string
	_ = dp.Db.QueryRowContext(ctx, `SELECT order_type FROM sale_lines WHERE sale_id='remote-ta-ret'`).Scan(&retLine)
	_ = dp.Db.QueryRowContext(ctx, `SELECT order_type FROM sales WHERE id='remote-ta-ret'`).Scan(&retHeader)
	if retLine != pos.OrderTypeTakeaway || retHeader != pos.OrderTypeTakeaway {
		t.Fatalf("legacy return replay: line=%q header=%q, want takeaway/takeaway (inherited from original)", retLine, retHeader)
	}
	returned, err := data.NewPOSRepo(dp.Db).ReturnedQuantities(ctx, "remote-ta-sale")
	if err != nil {
		t.Fatal(err)
	}
	if returned[data.RefundLineKey("itm1", "", 100, pos.OrderTypeTakeaway)] != 1 {
		t.Fatalf("returned pool = %v, want the unit under the takeaway key", returned)
	}
}
