package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ADR-0169 §9, ut-docs#4768: the main till records a channel order and lowers
// stock with a `channel_sale` movement. It is not a till sale.

func openChannelOrders(t *testing.T) (*sql.DB, *ChannelOrdersRepo) {
	t.Helper()
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "channel_orders.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	testsupport.SeedTaxCode(t, dbo.DB, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, dbo.DB, testsupport.ItemSeed{ID: "itm1", SKU: "LS-M-BLU", Name: "Shirt", BasePrice: 3500, TaxCodeID: "tc1", IsActive: true})
	testsupport.SeedItem(t, dbo.DB, testsupport.ItemSeed{ID: "itm2", SKU: "MUG-1", Name: "Mug", BasePrice: 800, TaxCodeID: "tc1", IsActive: true})
	mustExecCO(t, dbo.DB, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv1','itm1','loc_main',10)`)
	mustExecCO(t, dbo.DB, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv2','itm2','loc_main',1)`)
	return dbo.DB, NewChannelOrdersRepo(dbo.DB)
}

func mustExecCO(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func scalarCO(t *testing.T, d *sql.DB, q string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := d.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func sampleChannelOrder() ChannelOrderInput {
	return ChannelOrderInput{
		DirectiveID: "dir-1", ConnectionID: "conn-1", PluginKey: "com.universaltill.channel-demo",
		ExternalOrderID: "o-9001", ExternalVersion: "7", Revision: 1, OrderNumber: "#1001",
		PlacedAt: "2026-10-22T10:12:00Z", Currency: "GBP", Status: "new",
		Shipping: money.FromMinor(399), Total: money.FromMinor(7399), CustomerCountry: "GB", LocationID: "loc_main",
		Lines: []ChannelOrderLineInput{
			{LineID: "l-1", CloudItemID: "itm1", SKU: "LS-M-BLU", Name: "Shirt", Qty: 2, UnitPrice: money.FromMinor(3500), TaxBP: 2000},
		},
	}
}

func TestChannelOrders_ApplyRecordsOrderAndChannelSaleMovement(t *testing.T) {
	d, repo := openChannelOrders(t)
	res, err := repo.Apply(context.Background(), sampleChannelOrder())
	if err != nil {
		t.Fatal(err)
	}
	if res.OrderID == "" || res.Duplicate || len(res.Unmatched) != 0 || len(res.NegativeStock) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements WHERE type='channel_sale' AND item_id='itm1' AND quantity=-2`); n != 1 {
		t.Fatalf("channel_sale movements = %v, want 1", n)
	}
	got, ok, err := repo.Get(context.Background(), res.OrderID)
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if got.OrderNumber != "#1001" || got.Revision != 1 || got.Total != money.FromMinor(7399) || got.Shipping != money.FromMinor(399) || got.CustomerCountry != "GB" || len(got.Lines) != 1 || got.Lines[0].Unmatched {
		t.Fatalf("order = %+v", got)
	}
}

func TestChannelOrders_SameDirectiveTwiceRecordsOnce(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	first, err := repo.Apply(ctx, sampleChannelOrder())
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Apply(ctx, sampleChannelOrder())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.OrderID != first.OrderID {
		t.Fatalf("second = %+v, want duplicate of %s", second, first.OrderID)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_orders`); n != 1 {
		t.Fatalf("orders = %v", n)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements WHERE type='channel_sale'`); n != 1 {
		t.Fatalf("movements = %v", n)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
}

func TestChannelOrders_SameRevisionUnderNewDirectiveIsNoOp(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	first, _ := repo.Apply(ctx, sampleChannelOrder())
	again := sampleChannelOrder()
	again.DirectiveID = "dir-2" // a new directive id, same revision: a redelivery
	res, err := repo.Apply(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Duplicate || res.OrderID != first.OrderID {
		t.Fatalf("res = %+v", res)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements WHERE type='channel_sale'`); n != 1 {
		t.Fatalf("movements = %v", n)
	}
}

func TestChannelOrders_UnknownSKUStoredUnmatchedNoMovement(t *testing.T) {
	d, repo := openChannelOrders(t)
	in := sampleChannelOrder()
	in.Lines = append(in.Lines, ChannelOrderLineInput{LineID: "l-2", CloudItemID: "nope", SKU: "NO-SUCH", Qty: 1, UnitPrice: money.FromMinor(100)})
	res, err := repo.Apply(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unmatched) != 1 || res.Unmatched[0] != "l-2" {
		t.Fatalf("unmatched = %v", res.Unmatched)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements WHERE type='channel_sale'`); n != 1 {
		t.Fatalf("movements = %v, want only the matched line's", n)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_order_lines WHERE unmatched=1 AND line_id='l-2'`); n != 1 {
		t.Fatalf("unmatched rows = %v", n)
	}
}

func TestChannelOrders_MatchBySKUWhenIDUnknown(t *testing.T) {
	d, repo := openChannelOrders(t)
	in := sampleChannelOrder()
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "cloud-only-id", SKU: "MUG-1", Qty: 1, UnitPrice: money.FromMinor(800)}}
	res, err := repo.Apply(context.Background(), in)
	if err != nil || len(res.Unmatched) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv2'`); q != 0 {
		t.Fatalf("stock = %v, want 0", q)
	}
}

