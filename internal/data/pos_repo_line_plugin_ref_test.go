package data

import (
	"context"
	"testing"
)

// ut-docs#7206: a sale line's opaque plugin ref and the plugin that set it
// are persisted on insert and read back with the sale detail; a line
// without one reads as "".
func TestPOSRepo_SaleLinePluginRef_RoundTrip_7206(t *testing.T) {
	d := openBatch8DB(t, "line-plugin-ref.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active) VALUES ('itm-ref', 'SVC', 'Service', 2500, 1)`)
	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "sale-ref", ReceiptNo: "000000778", SaleType: "sale", Currency: "GBP",
		Subtotal: 7500, Total: 7500, CreatedAt: "2026-11-28T09:00:00Z", TenderType: "cash", SyncStatus: "synced", // fixed-date:ok stored and read back only, never compared to the clock
	}); err != nil {
		t.Fatalf("InsertSale: %v", err)
	}
	rows := []SaleLineRow{
		{ID: "ref-1", SaleID: "sale-ref", LineNo: 1, ItemID: "itm-ref", Name: "Service", SKU: "SVC", Qty: 1, UnitPrice: 2500, TotalBeforeTax: 2500, TotalAfterTax: 2500, PluginRef: "appt:1:0", PluginRefBy: "com.example.page"},
		{ID: "ref-2", SaleID: "sale-ref", LineNo: 2, ItemID: "itm-ref", Name: "Service", SKU: "SVC", Qty: 1, UnitPrice: 2500, TotalBeforeTax: 2500, TotalAfterTax: 2500, PluginRef: "appt:1:1", PluginRefBy: "com.example.page"},
		{ID: "ref-3", SaleID: "sale-ref", LineNo: 3, ItemID: "itm-ref", Name: "Service", SKU: "SVC", Qty: 1, UnitPrice: 2500, TotalBeforeTax: 2500, TotalAfterTax: 2500},
	}
	if err := repo.InsertSaleLinesBatch(ctx, nil, rows); err != nil {
		t.Fatalf("InsertSaleLinesBatch: %v", err)
	}
	detail, ok, err := repo.GetSaleDetailByID(ctx, "sale-ref")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetailByID: ok=%v err=%v", ok, err)
	}
	if len(detail.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(detail.Lines))
	}
	for i, want := range [][2]string{{"appt:1:0", "com.example.page"}, {"appt:1:1", "com.example.page"}, {"", ""}} {
		if got := [2]string{detail.Lines[i].PluginRef, detail.Lines[i].PluginRefBy}; got != want {
			t.Errorf("line %d ref = %v, want %v", i+1, got, want)
		}
	}
}
