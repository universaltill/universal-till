package data

import (
	"context"
	"testing"
)

// ut-docs#1181 / ADR-0073 Decision 6: a sale line's own order type is
// persisted on insert and surfaced by every reader a downstream path uses
// (detail for receipts/kitchen/journal/sync/event; snapshots for returns).
func TestPOSRepo_SaleLineOrderType_RoundTrip(t *testing.T) {
	d := openBatch8DB(t, "line-order-type.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active) VALUES ('itm-lot', 'LOT', 'Line OT', 500, 1)`)
	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "sale-lot", ReceiptNo: "000000777", SaleType: "sale", Currency: "GBP",
		Subtotal: 1000, Total: 1000, CreatedAt: "2026-09-01T09:00:00Z", TenderType: "cash",
		OrderType: "mixed", SyncStatus: "synced",
	}); err != nil {
		t.Fatalf("InsertSale: %v", err)
	}
	rows := []SaleLineRow{
		{ID: "lot-1", SaleID: "sale-lot", LineNo: 1, ItemID: "itm-lot", Name: "Line OT", SKU: "LOT", Qty: 1, UnitPrice: 500, TaxRateBP: 1900, TaxAmount: 0, TotalBeforeTax: 500, TotalAfterTax: 500, OrderType: ""},
		{ID: "lot-2", SaleID: "sale-lot", LineNo: 2, ItemID: "itm-lot", Name: "Line OT", SKU: "LOT", Qty: 1, UnitPrice: 500, TaxRateBP: 700, TaxAmount: 0, TotalBeforeTax: 500, TotalAfterTax: 500, OrderType: "takeaway"},
	}
	if err := repo.InsertSaleLinesBatch(ctx, nil, rows); err != nil {
		t.Fatalf("InsertSaleLinesBatch: %v", err)
	}
	detail, ok, err := repo.GetSaleDetail(ctx, "000000777")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail: ok=%v err=%v", ok, err)
	}
	if detail.OrderType != "mixed" {
		t.Fatalf("header = %q, want mixed", detail.OrderType)
	}
	if len(detail.Lines) != 2 || detail.Lines[0].OrderType != "" || detail.Lines[1].OrderType != "takeaway" {
		t.Fatalf("detail line order types = %+v, want [\"\", takeaway]", detail.Lines)
	}
	snaps, err := repo.ListSaleLineSnapshots(ctx, "sale-lot")
	if err != nil {
		t.Fatalf("ListSaleLineSnapshots: %v", err)
	}
	if len(snaps) != 2 || snaps[0].OrderType != "" || snaps[1].OrderType != "takeaway" {
		t.Fatalf("snapshot order types = %+v, want [\"\", takeaway]", snaps)
	}
}
