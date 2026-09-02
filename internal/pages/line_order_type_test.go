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

// POST /api/pos/line-order-type flips ONE line by key, leaves the rest and
// the default alone, and renders the basket with the mixed summary.
func TestLineOrderTypeHandler_FlipsOneLineByKey(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	posPostForm(mux, "/api/pos/scan", "code=ABC")
	posPostForm(mux, "/api/pos/scan", "code=VAR")
	b := dp.Engine.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("seed lines = %d", len(b.Lines))
	}
	rec := posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[0].LineKey+"&order_type=takeaway")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b = dp.Engine.Basket()
	if b.Lines[0].OrderType != pos.OrderTypeTakeaway || b.Lines[1].OrderType != "" {
		t.Fatalf("line modes = %q/%q, want takeaway/dine-in", b.Lines[0].OrderType, b.Lines[1].OrderType)
	}
	if b.OrderType != pos.OrderTypeMixed {
		t.Fatalf("summary = %q, want mixed", b.OrderType)
	}
	if dp.Engine.OrderType() != "" {
		t.Fatalf("default changed to %q by a per-line edit", dp.Engine.OrderType())
	}
	body := rec.Body.String()
	// Per-line control renders on every line, keyed by LineKey, and the
	// basket shows the mixed status + both bulk actions.
	if !strings.Contains(body, `data-testid="line-order-type-`) {
		t.Fatalf("expected per-line order-type controls in the basket, got: %s", body)
	}
	if !strings.Contains(body, `hx-post="/api/pos/line-order-type"`) {
		t.Fatalf("expected the per-line control to post to /api/pos/line-order-type, got: %s", body)
	}
	if !strings.Contains(body, `data-testid="order-type-mixed"`) {
		t.Fatalf("expected the mixed status marker, got: %s", body)
	}
	if !strings.Contains(body, `hx-sync="#basket:replace"`) {
		t.Fatalf("expected hx-sync race guard, got: %s", body)
	}
	// Unknown value is a 400, not a silent clamp (ADR-0073 D2 "rejects an
	// unknown value at the HTTP boundary").
	rec = posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[0].LineKey+"&order_type=mixed")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mixed as a line value: expected 400, got %d", rec.Code)
	}
	// Unknown key: basket re-rendered unchanged, 200 (a stale page's tap
	// after a remove must not error the whole basket).
	rec = posPostForm(mux, "/api/pos/line-order-type", "key=nope&order_type=takeaway")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown key: expected 200 re-render, got %d", rec.Code)
	}
}

// The whole-basket control still converts every line (bulk) and the
// tender persists each line's own mode + derived header.
func TestTender_PersistsPerLineOrderTypes(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	posPostForm(mux, "/api/pos/scan", "code=ABC")
	posPostForm(mux, "/api/pos/scan", "code=VAR")
	b := dp.Engine.Basket()
	posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[1].LineKey+"&order_type=takeaway")

	body := `{"payments":[{"method_id":"cash","amount":100000}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var header string
	if err := dp.Db.QueryRow(`SELECT order_type FROM sales WHERE status='completed'`).Scan(&header); err != nil {
		t.Fatal(err)
	}
	if header != pos.OrderTypeMixed {
		t.Fatalf("persisted header = %q, want mixed", header)
	}
	rows, err := dp.Db.Query(`SELECT sku_snapshot, order_type FROM sale_lines ORDER BY line_no`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var sku, ot string
		_ = rows.Scan(&sku, &ot)
		got[sku] = ot
	}
	if got["ABC"] != "" || got["VAR"] != pos.OrderTypeTakeaway {
		t.Fatalf("persisted line modes = %v", got)
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
		"barcodesvg": func(s string) template.HTML { return "" },
		"bpPercent":  func(bp int64) string { return "" },
		"T":          func(key string) string { return key },
	}
	mixed := []pos.SaleLineInput{
		{Name: "Coffee", Qty: 1, UnitPrice: 100, OrderType: ""},
		{Name: "Coffee", Qty: 1, UnitPrice: 100, OrderType: pos.OrderTypeTakeaway},
	}
	html, err := renderReceipt(funcs, "1", mixed, nil, 200, 0, 200, false, 0, "", 0, nil, false, false, false, false, nil, "S", receiptDesign{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-testid="receipt-mixed"`) || !strings.Contains(html, "basket.order_type.takeaway") || !strings.Contains(html, "basket.order_type.dine_in") {
		t.Fatalf("mixed receipt lacks markers: %s", html)
	}
	uniform := []pos.SaleLineInput{{Name: "Coffee", Qty: 1, UnitPrice: 100, OrderType: pos.OrderTypeTakeaway}}
	html, err = renderReceipt(funcs, "2", uniform, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "S", receiptDesign{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "receipt-mixed") || strings.Contains(html, "receipt-line-mode") {
		t.Fatalf("uniform receipt must carry no mode markers: %s", html)
	}
}

