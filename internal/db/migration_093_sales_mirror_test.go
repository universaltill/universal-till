package db

import "testing"

// 093 adds the read-only sales copy and the main till's change log
// (ut-docs#5074). It must be safe on a till that already holds data: no
// existing row changes, no backfill, nothing logged (the setting has no row
// on any till, so it is off), and re-running it is a no-op.
func TestMigration093_SalesMirror_SafeOnExistingData(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, 93, "sales-mirror.db")
	if _, err := d.DB.Exec(`SELECT 1 FROM mirror_sales`); err == nil {
		t.Fatal("mirror_sales must not exist before 093")
	}
	for _, s := range []string{
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date) VALUES ('s1', '000000001', 'completed', 1000, 1000, '2026-10-25')`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date, till_id) VALUES ('s2', 'T2-000000001', 'voided', 500, 500, '2026-10-25', 'till-2')`,
		`INSERT INTO report_archive (id, kind, period, content_json) VALUES ('ra1', 'eod', '2026-10-25', '{}')`,
		`INSERT INTO tills (id, name) VALUES ('till-2', 'Till 2')`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	d.Close()
	d = openMigratedTo(t, path, 93)

	var sales, archives int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM report_archive`).Scan(&archives)
	if sales != 2 || archives != 1 {
		t.Fatalf("existing rows changed: sales=%d archives=%d", sales, archives)
	}
	for _, tb := range []string{"mirror_sales", "mirror_sale_lines", "mirror_sale_line_modifiers", "mirror_payments",
		"mirror_sale_charges", "mirror_sale_discounts", "mirror_sale_links", "mirror_closes", "mirror_state",
		"sales_mirror_log", "sales_mirror_acks"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
			t.Fatalf("%s missing after 093: %v", tb, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after 093 on an existing till, want 0", tb, n)
		}
	}
	for _, trg := range []string{"trg_sales_mirror_log_sale_ins", "trg_sales_mirror_log_sale_upd",
		"trg_sales_mirror_log_close_ins", "trg_sales_mirror_acks_till_del"} {
		var n int
		_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg).Scan(&n)
		if n != 1 {
			t.Errorf("trigger %s missing", trg)
		}
	}
	// Existing-data updates with the setting off still log nothing.
	if _, err := d.DB.Exec(`UPDATE sales SET status = 'voided' WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	var logged int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales_mirror_log`).Scan(&logged)
	if logged != 0 {
		t.Fatalf("log has %d rows with the setting off", logged)
	}
	// The receipt index on the copy is NOT unique: a copy never fails.
	for _, id := range []string{"m1", "m2"} {
		if _, err := d.DB.Exec(`INSERT INTO mirror_sales (id, src_seq, epoch, origin_till_id, receipt_no, status, sale_type, tender_type,
  currency, subtotal, discount_total, tax_total, total, rounding, service_charge_amount, service_charge_tax_basis_bp,
  voucher_issue_total, offline, created_at, local_date, received_at)
  VALUES (?, 1, 'e', 't', 'T2-000000009', 'completed', 'sale', 'cash', 'GBP', 1, 0, 0, 1, 0, 0, 0, 0, 0, 'x', '2026-10-25', 'x')`, id); err != nil {
			t.Fatalf("duplicate receipt_no in the copy refused: %v", err)
		}
	}
}