func TestChannelOrders_NegativeStockIsAppliedAndReported(t *testing.T) {
	d, repo := openChannelOrders(t)
	in := sampleChannelOrder()
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "itm2", SKU: "MUG-1", Qty: 3, UnitPrice: money.FromMinor(800)}}
	res, err := repo.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("negative stock must be applied, not refused: %v", err)
	}
	if len(res.NegativeStock) != 1 || res.NegativeStock[0].LineID != "l-1" || res.NegativeStock[0].OnHand != -2 {
		t.Fatalf("negative = %+v", res.NegativeStock)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv2'`); q != -2 {
		t.Fatalf("stock = %v, want -2", q)
	}
}

func TestChannelOrders_SoldFromTheLocationHoldingTheStock(t *testing.T) {
	d, repo := openChannelOrders(t)
	mustExecCO(t, d, `INSERT INTO stock_locations (id, name, is_active) VALUES ('loc_b','Back',1)`)
	mustExecCO(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv1b','itm1','loc_b',50)`)
	in := sampleChannelOrder()
	in.LocationID = "loc_main"
	if _, err := repo.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1b'`); q != 48 {
		t.Fatalf("loc_b stock = %v, want 48", q)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("loc_main stock = %v, want untouched", q)
	}
}

func TestChannelOrders_UntrackedItemMatchedWithoutMovement(t *testing.T) {
	d, repo := openChannelOrders(t)
	mustExecCO(t, d, `UPDATE items SET stock_untracked=1 WHERE id='itm2'`)
	in := sampleChannelOrder()
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "itm2", SKU: "MUG-1", Qty: 1, UnitPrice: money.FromMinor(800)}}
	res, err := repo.Apply(context.Background(), in)
	if err != nil || len(res.Unmatched) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements`); n != 0 {
		t.Fatalf("movements = %v, want 0 for an untracked item", n)
	}
}

func TestChannelOrders_VariantLineMovesVariantStock(t *testing.T) {
	d, repo := openChannelOrders(t)
	mustExecCO(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('var1','itm1','LS-M-BLU-V','Blue M',3500)`)
	mustExecCO(t, d, `INSERT INTO inventory (id, variant_id, location_id, quantity) VALUES ('invv','var1','loc_main',5)`)
	in := sampleChannelOrder()
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "itm1", CloudVariantID: "var1", SKU: "LS-M-BLU-V", Qty: 2, UnitPrice: money.FromMinor(3500)}}
	res, err := repo.Apply(context.Background(), in)
	if err != nil || len(res.Unmatched) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='invv'`); q != 3 {
		t.Fatalf("variant stock = %v, want 3", q)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("item stock = %v, want untouched 10", q)
	}
}

func TestChannelOrders_ItemWithVariantsButNoVariantIsUnmatched(t *testing.T) {
	d, repo := openChannelOrders(t)
	mustExecCO(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('var1','itm1','LS-M-BLU-V','Blue M',3500)`)
	res, err := repo.Apply(context.Background(), sampleChannelOrder())
	if err != nil || len(res.Unmatched) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestChannelOrders_AppliesAllOrNothing(t *testing.T) {
	d, repo := openChannelOrders(t)
	mustExecCO(t, d, `DELETE FROM inventory WHERE id='inv2'`)
	in := sampleChannelOrder()
	in.Lines = append(in.Lines, ChannelOrderLineInput{LineID: "l-2", CloudItemID: "itm2", SKU: "MUG-1", Name: "Mug", Qty: 1, UnitPrice: money.FromMinor(800)})
	in.LocationID = "no-such-location" // the second line has no stock row, so lands here: FK failure after the first line moved stock
	if _, err := repo.Apply(context.Background(), in); err == nil {
		t.Fatal("want an error")
	}
	for _, tbl := range []string{"channel_orders", "channel_order_lines", "stock_movements"} {
		if n := scalarCO(t, d, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Fatalf("%s rows = %v after a failed apply, want 0", tbl, n)
		}
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock = %v, want untouched", q)
	}
}

