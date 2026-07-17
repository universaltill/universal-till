package pages

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// Slow-sellers and dead-stock queries: an item that sold once is a slow
// seller (not dead); an item with stock and no sales is dead stock (value =
// qty × base_price); a sold-out never-seller doesn't appear in dead stock.
func TestSlowItemsAndDeadStock(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rep.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-a','Seller','A',100,1)`)
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-b','Shelf Warmer','B',250,1)`)
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-c','Sold Out','C',300,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1','Floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-a','it-a','loc-1',5)`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-b','it-b','loc-1',4)`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-c','it-c','loc-1',0)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('s1','R1','completed','sale',100,0,100, datetime('now','-2 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('l1','s1',1,'it-a','Seller',1,100,0,0,0,100,100)`)

	repo := data.NewPOSRepo(d)
	ctx := context.Background()

	// db.Open seeds the demo catalog, so assert on OUR rows, not exact lists.
	slow, err := repo.SlowItems(ctx, 30, 100)
	if err != nil {
		t.Fatalf("slow: %v", err)
	}
	foundSeller := false
	for _, s := range slow {
		if s.Name == "Seller" {
			foundSeller = true
		}
		if s.Name == "Shelf Warmer" || s.Name == "Sold Out" {
			t.Fatalf("never-sold item %q must not be a slow SELLER", s.Name)
		}
	}
	if !foundSeller {
		t.Fatalf("Seller missing from slow list: %+v", slow)
	}

	dead, err := repo.DeadStock(ctx, 30, 1000)
	if err != nil {
		t.Fatalf("dead: %v", err)
	}
	var warmer *data.DeadStockRow
	for i := range dead {
		switch dead[i].Name {
		case "Shelf Warmer":
			warmer = &dead[i]
		case "Seller":
			t.Fatal("an item that sold must not be dead stock")
		case "Sold Out":
			t.Fatal("zero-stock items must not be dead stock")
		}
	}
	if warmer == nil {
		t.Fatalf("Shelf Warmer missing from dead stock")
	}
	if warmer.StockValue != 1000 { // 4 × 250
		t.Fatalf("dead value = %d, want 1000", warmer.StockValue)
	}

	// Busy-times buckets: our one sale lands in exactly one weekday bucket
	// and one hour bucket, and totals carry through.
	wd, err := repo.SalesByWeekday(ctx, 30)
	if err != nil {
		t.Fatalf("weekday: %v", err)
	}
	hr, err := repo.SalesByHour(ctx, 30)
	if err != nil {
		t.Fatalf("hour: %v", err)
	}
	sumWd, sumHr := 0, 0
	for _, b := range wd {
		if b.Slot < 0 || b.Slot > 6 {
			t.Fatalf("weekday slot out of range: %d", b.Slot)
		}
		sumWd += b.Count
	}
	for _, b := range hr {
		if b.Slot < 0 || b.Slot > 23 {
			t.Fatalf("hour slot out of range: %d", b.Slot)
		}
		sumHr += b.Count
	}
	if sumWd != 1 || sumHr != 1 {
		t.Fatalf("bucket sums = %d/%d, want 1/1", sumWd, sumHr)
	}
}
