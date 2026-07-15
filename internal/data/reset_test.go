package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func TestResetTransactionHistoryClearsSalesKeepsCatalog(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "reset.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, args ...any) {
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// Catalog (must survive) + a completed sale with a line, payment and invoice.
	x(`INSERT INTO items (id, name, base_price) VALUES ('i1','Widget',100)`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',100,100)`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id)
	   VALUES ('l1','s1',1,'Widget',1,100,0,0,100,100,'i1')`)
	x(`INSERT INTO payments (id, sale_id, method_id, amount) VALUES ('p1','s1','cash',100)`)
	x(`INSERT INTO invoices (id, series, invoice_no, display_no, sale_id, customer_name, seller_json, net_total, tax_total, gross_total, vat_breakdown_json, issued_at, issued_by)
	   VALUES ('inv1','A',1,'A-1','s1','Cust','{}',100,0,100,'[]','2026-01-01T00:00:00Z','u1')`)

	count := func(tbl string) int {
		var c int
		if err := d.DB.QueryRow("SELECT count(*) FROM " + tbl).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		return c
	}
	itemsBefore := count("items") // fresh DB may seed a sample catalog

	n, err := data.NewPOSRepo(d.DB).ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n != 1 {
		t.Fatalf("sales_deleted = %d, want 1", n)
	}
	for _, tbl := range []string{"sales", "sale_lines", "payments", "invoices"} {
		if c := count(tbl); c != 0 {
			t.Fatalf("%s not cleared: %d", tbl, c)
		}
	}
	if c := count("items"); c != itemsBefore {
		t.Fatalf("catalog must survive, items %d -> %d", itemsBefore, c)
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='transaction_history_reset'`).Scan(&action); err != nil {
		t.Fatalf("reset not audited: %v", err)
	}
}

func TestEraseCustomer(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "erase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, a ...any) {
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	x(`INSERT INTO customers (id, name, phone, email) VALUES ('c1','Ada Lovelace','555','ada@x.com')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, customer_id) VALUES ('s1','R1',100,100,'c1')`)

	repo := data.NewPOSRepo(d.DB)
	found, err := repo.SearchCustomers(context.Background(), "ada", 10)
	if err != nil || len(found) != 1 || found[0].ID != "c1" {
		t.Fatalf("search: err=%v found=%+v", err, found)
	}
	ok, err := repo.EraseCustomer(context.Background(), "c1", "")
	if err != nil || !ok {
		t.Fatalf("erase: ok=%v err=%v", ok, err)
	}
	var custs int
	d.DB.QueryRow(`SELECT count(*) FROM customers WHERE id='c1'`).Scan(&custs)
	if custs != 0 {
		t.Fatalf("customer not erased")
	}
	// The sale is KEPT but anonymised (customer_id NULL).
	var cid *string
	var saleCount int
	d.DB.QueryRow(`SELECT count(*) FROM sales WHERE id='s1'`).Scan(&saleCount)
	d.DB.QueryRow(`SELECT customer_id FROM sales WHERE id='s1'`).Scan(&cid)
	if saleCount != 1 || cid != nil {
		t.Fatalf("sale should be kept + unlinked: count=%d cid=%v", saleCount, cid)
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='customer_erased'`).Scan(&action); err != nil {
		t.Fatalf("erasure not audited: %v", err)
	}
}