// Review B1 (ut-docs#1181): the live tender must persist the basket's
// DERIVED summary, never the default-for-new-lines. Otherwise "bulk
// Takeaway → scan → flip the line back to dine-in" tenders a dine-in-taxed
// line under a takeaway header, which CompleteSale's legacy rule would then
// rewrite to takeaway.
func TestTender_DefaultTakeawayButLineDineIn_PersistsDineIn(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	posPostForm(mux, "/api/pos/order-type", "order_type=takeaway")
	posPostForm(mux, "/api/pos/scan", "code=ABC")
	b := dp.Engine.Basket()
	posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[0].LineKey+"&order_type=")
	if dp.Engine.OrderType() != pos.OrderTypeTakeaway || dp.Engine.Basket().OrderType != "" {
		t.Fatalf("seed: default=%q summary=%q", dp.Engine.OrderType(), dp.Engine.Basket().OrderType)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method_id":"cash","amount":100000}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var header, line string
	_ = dp.Db.QueryRow(`SELECT order_type FROM sales WHERE status='completed'`).Scan(&header)
	_ = dp.Db.QueryRow(`SELECT order_type FROM sale_lines`).Scan(&line)
	if header != "" || line != "" {
		t.Fatalf("persisted header=%q line=%q, want dine-in/dine-in", header, line)
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

// ut-docs#1390 × ADR-0073 D5: when a per-line flip or a void turns the
// basket all-takeaway, the engine clears the table AND the persisted
// table_claims row must be released, or the table reads occupied with
// nothing on it.
func TestLineOrderType_ClaimReleasedWhenBasketTurnsAllTakeaway(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	posPostForm(mux, "/api/pos/scan", "code=ABC")
	posPostForm(mux, "/api/pos/scan", "code=VAR")
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign: %d", rec.Code)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatal("T1 should be claimed")
	}
	b := dp.Engine.Basket()
	posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[0].LineKey+"&order_type=takeaway")
	if !tableOccupied(t, dp, t1) {
		t.Fatal("mixed basket must keep its claim")
	}
	posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[1].LineKey+"&order_type=takeaway")
	if dp.Engine.TableID() != "" || tableOccupied(t, dp, t1) {
		t.Fatalf("all-takeaway via per-line flip: table=%q occupied=%v, want cleared+released", dp.Engine.TableID(), tableOccupied(t, dp, t1))
	}
	// Void path: dine-in + takeaway, table, void the dine-in line.
	posPostForm(mux, "/api/pos/reset", "")
	posPostForm(mux, "/api/pos/scan", "code=ABC")
	posPostForm(mux, "/api/pos/scan", "code=VAR")
	b = dp.Engine.Basket()
	posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[1].LineKey+"&order_type=takeaway")
	posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if !tableOccupied(t, dp, t1) {
		t.Fatal("T1 should be claimed (mixed)")
	}
	posPostForm(mux, "/api/pos/remove", "key="+b.Lines[0].LineKey)
	if dp.Engine.TableID() != "" || tableOccupied(t, dp, t1) {
		t.Fatalf("all-takeaway via void: table=%q occupied=%v, want cleared+released", dp.Engine.TableID(), tableOccupied(t, dp, t1))
	}
}

// Product owner 2026-09-02: "2 americano, one takeaway and the other one
// dine in" — through the real handler, one tap on the per-line icon of a
// qty-2 line yields two lines, and the tender persists them separately
// with each line's own tax rate.
func TestLineOrderTypeHandler_SplitsOneUnitOfMultiQtyLine(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	posPostForm(mux, "/api/pos/scan", "code=ABC&qty=2")
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("seed: %+v", b.Lines)
	}
	rec := posPostForm(mux, "/api/pos/line-order-type", "key="+b.Lines[0].LineKey+"&order_type=takeaway")
	if rec.Code != http.StatusOK {
		t.Fatalf("flip: %d %s", rec.Code, rec.Body.String())
	}
	b = dp.Engine.Basket()
	if len(b.Lines) != 2 || b.Lines[0].Qty != 1 || b.Lines[1].Qty != 1 || b.Lines[0].OrderType != "" || b.Lines[1].OrderType != pos.OrderTypeTakeaway {
		t.Fatalf("after one tap: %+v", b.Lines)
	}
	if strings.Count(rec.Body.String(), `data-testid="line-order-type-`) < 4 {
		t.Fatalf("expected two rendered lines each with a control, got: %s", rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method_id":"cash","amount":100000}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&n)
	var header string
	_ = dp.Db.QueryRow(`SELECT order_type FROM sales WHERE status='completed'`).Scan(&header)
	if n != 2 || header != pos.OrderTypeMixed {
		t.Fatalf("persisted lines=%d header=%q, want 2/mixed", n, header)
	}
}
