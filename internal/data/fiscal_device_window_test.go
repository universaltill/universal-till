package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// fdwOpenDB opens a fresh migrated DB in a temp dir — same convention as
// b8OpenDB (pos_repo_batch8_reports_test.go), reused here rather than
// duplicated: real DB, real migrations, exact assertions.
func fdwOpenDB(t *testing.T, name string) *db.DB {
	return b8OpenDB(t, name)
}

// fdwReceipt seeds a fiscal_device_receipts row directly (no FK to sales —
// see migration 010's schema), with an explicit created_at so ordering/
// windowing doesn't depend on the clock.
func fdwReceipt(t *testing.T, d *db.DB, saleID, maker, serial, receiptKind string, zNo int64, createdAt string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO fiscal_device_receipts
(sale_id, device_kind, maker, serial, receipt_no, receipt_kind, z_no, issued_at, created_at)
VALUES (?, 'okc', ?, ?, ?, ?, ?, ?, ?)`,
		saleID, maker, serial, "RCPT-"+saleID, receiptKind, zNo, createdAt, createdAt)
}

// fdwOKCMethod seeds the "okc" payment method row payments.method_id's FK
// requires.
func fdwOKCMethod(t *testing.T, d *db.DB) {
	t.Helper()
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('okc', 'tender.okc', 'device', 1, 0)`)
}

