package data

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
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

// scInsertReturn inserts a sale_type='return' sale (a refund).
func scInsertReturn(t *testing.T, dbx *posTestDB, id, status, register string, total int64, at string) {
	t.Helper()
	if _, err := dbx.d.DB.ExecContext(context.Background(), `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES(?, ?, ?, 'return', 'GBP', ?, 0, 0, ?, ?, ?, ?)`, id, "R-"+id, status, total, total, register, at, at); err != nil {
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

// Cash refunds subtract from the drawer (ut-docs#5803).
func TestShiftCashBreakdown_SubtractsCashRefunds(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"reg-r", "reg-r2"} {
		if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES(?,?,1)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-r", "reg-r", "user1", 10000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	scInsertSale(t, dbx, "s-cash", "completed", "reg-r", 6000, "2026-01-01T10:00:00Z")
	scInsertPayment(t, dbx, "p-cash", "s-cash", "cash", 6000, 1000, 0, "2026-01-01T10:00:00Z")
	scInsertReturn(t, dbx, "r-cash", "completed", "reg-r", 1500, "2026-01-01T11:00:00Z")
	scInsertPayment(t, dbx, "pr-cash", "r-cash", "cash", 1500, 0, 0, "2026-01-01T11:00:00Z")
	// Ignored: card refund, other register, voided, before the shift opened.
	scInsertReturn(t, dbx, "r-card", "completed", "reg-r", 700, "2026-01-01T11:10:00Z")
	scInsertPayment(t, dbx, "pr-card", "r-card", "card", 700, 0, 0, "2026-01-01T11:10:00Z")
	scInsertReturn(t, dbx, "r-oth", "completed", "reg-r2", 900, "2026-01-01T11:20:00Z")
	scInsertPayment(t, dbx, "pr-oth", "r-oth", "cash", 900, 0, 0, "2026-01-01T11:20:00Z")
	scInsertReturn(t, dbx, "r-void", "voided", "reg-r", 800, "2026-01-01T11:30:00Z")
	scInsertPayment(t, dbx, "pr-void", "r-void", "cash", 800, 0, 0, "2026-01-01T11:30:00Z")
	scInsertReturn(t, dbx, "r-early", "completed", "reg-r", 600, "2026-01-01T08:00:00Z")
	scInsertPayment(t, dbx, "pr-early", "r-early", "cash", 600, 0, 0, "2026-01-01T08:00:00Z")

	sum, err := dbx.repo.SumCashPaymentsForShift(ctx, "reg-r", nullStr("2026-01-01T09:00:00Z"), nullStr(""))
	if err != nil {
		t.Fatal(err)
	}
	if sum != 3500 {
		t.Fatalf("SumCashPaymentsForShift = %d, want 3500", sum)
	}
	b, err := dbx.repo.ShiftCashBreakdown(ctx, "shift-r", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if b.Opening.Minor() != 10000 || b.CashSales.Minor() != 3500 || b.TipsHeldOut.Minor() != 0 ||
		b.Adjustments.Minor() != 0 || b.Expected.Minor() != 13500 {
		t.Fatalf("breakdown = %+v, want opening 10000 cash 3500 tips 0 adj 0 expected 13500", b)
	}
	if sum := b.Opening.Add(b.CashSales).Add(b.TipsHeldOut).Add(b.Adjustments); sum != b.Expected {
		t.Fatalf("identity broken: sum %d != expected %d", sum.Minor(), b.Expected.Minor())
	}
	exp, err := dbx.repo.ComputeExpectedCash(ctx, "shift-r", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if exp != 13500 {
		t.Fatalf("ComputeExpectedCash = %d, want 13500", exp)
	}
}

func TestShiftCashBreakdown_RefundWithTipsAndAdjustment(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg-rt','RT Till',1)`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-rt", "reg-rt", "user1", 10000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	scInsertSale(t, dbx, "s-rt", "completed", "reg-rt", 40000, "2026-01-01T10:00:00Z")
	scInsertPayment(t, dbx, "p-rt", "s-rt", "cash", 45000, 3000, 2000, "2026-01-01T10:00:00Z")
	scInsertReturn(t, dbx, "r-rt", "completed", "reg-rt", 1500, "2026-01-01T10:30:00Z")
	scInsertPayment(t, dbx, "pr-rt", "r-rt", "cash", 1500, 0, 0, "2026-01-01T10:30:00Z")
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-rt", "cash_adjustment", map[string]any{"amount": -1500}, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	b, err := dbx.repo.ShiftCashBreakdown(ctx, "shift-rt", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if b.CashSales.Minor() != 38500 || b.TipsHeldOut.Minor() != 2000 || b.Adjustments.Minor() != -1500 || b.Expected.Minor() != 49000 {
		t.Fatalf("breakdown = %+v, want cash 38500 tips 2000 adj -1500 expected 49000", b)
	}
	if sum := b.Opening.Add(b.CashSales).Add(b.TipsHeldOut).Add(b.Adjustments); sum != b.Expected {
		t.Fatalf("identity broken: sum %d != expected %d", sum.Minor(), b.Expected.Minor())
	}
}

// shiftWindowBase is UTC noon today, derived from the clock (never a fixed
// date: guard-fixed-dates.sh). t0-1h is always the same UTC day.
func shiftWindowBase() time.Time {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, time.UTC)
}

// ut-docs#5796 F1: a legacy 'YYYY-MM-DD HH:MM:SS' opened_at must not pull in
// earlier sales of the same UTC day (text compare puts 'T' after ' ').
func TestShiftCashBreakdown_LegacyOpenedAtExcludesEarlierSameDay(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	mustExec(t, dbx.d, `INSERT INTO registers(id,name,is_active) VALUES('reg-a','A',1)`)
	t0 := shiftWindowBase()
	legacy := t0.Format("2006-01-02 15:04:05")
	mustExec(t, dbx.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('L','reg-a','user1',?,0)`, legacy)
	for i, c := range []struct {
		at  time.Time
		amt int64
	}{{t0.Add(-time.Hour), 1000}, {t0.Add(-25 * time.Hour), 2000}, {t0.Add(5 * time.Minute), 300}} {
		id := fmt.Sprintf("lg%d", i)
		scInsertSale(t, dbx, id, "completed", "reg-a", c.amt, b8At(c.at))
		scInsertPayment(t, dbx, "p-"+id, id, "cash", c.amt, 0, 0, b8At(c.at))
	}
	b, err := dbx.repo.ShiftCashBreakdown(ctx, "L", 0)
	if err != nil {
		t.Fatal(err)
	}
	if b.CashSales.Minor() != 300 || b.Expected.Minor() != 300 {
		t.Fatalf("breakdown = %+v, want CashSales 300 Expected 300 (only the sale after opening)", b)
	}
	got, err := dbx.repo.SumCashPaymentsForShift(ctx, "reg-a", nullStr(legacy), sql.NullString{})
	if err != nil || got != 300 {
		t.Fatalf("SumCashPaymentsForShift = %d, %v; want 300", got, err)
	}
}

// A closed_at of ” (Valid but empty) means open, not "bound at ”".
func TestShiftWindow_ClosedEmptyIsOpen(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	mustExec(t, dbx.d, `INSERT INTO registers(id,name,is_active) VALUES('reg-a','A',1)`)
	t0 := shiftWindowBase()
	mustExec(t, dbx.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash) VALUES('E','reg-a','user1',?,'',0)`, b8At(t0))
	scInsertSale(t, dbx, "e1", "completed", "reg-a", 700, b8At(t0.Add(time.Minute)))
	scInsertPayment(t, dbx, "p-e1", "e1", "cash", 700, 0, 0, b8At(t0.Add(time.Minute)))
	b, err := dbx.repo.ShiftCashBreakdown(ctx, "E", 0)
	if err != nil {
		t.Fatal(err)
	}
	if b.CashSales.Minor() != 700 {
		t.Fatalf("CashSales = %d, want 700 (closed_at '' is open)", b.CashSales.Minor())
	}
}