func TestChannelOrders_ValidationRefusals(t *testing.T) {
	_, repo := openChannelOrders(t)
	ctx := context.Background()
	for name, mut := range map[string]func(*ChannelOrderInput){
		"no directive":   func(i *ChannelOrderInput) { i.DirectiveID = "" },
		"no revision":    func(i *ChannelOrderInput) { i.Revision = 0 },
		"no connection":  func(i *ChannelOrderInput) { i.ConnectionID = "" },
		"no external id": func(i *ChannelOrderInput) { i.ExternalOrderID = "" },
		"no lines":       func(i *ChannelOrderInput) { i.Lines = nil },
		"zero qty":       func(i *ChannelOrderInput) { i.Lines[0].Qty = 0 },
		"negative qty":   func(i *ChannelOrderInput) { i.Lines[0].Qty = -1 },
		"no location":    func(i *ChannelOrderInput) { i.LocationID = "" },
	} {
		in := sampleChannelOrder()
		mut(&in)
		if _, err := repo.Apply(ctx, in); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestChannelOrders_ListNewestFirstAndSchemaHasNoCustomerColumns(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	a := sampleChannelOrder()
	b := sampleChannelOrder()
	b.DirectiveID, b.ExternalOrderID, b.PlacedAt = "dir-2", "o-9002", "2026-10-23T09:00:00Z"
	b.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "itm1", SKU: "LS-M-BLU", Qty: 1, UnitPrice: money.FromMinor(3500)}}
	_, _ = repo.Apply(ctx, a)
	_, _ = repo.Apply(ctx, b)
	list, err := repo.List(ctx, 50)
	if err != nil || len(list) != 2 || list[0].ExternalOrderID != "o-9002" || list[0].LineCount != 1 || list[1].LineCount != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	rows, err := d.Query(`SELECT name FROM pragma_table_info('channel_orders') UNION SELECT name FROM pragma_table_info('channel_order_lines')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		if c == "name" { // the product title as sold, on a line: not a person's name
			continue
		}
		for _, pii := range []string{"email", "phone", "name", "address", "street", "postcode"} {
			if strings.Contains(strings.ToLower(c), pii) {
				t.Errorf("column %q looks like customer data; none may be stored", c)
			}
		}
	}
}

// Updates (channel_order_update): revision orders, not clocks; stock moves by
// the difference in restocked units.

func TestChannelOrders_HigherRevisionUpdatesStatusAndRestocksTheDifference(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	first, _ := repo.Apply(ctx, sampleChannelOrder())

	up := sampleChannelOrder()
	up.DirectiveID, up.Revision, up.Status = "dir-2", 2, "partially_refunded"
	up.Lines[0].RefundedQuantity, up.Lines[0].RestockedQuantity = 1, 1
	up.RefundTotal = money.FromMinor(3500)
	res, err := repo.Apply(ctx, up)
	if err != nil || res.Duplicate || res.OrderID != first.OrderID {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 9 {
		t.Fatalf("stock = %v, want 9 (8 + 1 restocked)", q)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements WHERE type='channel_return' AND quantity=1`); n != 1 {
		t.Fatalf("return movements = %v", n)
	}

	// Revision 3: 2 of 2 refunded and restocked: only the difference (1) moves.
	up3 := up
	up3.Lines = []ChannelOrderLineInput{up.Lines[0]}
	up3.DirectiveID, up3.Revision, up3.Status = "dir-3", 3, "refunded"
	up3.Lines[0].RefundedQuantity, up3.Lines[0].RestockedQuantity = 2, 2
	if _, err := repo.Apply(ctx, up3); err != nil {
		t.Fatal(err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock = %v, want 10", q)
	}
	got, _, _ := repo.Get(ctx, first.OrderID)
	if got.Status != "refunded" || got.Revision != 3 || got.RefundTotal != money.FromMinor(3500) {
		t.Fatalf("order = %+v", got)
	}
}

func TestChannelOrders_EqualOrLowerRevisionIgnored(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	up := sampleChannelOrder()
	up.DirectiveID, up.Revision, up.Status = "dir-5", 5, "shipped"
	if _, err := repo.Apply(ctx, up); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []int64{5, 4, 1} {
		stale := sampleChannelOrder()
		stale.DirectiveID, stale.Revision, stale.Status = "dir-x", rev, "cancelled"
		stale.Lines[0].RefundedQuantity, stale.Lines[0].RestockedQuantity = 2, 2
		res, err := repo.Apply(ctx, stale)
		if err != nil || !res.Duplicate {
			t.Fatalf("rev %d: res=%+v err=%v", rev, res, err)
		}
	}
	got, _, _ := repo.Get(ctx, mustOrderID(t, d))
	if got.Status != "shipped" || got.Revision != 5 {
		t.Fatalf("order = %+v", got)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
}

func TestChannelOrders_RefundWithNothingRestockedMovesNoStockBack(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	_, _ = repo.Apply(ctx, sampleChannelOrder())
	up := sampleChannelOrder()
	up.DirectiveID, up.Revision, up.Status = "dir-2", 2, "refunded"
	up.Lines[0].RefundedQuantity, up.Lines[0].RestockedQuantity = 2, 0
	if _, err := repo.Apply(ctx, up); err != nil {
		t.Fatal(err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
}

func TestChannelOrders_UpdateForUnseenOrderIsRecordedAsNew(t *testing.T) {
	d, repo := openChannelOrders(t)
	up := sampleChannelOrder()
	up.Revision, up.Status = 4, "accepted" // revisions 1-3 never reached this till
	res, err := repo.Apply(context.Background(), up)
	if err != nil || res.Duplicate {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
}

func TestChannelOrders_UnmatchedLineMatchesLaterAndThenMovesStock(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	in := sampleChannelOrder()
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", SKU: "LATE-1", Name: "Late", Qty: 2, UnitPrice: money.FromMinor(100)}}
	res, _ := repo.Apply(ctx, in)
	if len(res.Unmatched) != 1 {
		t.Fatalf("res = %+v", res)
	}
	testsupport.SeedItem(t, d, testsupport.ItemSeed{ID: "itm3", SKU: "LATE-1", Name: "Late", BasePrice: 100, TaxCodeID: "tc1", IsActive: true})
	mustExecCO(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv3','itm3','loc_main',5)`)
	up := in
	up.Lines = []ChannelOrderLineInput{in.Lines[0]}
	up.DirectiveID, up.Revision = "dir-2", 2
	res, err := repo.Apply(ctx, up)
	if err != nil || len(res.Unmatched) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv3'`); q != 3 {
		t.Fatalf("stock = %v, want 3", q)
	}
	// Redelivery of the same revision does not move it again.
	_, _ = repo.Apply(ctx, up)
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv3'`); q != 3 {
		t.Fatalf("stock = %v after redelivery", q)
	}
}

func mustOrderID(t *testing.T, d *sql.DB) string {
	t.Helper()
	var id string
	if err := d.QueryRow(`SELECT id FROM channel_orders LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChannelOrders_FirstDeliveryAlreadyRefundedLandsRight(t *testing.T) {
	d, repo := openChannelOrders(t)
	in := sampleChannelOrder()
	in.Revision, in.Status = 3, "partially_refunded"
	in.Lines[0].RefundedQuantity, in.Lines[0].RestockedQuantity = 1, 1
	if _, err := repo.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// -qty_new + restocked_new = -2 + 1
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 9 {
		t.Fatalf("stock = %v, want 9", q)
	}
}

func TestChannelOrders_AddedUnitsAndLinesMoveTheDifference(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	if _, err := repo.Apply(ctx, sampleChannelOrder()); err != nil { // itm1 10 -> 8
		t.Fatal(err)
	}
	up := sampleChannelOrder()
	up.Revision = 2
	up.Lines[0].Qty = 3 // one unit added by an edit
	up.Lines = append(up.Lines, ChannelOrderLineInput{LineID: "l-2", CloudItemID: "itm2", SKU: "MUG-1", Name: "Mug", Qty: 1, UnitPrice: money.FromMinor(800)})
	if _, err := repo.Apply(ctx, up); err != nil {
		t.Fatal(err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 7 {
		t.Fatalf("itm1 = %v, want 7", q)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv2'`); q != 0 {
		t.Fatalf("itm2 = %v, want 0", q)
	}
	_, _ = repo.Apply(ctx, up)
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 7 {
		t.Fatalf("itm1 = %v after redelivery", q)
	}
}

func TestNextChannelStatus(t *testing.T) {
	for _, c := range []struct{ from, to, want string }{
		{"new", "accepted", "accepted"},
		{"shipped", "packed", "shipped"}, // never regresses
		{"packed", "shipped", "shipped"},
		{"cancelled", "refunded", "cancelled"}, // terminal
		{"cancelled", "shipped", "cancelled"},
		{"refunded", "cancelled", "refunded"}, // terminal
		{"shipped", "partially_refunded", "partially_refunded"},
		{"partially_refunded", "refunded", "refunded"},
		{"partially_refunded", "cancelled", "cancelled"}, // channel-reported
		{"partially_refunded", "shipped", "shipped"},     // continues forward
		{"partially_refunded", "new", "partially_refunded"},
	} {
		if got, _ := nextChannelStatus(c.from, c.to, 0); got != c.want {
			t.Errorf("%s -> %s = %s, want %s", c.from, c.to, got, c.want)
		}
	}
}

func TestChannelOrders_StatusDoesNotRegressButRefundStockStillApplies(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	c := sampleChannelOrder()
	c.Status = "cancelled"
	first, _ := repo.Apply(ctx, c)
	up := sampleChannelOrder()
	up.Revision, up.Status = 2, "refunded"
	up.Lines[0].RefundedQuantity, up.Lines[0].RestockedQuantity = 2, 2
	if _, err := repo.Apply(ctx, up); err != nil {
		t.Fatal(err)
	}
	got, _, _ := repo.Get(ctx, first.OrderID)
	if got.Status != "cancelled" {
		t.Fatalf("status = %s, want cancelled to stay", got.Status)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock = %v, want 10 (restock applies from the channel)", q)
	}
}

func TestChannelOrders_RestockedAboveRefundedRefused(t *testing.T) {
	_, repo := openChannelOrders(t)
	in := sampleChannelOrder()
	in.Lines[0].RefundedQuantity, in.Lines[0].RestockedQuantity = 1, 2
	if _, err := repo.Apply(context.Background(), in); err == nil {
		t.Fatal("restocked above refunded must be refused")
	}
}

// Review H2 (ut-docs#4768): a line that already moved stock keeps the slot it
// moved, even when the variant is deactivated afterwards, so a later restock
// still lands and the stored item/variant ids are never blanked.
func TestChannelOrders_DeactivatedVariantStillGetsItsRestock(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	mustExecCO(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('var1','itm1','LS-M-BLU-V','Blue M',3500)`)
	mustExecCO(t, d, `INSERT INTO inventory (id, variant_id, location_id, quantity) VALUES ('invv','var1','loc_main',5)`)
	in := sampleChannelOrder()
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "itm1", CloudVariantID: "var1", SKU: "LS-M-BLU-V", Qty: 2, UnitPrice: money.FromMinor(3500)}}
	first, err := repo.Apply(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	mustExecCO(t, d, `UPDATE item_variants SET is_active = 0 WHERE id='var1'`)

	up := in
	up.DirectiveID, up.Revision, up.Status = "dir-2", 2, "partially_refunded"
	up.Lines = []ChannelOrderLineInput{in.Lines[0]}
	up.Lines[0].RefundedQuantity, up.Lines[0].RestockedQuantity = 1, 1
	res, err := repo.Apply(ctx, up)
	if err != nil || len(res.Unmatched) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='invv'`); q != 4 {
		t.Fatalf("variant stock = %v, want 4 (3 + 1 restocked)", q)
	}
	var item, variant sql.NullString
	if err := d.QueryRow(`SELECT item_id, variant_id FROM channel_order_lines WHERE order_id=?`, first.OrderID).Scan(&item, &variant); err != nil {
		t.Fatal(err)
	}
	if item.String != "itm1" || variant.String != "var1" {
		t.Fatalf("stored slot = %q/%q, want itm1/var1", item.String, variant.String)
	}
	if n := scalarCO(t, d, `SELECT unmatched FROM channel_order_lines WHERE order_id=?`, first.OrderID); n != 0 {
		t.Fatalf("unmatched = %v after the restock", n)
	}
}

// Review M5: the status machine is exactly the contract's.
func TestChannelOrders_StatusTransitionsFollowTheContract(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []string
		want  string
	}{
		{"partially_refunded never goes back to accepted", []string{"new", "partially_refunded", "accepted"}, "partially_refunded"},
		{"partially_refunded continues to picked", []string{"accepted", "partially_refunded", "picked"}, "picked"},
		{"fulfilment never regresses through a partial refund", []string{"shipped", "partially_refunded", "picked"}, "partially_refunded"},
		{"shipped then partial refund then packed stays", []string{"shipped", "partially_refunded", "packed"}, "partially_refunded"},
		{"partially_refunded may ship after packed", []string{"packed", "partially_refunded", "shipped"}, "shipped"},
		{"partially_refunded may be cancelled", []string{"shipped", "partially_refunded", "cancelled"}, "cancelled"},
		{"partially_refunded may be refunded", []string{"shipped", "partially_refunded", "refunded"}, "refunded"},
		{"cancelled is terminal", []string{"cancelled", "refunded"}, "cancelled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, repo := openChannelOrders(t)
			ctx := context.Background()
			var id string
			for i, s := range c.steps {
				in := sampleChannelOrder()
				in.DirectiveID, in.Revision, in.Status = "dir-"+s+string(rune('a'+i)), int64(i+1), s
				res, err := repo.Apply(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				id = res.OrderID
			}
			got, _, _ := repo.Get(ctx, id)
			if got.Status != c.want {
				t.Fatalf("%v -> %s, want %s", c.steps, got.Status, c.want)
			}
		})
	}
}

// Review L: a line is sold from the active location holding the most stock.
func TestChannelOrders_InactiveLocationIsNeverChosen(t *testing.T) {
	d, repo := openChannelOrders(t)
	mustExecCO(t, d, `INSERT INTO stock_locations (id, name, is_active) VALUES ('loc_off','Closed',0)`)
	mustExecCO(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv1off','itm1','loc_off',500)`)
	if _, err := repo.Apply(context.Background(), sampleChannelOrder()); err != nil {
		t.Fatal(err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1off'`); q != 500 {
		t.Fatalf("inactive location stock = %v, want untouched 500", q)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("active location stock = %v, want 8", q)
	}
}

// Review D4: the currency flag is recomputed on every revision, not only the
// insert.
func TestChannelOrders_CurrencyMismatchFollowsEveryRevision(t *testing.T) {
	_, repo := openChannelOrders(t)
	ctx := context.Background()
	in := sampleChannelOrder()
	in.CurrencyMismatch = true
	res, _ := repo.Apply(ctx, in)
	if got, _, _ := repo.Get(ctx, res.OrderID); !got.CurrencyMismatch {
		t.Fatal("insert did not store the mismatch")
	}
	up := sampleChannelOrder()
	up.DirectiveID, up.Revision, up.Status = "dir-2", 2, "accepted"
	if _, err := repo.Apply(ctx, up); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := repo.Get(ctx, res.OrderID); got.CurrencyMismatch {
		t.Fatal("a later revision in the shop currency still shows the mismatch")
	}
	up3 := sampleChannelOrder()
	up3.DirectiveID, up3.Revision, up3.Status, up3.CurrencyMismatch = "dir-3", 3, "accepted", true
	_, _ = repo.Apply(ctx, up3)
	if got, _, _ := repo.Get(ctx, res.OrderID); !got.CurrencyMismatch {
		t.Fatal("a later revision in another currency lost the mismatch")
	}
}

// Review L: the total mismatch is persisted, and a negative-stock finding
// names the item even when the line has no SKU.
func TestChannelOrders_TotalMismatchPersistedAndNegativeNamesTheItem(t *testing.T) {
	_, repo := openChannelOrders(t)
	ctx := context.Background()
	in := sampleChannelOrder()
	in.TotalMismatch = true
	in.Lines = []ChannelOrderLineInput{{LineID: "l-1", CloudItemID: "itm2", Name: "Mug", Qty: 3, UnitPrice: money.FromMinor(800)}}
	res, err := repo.Apply(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := repo.Get(ctx, res.OrderID); !got.TotalMismatch {
		t.Fatal("total_mismatch not stored")
	}
	if len(res.NegativeStock) != 1 || res.NegativeStock[0].ItemID != "itm2" || res.NegativeStock[0].Name != "Mug" {
		t.Fatalf("negative = %+v", res.NegativeStock)
	}
}
