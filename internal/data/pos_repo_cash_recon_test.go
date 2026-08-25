package data

import (
	"context"
	"testing"
	"time"
)

// Day-level cash-drawer reconciliation for the EOD Z-report (ut-docs#1006):
// sums every shift CLOSED on the shop-local day — opening floats, counted
// vs calculated cash, variance, skim-to-safe, and the carried-forward new
// float — plus the carry-forward lookup the next shift-open defaults from.

// cashReconDay returns a closed_at timestamp safely inside one shop-local
// calendar day (midday UTC keeps the local date identical for any sane TZ
// offset) and the matching local "YYYY-MM-DD" day string, per ADR-0057:
// aggregation is on date(closed_at,'localtime').
func cashReconDay(t *testing.T) (string, string) {
	t.Helper()
	at := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return at.Format(time.RFC3339), at.In(time.Local).Format("2006-01-02")
}

func TestCashReconciliationForLocalDay_NoShiftsClosed(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, day := cashReconDay(t)

	// Zero shifts closed that day must be nil, nil — never an error: the
	// day-close report still generates on a day nobody closed a shift.
	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec != nil {
		t.Fatalf("expected nil reconciliation with no closed shifts, got %+v", rec)
	}

	// A still-open shift doesn't count either.
	if err := dbx.repo.InsertShift(ctx, nil, "shift-open", "reg1", "user1", 5000, "2026-03-10T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rec, err = dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil || rec != nil {
		t.Fatalf("open shift must not produce a reconciliation, got %+v err=%v", rec, err)
	}
}

// The de-identified reference figures from the parent epic (ut-docs#1002):
// opening 100.00, cash takings 411.10, calculated 511.10, counted 511.10,
// variance 0.00, skim -411.10, new float 100.00.
func TestCashReconciliationForLocalDay_ReferenceFigures(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	closedAt, day := cashReconDay(t)

	if err := dbx.repo.InsertShift(ctx, nil, "shift1", "reg1", "user1", 10000, "2026-03-10T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift1", 51110, 51110, 10000, "", `{"5000":10,"100":11,"10":1}`, closedAt); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift1", "cash_adjustment",
		map[string]any{"shift_id": "shift1", "type": "skim", "amount": -41110, "reason": "skim to safe"}, closedAt, ""); err != nil {
		t.Fatal(err)
	}

	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a reconciliation")
	}
	if rec.OpeningFloat != 10000 {
		t.Errorf("OpeningFloat: want 10000, got %d", rec.OpeningFloat)
	}
	if rec.Calculated != 51110 {
		t.Errorf("Calculated: want 51110, got %d", rec.Calculated)
	}
	if rec.Counted != 51110 {
		t.Errorf("Counted: want 51110, got %d", rec.Counted)
	}
	if rec.Variance != 0 {
		t.Errorf("Variance: want 0, got %d", rec.Variance)
	}
	if rec.Skim != -41110 {
		t.Errorf("Skim: want -41110, got %d", rec.Skim)
	}
	if rec.NewFloat != 10000 {
		t.Errorf("NewFloat: want 10000, got %d", rec.NewFloat)
	}
	if rec.PayIns != 0 || rec.PayOuts != 0 {
		t.Errorf("PayIns/PayOuts: want 0/0, got %d/%d", rec.PayIns, rec.PayOuts)
	}
	if rec.ShiftsClosed != 1 {
		t.Errorf("ShiftsClosed: want 1, got %d", rec.ShiftsClosed)
	}
}

