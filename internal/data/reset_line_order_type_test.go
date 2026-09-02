package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#1181 / ADR-0073: sale_lines.order_type must survive the
// reset-archive → restore round trip (explicit column lists in
// reset_archive_repo.go — a column missing there is silently erased).
func TestResetThenRestoreRoundTrip_SaleLineOrderType(t *testing.T) {
	d, x, _ := resetTestDB(t, "restore-line-ot.db")
	x(`INSERT INTO items (id, name, base_price) VALUES ('i1','Widget',100)`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, order_type) VALUES ('s1','R1',100,100,'mixed')`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id, order_type)
	   VALUES ('l1','s1',1,'Widget',1,100,0,0,100,100,'i1','takeaway')`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id, order_type)
	   VALUES ('l2','s1',2,'Widget',1,100,0,0,100,100,'i1','')`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	var archived string
	if err := d.DB.QueryRow(`SELECT order_type FROM sale_lines_archive WHERE id='l1'`).Scan(&archived); err != nil || archived != "takeaway" {
		t.Fatalf("archived l1 order_type=%q err=%v, want takeaway", archived, err)
	}
	if _, err := repo.RestoreResetBatch(ctx, batchID, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var restored string
	if err := d.DB.QueryRow(`SELECT order_type FROM sale_lines WHERE id='l1'`).Scan(&restored); err != nil || restored != "takeaway" {
		t.Fatalf("restored l1 order_type=%q err=%v, want takeaway", restored, err)
	}
}
