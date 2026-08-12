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
	// ADR-0040 §9: report_archive is a retained legal record, not
	// transactional/test data -- a reset must NOT touch it.
	x(`INSERT INTO report_archive (id, kind, period, content_json) VALUES ('ra1','eod','2026-01-01','{"net":100}')`)

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
	// ADR-0040 §9: report_archive is a retained legal record and must
	// survive a transaction-history reset regardless of retention mode.
	if c := count("report_archive"); c != 1 {
		t.Fatalf("report_archive must survive a reset (retained legal record, ADR-0040 §9), got %d row(s)", c)
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

func TestCleanupObsoleteItems(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, a ...any) {
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// obs: inactive + never sold -> removable.
	x(`INSERT INTO stock_locations (id, name) VALUES ('loc1','Main')`)
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs','Old Test Product',100,0)`)
	x(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv1','obs','loc1',0)`)
	// sold: inactive BUT has a sale line -> must be kept.
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('sold','Discontinued But Sold',100,0)`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',100,100)`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id)
	   VALUES ('l1','s1',1,'Discontinued But Sold',1,100,0,0,100,100,'sold')`)
	// active: not a cleanup target.
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('live','Current Product',100,1)`)

	repo := data.NewPOSRepo(d.DB)
	preview, err := repo.ListObsoleteItems(context.Background(), 100)
	if err != nil || len(preview) != 1 || preview[0].ID != "obs" {
		t.Fatalf("preview should list only 'obs': err=%v got=%+v", err, preview)
	}
	n, err := repo.CleanupObsoleteItems(context.Background(), "")
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	has := func(id string) bool {
		var c int
		d.DB.QueryRow(`SELECT count(*) FROM items WHERE id=?`, id).Scan(&c)
		return c == 1
	}
	if has("obs") {
		t.Fatal("obsolete item not removed")
	}
	if !has("sold") || !has("live") {
		t.Fatal("sold/active items must be kept")
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='catalog_cleanup'`).Scan(&action); err != nil {
		t.Fatalf("cleanup not audited: %v", err)
	}
}

// An obsolete item carrying a kitchen-station override must still clean up
// (code review, ut-docs#516): item_station_routes has no pre-delete step in
// CleanupObsoleteItems, so this only works because 034_kitchen_stations.sql
// gives both routing tables ON DELETE CASCADE — without it this reproduces
// a raw "FOREIGN KEY constraint failed" for the whole batch.
func TestCleanupObsoleteItems_ItemWithKitchenStationRouteCascades(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cleanup-station.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, a ...any) {
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs2','Old Grill Item',100,0)`)

	repo := data.NewPOSRepo(d.DB)
	stationID, err := repo.CreateKitchenStation(context.Background(), "Grill", "g:9100")
	if err != nil {
		t.Fatalf("CreateKitchenStation: %v", err)
	}
	if err := repo.SetItemStationRoutes(context.Background(), "obs2", []string{stationID}); err != nil {
		t.Fatalf("SetItemStationRoutes: %v", err)
	}

	n, err := repo.CleanupObsoleteItems(context.Background(), "")
	if err != nil {
		t.Fatalf("cleanup must not fail on an item with a station route: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 item cleaned up, got %d", n)
	}
	var routeCount int
	d.DB.QueryRow(`SELECT count(*) FROM item_station_routes WHERE item_id='obs2'`).Scan(&routeCount)
	if routeCount != 0 {
		t.Fatalf("station route must cascade-delete with its item, got %d rows left", routeCount)
	}
}
