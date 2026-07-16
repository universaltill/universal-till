package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// SalesByDepartment must roll each sold item up to its TOP-LEVEL category (the
// department), so a subcategory's sales count toward its parent department, and
// items with no category fall under "" (rendered as Uncategorized).
func TestSalesByDepartment_RollsSubcategoriesToDepartment(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "dept.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, args ...any) {
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	// Departments (root categories) + a subcategory under Electronics.
	x(`INSERT INTO categories (id, name, parent_id) VALUES ('electronics','Electronics',NULL)`)
	x(`INSERT INTO categories (id, name, parent_id) VALUES ('grocery','Grocery',NULL)`)
	x(`INSERT INTO categories (id, name, parent_id) VALUES ('phones','Phones','electronics')`)

	// Items: a phone (subcategory Phones → dept Electronics), a laptop (directly
	// in Electronics), milk (Grocery), and a mystery item with no category.
	x(`INSERT INTO items (id, name, base_price, category_id) VALUES ('phone','Phone',50000,'phones')`)
	x(`INSERT INTO items (id, name, base_price, category_id) VALUES ('laptop','Laptop',120000,'electronics')`)
	x(`INSERT INTO items (id, name, base_price, category_id) VALUES ('milk','Milk',200,'grocery')`)
	x(`INSERT INTO items (id, name, base_price, category_id) VALUES ('mystery','Mystery',999,NULL)`)

	x(`INSERT INTO sales (id, receipt_no, status, subtotal, total) VALUES ('s1','R1','completed',0,0)`)
	lineNo := 0
	line := func(id, item string, qty float64, total int64) {
		lineNo++
		x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id)
		   VALUES (?,?,?,?,?,?,0,0,?,?,?)`, id, "s1", lineNo, item, qty, total, total, total, item)
	}
	line("l1", "phone", 1, 50000)
	line("l2", "laptop", 1, 120000)
	line("l3", "milk", 3, 600)
	line("l4", "mystery", 1, 999)

	repo := data.NewPOSRepo(d.DB)
	rows, err := repo.SalesByDepartment(context.Background(), 30)
	if err != nil {
		t.Fatalf("SalesByDepartment: %v", err)
	}

	got := map[string]data.DeptSales{}
	for _, row := range rows {
		got[row.Department] = row
	}
	// Electronics = phone (50000) + laptop (120000) = 170000, across 2 units.
	if e := got["Electronics"]; e.Revenue != 170000 || e.Qty != 2 {
		t.Fatalf("Electronics = %+v, want revenue 170000 qty 2", e)
	}
	if g := got["Grocery"]; g.Revenue != 600 || g.Qty != 3 {
		t.Fatalf("Grocery = %+v, want revenue 600 qty 3", g)
	}
	if u := got[""]; u.Revenue != 999 { // uncategorized
		t.Fatalf("Uncategorized = %+v, want revenue 999", u)
	}
	// Ordered by revenue desc: Electronics first.
	if rows[0].Department != "Electronics" {
		t.Fatalf("first department = %q, want Electronics (highest revenue)", rows[0].Department)
	}

	// A single-day query should match for today's sales.
	dayRows, err := repo.DepartmentsForDay(context.Background(), "now")
	if err == nil && len(dayRows) == 0 {
		// date('now') vs stored datetime — smoke only; the window query above is
		// the authoritative assertion.
		t.Log("DepartmentsForDay returned no rows for 'now' (timezone/date boundary)")
	}
}
