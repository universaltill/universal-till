package pages

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// buildKitchenTicket must carry each item's chosen modifiers through to the
// cook — the whole point of ADR-0020's item modifiers (Farshid: "customer
// should be able to add extra shot to the coffee or customize the
// sandwich") is that the kitchen actually gets told. The print.KitchenTicket
// rendering already supported per-item Modifiers (internal/print/kitchen.go)
// before this change; the gap was that buildKitchenTicket never populated
// them from the persisted sale.
func TestBuildKitchenTicket_IncludesLineModifiers(t *testing.T) {
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "kitchen.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbase.Close()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbase.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee','COFFEE','Flat White',370,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-1','R-0099','completed','sale','GBP',370,0,0,370,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-1','sale-1',1,'itm-coffee','Flat White',1,370,0,0,0,370,370)`)
	mustExec(`INSERT INTO sale_line_modifiers (id, sale_line_id, group_name_snapshot, option_name_snapshot, price_delta_minor) VALUES ('slm-1','line-1','Extras','Extra shot',50)`)

	dp := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}
	ticket, err := buildKitchenTicket(context.Background(), dp, "R-0099")
	if err != nil {
		t.Fatalf("buildKitchenTicket: %v", err)
	}
	if len(ticket.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(ticket.Items))
	}
	if got := ticket.Items[0].Modifiers; len(got) != 1 || got[0] != "Extra shot" {
		t.Fatalf("want kitchen ticket item to carry [Extra shot], got %+v", got)
	}
}