// fdwPayment seeds a payments row tendering amt on methodID for saleID.
func fdwPayment(t *testing.T, d *db.DB, id, saleID, methodID string, amt int64, paidAt string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES (?, ?, ?, ?, 'TRY', 0, ?)`,
		id, saleID, methodID, amt, paidAt)
}

func TestFiscalDeviceWindow_Empty(t *testing.T) {
	d := fdwOpenDB(t, "fdw-empty.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()

	win, err := repo.FiscalDeviceWindow(ctx, "okc", time.Time{}, time.Now())
	if err != nil {
		t.Fatalf("FiscalDeviceWindow: %v", err)
	}
	if win.Serial != "" || win.Maker != "" || win.ZNos != nil || win.MaliFis != 0 ||
		win.IadeFisi != 0 || win.BilgiFisi != 0 || win.Other != 0 || win.Total != 0 || win.TillOKCTenders != 0 {
		t.Fatalf("empty window should be the zero struct with nil ZNos, got %+v", win)
	}
	if !win.Empty() {
		t.Fatalf("Empty() = false, want true for a window with no device receipts and no till tenders")
	}
}

func TestFiscalDeviceWindow_CountsAndTillTenders(t *testing.T) {
	d := fdwOpenDB(t, "fdw-counts.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	fdwOKCMethod(t, d)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	from := today.Add(-1 * time.Hour)
	to := today.Add(1 * time.Hour)

	// Two sales + one refund (return), all completed, all tendered on okc.
	b8Sale(t, d, "fdw-sale-1", b8At(today), "completed", "sale", 0, 1000)
	b8Sale(t, d, "fdw-sale-2", b8At(today), "completed", "sale", 0, 2000)
	b8Sale(t, d, "fdw-refund-1", b8At(today), "completed", "return", 0, -500)
	fdwPayment(t, d, "fdw-pay-1", "fdw-sale-1", "okc", 1000, b8At(today))
	fdwPayment(t, d, "fdw-pay-2", "fdw-sale-2", "okc", 2000, b8At(today))
	fdwPayment(t, d, "fdw-pay-3", "fdw-refund-1", "okc", -500, b8At(today))

	// Non-completed sale on okc must not count.
	b8Sale(t, d, "fdw-voided", b8At(today), "voided", "sale", 0, 999)
	fdwPayment(t, d, "fdw-pay-4", "fdw-voided", "okc", 999, b8At(today))

	// A payment on a different method must not count.
	b8Sale(t, d, "fdw-cash-sale", b8At(today), "completed", "sale", 0, 300)
	fdwPayment(t, d, "fdw-pay-5", "fdw-cash-sale", "cash", 300, b8At(today))

	// Device receipts: asymmetric distinctive counts (review finding: a
	// symmetric 1/1 split can't catch a MaliFis/IadeFisi field swap) — 2
	// mali_fis + 1 iade_fisi + 1 bilgi_fisi, all z_no=3.
	fdwReceipt(t, d, "fdw-sale-1", "beko", "AV0001", "mali_fis", 3, b8At(today))
	fdwReceipt(t, d, "fdw-sale-2", "beko", "AV0001", "mali_fis", 3, b8At(today.Add(5*time.Minute)))
	fdwReceipt(t, d, "fdw-refund-1", "beko", "AV0001", "iade_fisi", 3, b8At(today.Add(10*time.Minute)))
	fdwReceipt(t, d, "fdw-cash-sale", "beko", "AV0001", "bilgi_fisi", 3, b8At(today.Add(15*time.Minute)))

	// Just before the window's lower bound — must be EXCLUDED (half-open
	// window, review finding: the original test never exercised the lower
	// bound at all).
	fdwReceipt(t, d, "fdw-before", "beko", "AV0001", "mali_fis", 3, b8At(from.Add(-1*time.Minute)))

	win, err := repo.FiscalDeviceWindow(ctx, "okc", from, to)
	if err != nil {
		t.Fatalf("FiscalDeviceWindow: %v", err)
	}
	if win.MaliFis != 2 || win.IadeFisi != 1 || win.BilgiFisi != 1 || win.Other != 0 || win.Total != 4 {
		t.Fatalf("counts = %+v, want mali=2 iade=1 bilgi=1 total=4 (the from-1min receipt excluded)", win)
	}
	if len(win.ZNos) != 1 || win.ZNos[0] != 3 {
		t.Fatalf("ZNos = %v, want [3]", win.ZNos)
	}
	if win.Serial != "AV0001" || win.Maker != "beko" {
		t.Fatalf("serial/maker = %q/%q, want AV0001/beko", win.Serial, win.Maker)
	}
	if win.TillOKCTenders != 3 {
		t.Fatalf("TillOKCTenders = %d, want 3 (2 sales + 1 refund, voided sale + cash-method payment excluded)", win.TillOKCTenders)
	}
	if win.Empty() {
		t.Fatalf("Empty() = true, want false for a window with real device receipts and till tenders")
	}
}

func TestFiscalDeviceWindow_MultiZAndExcludedOutsideWindow(t *testing.T) {
	d := fdwOpenDB(t, "fdw-multiz.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	from := today
	to := today.Add(2 * time.Hour)

	fdwReceipt(t, d, "z-sale-1", "pavo", "SER1", "mali_fis", 3, b8At(today.Add(10*time.Minute)))
	fdwReceipt(t, d, "z-sale-2", "pavo", "SER1", "mali_fis", 4, b8At(today.Add(20*time.Minute)))
	// Just outside `to` — must be excluded (half-open window).
	fdwReceipt(t, d, "z-sale-3", "pavo", "SER1", "mali_fis", 5, b8At(to.Add(1*time.Minute)))

	win, err := repo.FiscalDeviceWindow(ctx, "okc", from, to)
	if err != nil {
		t.Fatalf("FiscalDeviceWindow: %v", err)
	}
	if len(win.ZNos) != 2 || win.ZNos[0] != 3 || win.ZNos[1] != 4 {
		t.Fatalf("ZNos = %v, want [3 4] (z_no=5 receipt outside window excluded)", win.ZNos)
	}
	if win.Total != 2 {
		t.Fatalf("Total = %d, want 2", win.Total)
	}
	// Serial/maker should reflect the LATEST receipt actually in window
	// (z_no=4's), not the excluded z_no=5 one.
	if win.Serial != "SER1" || win.Maker != "pavo" {
		t.Fatalf("serial/maker = %q/%q, want SER1/pavo", win.Serial, win.Maker)
	}
}
