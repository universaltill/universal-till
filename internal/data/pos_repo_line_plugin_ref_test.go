package data

import (
	"context"
	"encoding/json"
	"strings"
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

// The per-line plugin ref is main-till scope (ADR-0121 amendment,
// ut-docs#7206): SaleDetail is the replica->primary / LAN journal wire, so
// marshalling it with refs set must carry no plugin_ref / plugin_ref_by key
// (nor the values) under any spelling.
func TestSaleDetailJSON_OmitsPluginRef_7206(t *testing.T) {
	d := SaleDetail{ID: "s1", Lines: []SaleDetailLine{{
		Name: "Skin fade", SKU: "SVC", PluginRef: "appt:1:0", PluginRefBy: "com.example.page",
	}}}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(d.Lines[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{out, line} {
		got := strings.ToLower(string(b))
		for _, bad := range []string{"plugin_ref", "pluginref", "appt:1:0", "com.example.page"} {
			if strings.Contains(got, bad) {
				t.Errorf("sale detail JSON carries %q: %s", bad, b)
			}
		}
	}
}
