package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigration077_BackfillsLineOrderTypeFromSaleHeader (ut-docs#1181,
// ADR-0073 Decision 6): historic sales carry order_type only on the header.
// The additive sale_lines.order_type column must be backfilled from that
// header for BOTH live and archived rows, or every pre-existing takeaway
// sale would silently read as dine-in line by line after the upgrade.
// Standard rewind-and-reopen shape: seed the pre-077 shape, drop 077's
// columns, reopen so 077 replays against it.
func TestMigration077_BackfillsLineOrderTypeFromSaleHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m077.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO items (id, name, base_price) VALUES ('i1','Widget',100)`)
	exec(`INSERT INTO sales (id, receipt_no, subtotal, total, order_type) VALUES ('s-ta','R-TA',100,100,'takeaway')`)
	exec(`INSERT INTO sales (id, receipt_no, subtotal, total, order_type) VALUES ('s-di','R-DI',100,100,'')`)
	exec(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id) VALUES ('l-ta','s-ta',1,'Widget',1,100,0,0,100,100,'i1')`)
	exec(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id) VALUES ('l-di','s-di',1,'Widget',1,100,0,0,100,100,'i1')`)
	exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('b1','2026-01-01T00:00:00Z',1)`)
	exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('b2','2026-02-01T00:00:00Z',1)`)
	// Same sale id archived twice in two batches (ADR-0042 never rewrites
	// ids): takeaway in b1, dine-in in b2 — the backfill must join on the
	// batch too, or b2's line would inherit b1's header.
	exec(`INSERT INTO sales_archive (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts, currency, subtotal, discount_total, tax_total, total, rounding, created_at, till_id, service_charge_amount, order_type, order_status, reset_batch_id)
	      VALUES ('s-arch','R-ARCH','completed','sale','cash',0,'synced',0,'GBP',100,0,0,100,0,'2026-01-01T00:00:00Z','till-1',0,'takeaway','','b1')`)
	exec(`INSERT INTO sales_archive (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts, currency, subtotal, discount_total, tax_total, total, rounding, created_at, till_id, service_charge_amount, order_type, order_status, reset_batch_id)
	      VALUES ('s-arch','R-ARCH','completed','sale','cash',0,'synced',0,'GBP',100,0,0,100,0,'2026-02-01T00:00:00Z','till-1',0,'','','b2')`)
	exec(`INSERT INTO sale_lines_archive (id, sale_id, line_no, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id, reset_batch_id) VALUES ('l-arch-1','s-arch',1,'Widget',1,100,0,0,0,100,100,'i1','b1')`)
	exec(`INSERT INTO sale_lines_archive (id, sale_id, line_no, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id, reset_batch_id) VALUES ('l-arch-2','s-arch',1,'Widget',1,100,0,0,0,100,100,'i1','b2')`)
	// Archived RETURN of the b1 takeaway original (header '' as every
	// pre-ADR-0073 return had); the same return id also exists in b2
	// against the b2 (dine-in) original and must stay dine-in.
	for _, batch := range []string{"b1", "b2"} {
		exec(`INSERT INTO sales_archive (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts, currency, subtotal, discount_total, tax_total, total, rounding, created_at, till_id, service_charge_amount, order_type, order_status, reset_batch_id)
		      VALUES ('s-arch-ret','R-ARCH-RET','completed','return','cash',0,'synced',0,'GBP',100,0,0,100,0,'2026-01-02T00:00:00Z','till-1',0,'','',?)`, batch)
		exec(`INSERT INTO sale_lines_archive (id, sale_id, line_no, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id, reset_batch_id) VALUES (?,'s-arch-ret',1,'Widget',1,100,0,0,0,100,100,'i1',?)`, "l-arch-ret-"+batch, batch)
		exec(`INSERT INTO sale_links_archive (id, sale_id, original_sale_id, reason, reset_batch_id) VALUES (?,'s-arch-ret','s-arch','return',?)`, "lnk-"+batch, batch)
	}

	// Review B2: a historic RETURN of a takeaway sale has header '' (the
	// pre-ADR-0073 refund path never set one) — its lines must still be
	// backfilled to takeaway via sale_links, or the refund pool (keyed per
	// mode) can never see that the unit was already returned.
	exec(`INSERT INTO sales (id, receipt_no, subtotal, total, sale_type, order_type) VALUES ('s-ret','R-RET',100,100,'return','')`)
	exec(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id) VALUES ('l-ret','s-ret',1,'Widget',1,100,0,0,100,100,'i1')`)
	exec(`INSERT INTO sale_links (id, sale_id, original_sale_id, reason) VALUES ('lnk','s-ret','s-ta','return')`)

	rewindSaleLineOrderType077(t, d)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 77`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path) // replays 077 against the simulated pre-upgrade till
	if err != nil {
		t.Fatalf("reopen replaying 077: %v", err)
	}
	defer d.Close()

	want := map[string]string{"l-ta": "takeaway", "l-di": "", "l-ret": "takeaway"}
	for id, w := range want {
		var got string
		if err := d.DB.QueryRowContext(ctx, `SELECT order_type FROM sale_lines WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != w {
			t.Fatalf("sale_lines %s order_type = %q, want %q", id, got, w)
		}
	}
	var retHeader string
	if err := d.DB.QueryRowContext(ctx, `SELECT order_type FROM sales WHERE id = 's-ret'`).Scan(&retHeader); err != nil || retHeader != "takeaway" {
		t.Fatalf("return header after backfill = %q err=%v, want takeaway (derived like every sale)", retHeader, err)
	}
	wantArch := map[string]string{"l-arch-1": "takeaway", "l-arch-2": "", "l-arch-ret-b1": "takeaway", "l-arch-ret-b2": ""}
	for id, w := range wantArch {
		var got string
		if err := d.DB.QueryRowContext(ctx, `SELECT order_type FROM sale_lines_archive WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read archive %s: %v", id, err)
		}
		if got != w {
			t.Fatalf("sale_lines_archive %s order_type = %q, want %q (batch-scoped backfill)", id, got, w)
		}
	}
	for batch, want := range map[string]string{"b1": "takeaway", "b2": ""} {
		var got string
		if err := d.DB.QueryRowContext(ctx, `SELECT order_type FROM sales_archive WHERE id = 's-arch-ret' AND reset_batch_id = ?`, batch).Scan(&got); err != nil || got != want {
			t.Fatalf("archived return header (%s) = %q err=%v, want %q", batch, got, err, want)
		}
	}
}
