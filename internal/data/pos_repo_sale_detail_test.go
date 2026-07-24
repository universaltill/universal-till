package data

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// GetSaleDetail must surface each line's chosen modifiers (ADR-0020) — the
// kitchen ticket (internal/pages/kitchen_print.go) reads this to tell a cook
// "extra shot" / "no onions" apply to a specific item, and nothing else in
// the repo currently re-reads sale_line_modifiers after the sale completes.
func TestPOSRepo_GetSaleDetail_IncludesLineModifiers(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "sale_detail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}

	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee','COFFEE','Flat White',370,1)`)
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-tea','TEA','Tea',250,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-1','R-0099','completed','sale','GBP',370,0,0,370,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-1','sale-1',1,'itm-coffee','Flat White',1,370,0,0,0,370,370)`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-2','sale-1',2,'itm-tea','Tea',1,250,0,0,0,250,250)`)
	mustExec(`INSERT INTO sale_line_modifiers (id, sale_line_id, group_name_snapshot, option_name_snapshot, price_delta_minor) VALUES ('slm-1','line-1','Extras','Extra shot',50)`)
	mustExec(`INSERT INTO sale_line_modifiers (id, sale_line_id, group_name_snapshot, option_name_snapshot, price_delta_minor) VALUES ('slm-2','line-1','Milk','Oat milk',0)`)

	detail, ok, err := NewPOSRepo(d.DB).GetSaleDetail(ctx, "R-0099")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(detail.Lines))
	}
	if got := detail.Lines[0].Modifiers; len(got) != 2 || got[0] != "Extra shot" || got[1] != "Oat milk" {
		t.Fatalf("want [Extra shot, Oat milk] on line 1 (in insertion order), got %+v", got)
	}
	if got := detail.Lines[1].Modifiers; len(got) != 0 {
		t.Fatalf("Tea line has no modifiers, got %+v", got)
	}
}