func TestCashReconciliationForLocalDay_MultipleShiftsAndAdjustments(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	closedAt, day := cashReconDay(t)

	// Shift 1: opening 5000, counted 6000 vs expected 5900 (variance +100),
	// pay-in +500 and payout -200 during the shift, no skim; new_float NULL
	// (pre-feature close) → falls back to closing_cash 6000.
	if err := dbx.repo.InsertShift(ctx, nil, "s1", "reg1", "user1", 5000, "2026-03-10T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
UPDATE shifts SET closed_at = ?, closing_cash = 6000, expected_cash = 5900 WHERE id = 's1'`, closedAt); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s1", "cash_adjustment",
		map[string]any{"shift_id": "s1", "type": "adjustment", "amount": 500, "reason": "float top-up"}, "2026-03-10T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s1", "cash_adjustment",
		map[string]any{"shift_id": "s1", "type": "payout", "amount": -200, "reason": "supplies"}, "2026-03-10T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	// Shift 2: opening 2000, counted 3000 = expected, skim -2500, new float 500.
	if err := dbx.repo.InsertShift(ctx, nil, "s2", "reg1", "user2", 2000, "2026-03-10T12:30:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "s2", 3000, 3000, 500, "", "", "2026-03-10T14:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user2", "shift", "s2", "cash_adjustment",
		map[string]any{"shift_id": "s2", "type": "skim", "amount": -2500, "reason": "skim to safe"}, "2026-03-10T14:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	// A shift closed on a DIFFERENT day (and its adjustment) must not count.
	if err := dbx.repo.InsertShift(ctx, nil, "s-other", "reg1", "user1", 9999, "2026-03-11T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "s-other", 9999, 9999, 9999, "", "", "2026-03-11T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s-other", "cash_adjustment",
		map[string]any{"shift_id": "s-other", "type": "payout", "amount": -7777, "reason": "other day"}, "2026-03-11T12:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a reconciliation")
	}
	if rec.ShiftsClosed != 2 {
		t.Errorf("ShiftsClosed: want 2, got %d", rec.ShiftsClosed)
	}
	if rec.OpeningFloat != 5000+2000 {
		t.Errorf("OpeningFloat: want 7000, got %d", rec.OpeningFloat)
	}
	if rec.Counted != 6000+3000 {
		t.Errorf("Counted: want 9000, got %d", rec.Counted)
	}
	if rec.Calculated != 5900+3000 {
		t.Errorf("Calculated: want 8900, got %d", rec.Calculated)
	}
	if rec.Variance != 100 {
		t.Errorf("Variance: want +100, got %d", rec.Variance)
	}
	if rec.PayIns != 500 {
		t.Errorf("PayIns: want 500, got %d", rec.PayIns)
	}
	if rec.PayOuts != -200 {
		t.Errorf("PayOuts: want -200, got %d", rec.PayOuts)
	}
	if rec.Skim != -2500 {
		t.Errorf("Skim: want -2500, got %d", rec.Skim)
	}
	// s1 has no new_float (pre-feature) → closing_cash 6000; s2 → 500.
	if rec.NewFloat != 6000+500 {
		t.Errorf("NewFloat: want 6500, got %d", rec.NewFloat)
	}
}

// EndOfDay (single-day Z-report) carries the reconciliation, including the
// day's net cash sales from the payment-method breakdown; the multi-day
// range report deliberately does not (a summed multi-day reconciliation
// would be misleading, same from==to gate as Departments/Tills).
func TestEndOfDay_PopulatesCashReconciliation(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	closedAt, day := cashReconDay(t)

	// One cash sale of 411.10 on the day.
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES('sale-cr', 'R-CR', 'completed', 'sale', 'GBP', 41110, 0, 0, 41110, 'reg1', ?, ?)`, closedAt, closedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES('pay-cr', 'sale-cr', 'cash', 41110, 'GBP', 0, ?)`, closedAt); err != nil {
		t.Fatal(err)
	}

	if err := dbx.repo.InsertShift(ctx, nil, "shift-cr", "reg1", "user1", 10000, "2026-03-10T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-cr", 51110, 51110, 10000, "", "", closedAt); err != nil {
		t.Fatal(err)
	}

	rep, err := dbx.repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CashReconciliation == nil {
		t.Fatal("EndOfDay must carry the cash reconciliation when a shift closed that day")
	}
	if rep.CashReconciliation.CashSales != 41110 {
		t.Errorf("CashSales: want 41110, got %d", rep.CashReconciliation.CashSales)
	}
	if rep.CashReconciliation.Counted != 51110 {
		t.Errorf("Counted: want 51110, got %d", rep.CashReconciliation.Counted)
	}

	// Multi-day range: reconciliation stays nil.
	rangeRep, err := dbx.repo.EndOfDayRange(ctx, day, "2026-03-11")
	if err != nil {
		t.Fatal(err)
	}
	if rangeRep.CashReconciliation != nil {
		t.Error("EndOfDayRange must not populate a cash reconciliation")
	}
}

func TestLastClosedShiftCarryForward(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// No closed shift for the register.
	if _, ok, err := dbx.repo.LastClosedShiftCarryForward(ctx, "reg1"); err != nil || ok {
		t.Fatalf("expected no carry-forward, got ok=%v err=%v", ok, err)
	}

	// Two closes: the LATER one wins.
	if err := dbx.repo.InsertShift(ctx, nil, "cf1", "reg1", "user1", 1000, "2026-03-09T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "cf1", 2000, 2000, 700, "", "", "2026-03-09T17:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "cf2", "reg1", "user1", 700, "2026-03-10T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "cf2", 5000, 5000, 1500, "", "", "2026-03-10T17:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := dbx.repo.LastClosedShiftCarryForward(ctx, "reg1")
	if err != nil || !ok {
		t.Fatalf("expected carry-forward, got ok=%v err=%v", ok, err)
	}
	if got != 1500 {
		t.Errorf("carry-forward: want latest close's new_float 1500, got %d", got)
	}
}
