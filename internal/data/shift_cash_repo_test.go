package data

import (
	"context"
	"database/sql"
	"testing"
)

// Cash-tip rule (ut-docs#5801): Opening + CashSales + TipsHeldOut +
// Adjustments == Expected on every shift cash view.

func nullStr(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

func scInsertSale(t *testing.T, dbx *posTestDB, id, status, register string, total int64, at string) {
	t.Helper()
	if _, err := dbx.d.DB.ExecContext(context.Background(), `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES(?, ?, ?, 'sale', 'GBP', ?, 0, 0, ?, ?, ?, ?)`, id, "R-"+id, status, total, total, register, at, at); err != nil {
		t.Fatal(err)
	}
}

func scInsertPayment(t *testing.T, dbx *posTestDB, id, saleID, method string, amount, change, tip int64, at string) {
	t.Helper()
	if _, err := dbx.d.DB.ExecContext(context.Background(), `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at)
VALUES(?, ?, ?, ?, 'GBP', ?, ?, ?)`, id, saleID, method, amount, change, tip, at); err != nil {
		t.Fatal(err)
	}
}

func TestShiftCashBreakdown_HoldsCashTipsOutOfCashSales(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg-sc','SC Till',1)`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-sc", "reg-sc", "user1", 10000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	scInsertSale(t, dbx, "sale-sc-cash", "completed", "reg-sc", 40000, "2026-01-01T10:00:00Z")
	scInsertPayment(t, dbx, "pay-sc-cash", "sale-sc-cash", "cash", 45000, 3000, 2000, "2026-01-01T10:00:00Z")
	scInsertSale(t, dbx, "sale-sc-card", "completed", "reg-sc", 1500, "2026-01-01T10:30:00Z")
	scInsertPayment(t, dbx, "pay-sc-card", "sale-sc-card", "card", 1800, 0, 300, "2026-01-01T10:30:00Z")
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-sc", "cash_adjustment", map[string]any{"amount": -1500}, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	b, err := dbx.repo.ShiftCashBreakdown(ctx, "shift-sc", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if b.Opening.Minor() != 10000 || b.CashSales.Minor() != 40000 || b.TipsHeldOut.Minor() != 2000 ||
		b.Adjustments.Minor() != -1500 || b.Expected.Minor() != 50500 {
		t.Fatalf("breakdown = %+v, want opening 10000 cash 40000 tips 2000 adj -1500 expected 50500", b)
	}
	if sum := b.Opening.Add(b.CashSales).Add(b.TipsHeldOut).Add(b.Adjustments); sum != b.Expected {
		t.Fatalf("identity broken: sum %d != expected %d", sum.Minor(), b.Expected.Minor())
	}
	exp, err := dbx.repo.ComputeExpectedCash(ctx, "shift-sc", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if exp != 50500 {
		t.Fatalf("ComputeExpectedCash = %d, want 50500", exp)
	}
	tips, err := dbx.repo.SumCashTipsForShift(ctx, "reg-sc", nullStr("2026-01-01T09:00:00Z"), nullStr(""))
	if err != nil {
		t.Fatal(err)
	}
	if tips != 2000 {
		t.Fatalf("SumCashTipsForShift = %d, want 2000 (card tip excluded)", tips)
	}
	if _, err := dbx.repo.ShiftCashBreakdown(ctx, "", 0); err == nil {
		t.Fatal("expected an error for an empty shift id")
	}
}

func TestShiftCashBreakdown_NoCashTip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg-nt','NT Till',1)`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-nt", "reg-nt", "user1", 5000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	scInsertSale(t, dbx, "sale-nt", "completed", "reg-nt", 1000, "2026-01-01T10:00:00Z")
	scInsertPayment(t, dbx, "pay-nt", "sale-nt", "cash", 1000, 0, 0, "2026-01-01T10:00:00Z")
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-nt", "cash_adjustment", map[string]any{"amount": -150}, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	b, err := dbx.repo.ShiftCashBreakdown(ctx, "shift-nt", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if b.CashSales.Minor() != 1000 || b.TipsHeldOut.Minor() != 0 || b.Expected.Minor() != 5850 {
		t.Fatalf("breakdown = %+v, want cash 1000 tips 0 expected 5850", b)
	}
}

func TestSumCashTipsForShift_SameWindowAsCashSales(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"reg-w", "reg-other"} {
		if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES(?,?,1)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-w", "reg-w", "user1", 0, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-w", 0, 0, 0, "", "", "2026-01-01T17:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, status, reg, at string
		tip                 int64
	}{
		{"in", "completed", "reg-w", "2026-01-01T12:00:00Z", 500},
		{"before", "completed", "reg-w", "2026-01-01T08:00:00Z", 700},
		{"after", "completed", "reg-w", "2026-01-01T18:00:00Z", 900},
		{"other", "completed", "reg-other", "2026-01-01T12:00:00Z", 1100},
		{"voided", "voided", "reg-w", "2026-01-01T12:00:00Z", 1300},
	}
	for _, r := range rows {
		scInsertSale(t, dbx, "sale-"+r.id, r.status, r.reg, 10000, r.at)
		scInsertPayment(t, dbx, "pay-"+r.id, "sale-"+r.id, "cash", 10000+r.tip, 0, r.tip, r.at)
	}
	opened, closed := nullStr("2026-01-01T09:00:00Z"), nullStr("2026-01-01T17:00:00Z")
	tips, err := dbx.repo.SumCashTipsForShift(ctx, "reg-w", opened, closed)
	if err != nil {
		t.Fatal(err)
	}
	if tips != 500 {
		t.Fatalf("tips = %d, want 500", tips)
	}
	tendered, err := dbx.repo.SumCashPaymentsForShift(ctx, "reg-w", opened, closed)
	if err != nil {
		t.Fatal(err)
	}
	if tendered != 10500 {
		t.Fatalf("tendered = %d, want 10500 (same single in-window payment)", tendered)
	}
}
