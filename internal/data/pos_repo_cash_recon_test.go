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

// cashReconAnchor returns a time safely inside one shop-local calendar day
// — host-local noon (time.Now().Local(), not a hardcoded date) keeps every
// same-day instant safely inside its calendar day for any real IANA offset,
// same convention eod_zreport_local_day_869_test.go documents and uses.
//
// ut-docs#1006 review finding 5: the first version of this file hardcoded
// both the seeded timestamps AND the expected day string as fixed UTC
// literals ("2026-03-10"/"2026-03-11"). That's exactly the mistake
// ut-docs#869/#559 already found and fixed elsewhere in this package
// (see eod_zreport_local_day_869_test.go's own doc comment) — it encodes
// the query's OLD (bare-UTC) semantics and can pass against broken code on
// a non-UTC host. Every test below derives its "day" argument via
// b8ExpectedDay's date(?,'localtime') control query against the real DB
// (the SAME production convention CashReconciliationForLocalDay uses),
// never a Go-side literal.
func cashReconAnchor() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
}

func TestCashReconciliationForLocalDay_NoShiftsClosed(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)

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
	if err := dbx.repo.InsertShift(ctx, nil, "shift-open", "reg1", "user1", 5000, b8At(anchor.Add(-3*time.Hour))); err != nil {
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
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	closedAt := b8At(anchor)

	if err := dbx.repo.InsertShift(ctx, nil, "shift1", "reg1", "user1", 10000, b8At(anchor.Add(-3*time.Hour))); err != nil {
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

// Covers two distinct aggregation rules (ut-docs#1006 review finding 3):
// Opening/Counted/Calculated/adjustments are additive across EVERY shift
// closed that day, but NewFloat is not additive across sequential closes
// on the SAME register — a register that closes twice in one day
// physically holds only its LAST close's new float. reg1 closes twice
// (s1 then s1b); reg2 closes once (s2). NewFloat must reflect s1b + s2,
// never s1 + s1b + s2.
func TestCashReconciliationForLocalDay_MultipleShiftsAndAdjustments(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)

	// s1 (reg1): opening 5000, counted 6000 vs expected 5900 (variance
	// +100), pay-in +500 and payout -200 during the shift, no skim;
	// new_float NULL (pre-feature close) -> falls back to closing_cash 6000.
	if err := dbx.repo.InsertShift(ctx, nil, "s1", "reg1", "user1", 5000, b8At(anchor.Add(-6*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
UPDATE shifts SET closed_at = ?, closing_cash = 6000, expected_cash = 5900 WHERE id = 's1'`, b8At(anchor.Add(-4*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s1", "cash_adjustment",
		map[string]any{"shift_id": "s1", "type": "adjustment", "amount": 500, "reason": "float top-up"}, b8At(anchor.Add(-5*time.Hour)), ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s1", "cash_adjustment",
		map[string]any{"shift_id": "s1", "type": "payout", "amount": -200, "reason": "supplies"}, b8At(anchor.Add(-4*time.Hour).Add(-30*time.Minute)), ""); err != nil {
		t.Fatal(err)
	}

	// s1b: SAME register (reg1), closes LATER the same day — its new_float
	// (600) is the drawer's actual carry-forward, not s1's 6000.
	if err := dbx.repo.InsertShift(ctx, nil, "s1b", "reg1", "user1", 6000, b8At(anchor.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "s1b", 1000, 1000, 600, "", "", b8At(anchor.Add(-1*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s1b", "cash_adjustment",
		map[string]any{"shift_id": "s1b", "type": "skim", "amount": -400, "reason": "skim to safe"}, b8At(anchor.Add(-1*time.Hour)), ""); err != nil {
		t.Fatal(err)
	}

	// s2 (reg2): a different register, opening 2000, counted 3000 =
	// expected, skim -2500, new float 500 — additive alongside s1b, not
	// instead of it. reg2 isn't part of newPOSLifecycleTestDB's seed set
	// (only reg1), so create it here.
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "s2", "reg2", "user2", 2000, b8At(anchor.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "s2", 3000, 3000, 500, "", "", b8At(anchor)); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user2", "shift", "s2", "cash_adjustment",
		map[string]any{"shift_id": "s2", "type": "skim", "amount": -2500, "reason": "skim to safe"}, b8At(anchor), ""); err != nil {
		t.Fatal(err)
	}

	// A shift closed on a DIFFERENT day (and its adjustment) must not count.
	otherDay := anchor.AddDate(0, 0, 1)
	if err := dbx.repo.InsertShift(ctx, nil, "s-other", "reg1", "user1", 9999, b8At(otherDay.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "s-other", 9999, 9999, 9999, "", "", b8At(otherDay)); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "s-other", "cash_adjustment",
		map[string]any{"shift_id": "s-other", "type": "payout", "amount": -7777, "reason": "other day"}, b8At(otherDay), ""); err != nil {
		t.Fatal(err)
	}

	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a reconciliation")
	}
	if rec.ShiftsClosed != 3 {
		t.Errorf("ShiftsClosed: want 3, got %d", rec.ShiftsClosed)
	}
	if rec.OpeningFloat != 5000+6000+2000 {
		t.Errorf("OpeningFloat: want 13000, got %d", rec.OpeningFloat)
	}
	if rec.Counted != 6000+1000+3000 {
		t.Errorf("Counted: want 10000, got %d", rec.Counted)
	}
	if rec.Calculated != 5900+1000+3000 {
		t.Errorf("Calculated: want 9900, got %d", rec.Calculated)
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
	if rec.Skim != -400+-2500 {
		t.Errorf("Skim: want -2900, got %d", rec.Skim)
	}
	// reg1's LAST close that day is s1b (new_float 600) — s1's 6000 must
	// NOT be added on top of it. reg2's only close (s2) adds 500.
	if rec.NewFloat != 600+500 {
		t.Errorf("NewFloat: want 1100 (s1b's 600 + s2's 500, NOT s1+s1b+s2), got %d", rec.NewFloat)
	}
}

// EndOfDay (single-day Z-report) carries the reconciliation, including the
// day's net cash sales from the payment-method breakdown; the multi-day
// range report deliberately does not (a summed multi-day reconciliation
// would be misleading, same from==to gate as Departments/Tills).
func TestEndOfDay_PopulatesCashReconciliation(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	closedAt := b8At(anchor)

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

	if err := dbx.repo.InsertShift(ctx, nil, "shift-cr", "reg1", "user1", 10000, b8At(anchor.Add(-3*time.Hour))); err != nil {
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
	rangeRep, err := dbx.repo.EndOfDayRange(ctx, day, anchor.AddDate(0, 0, 1).Format("2006-01-02"))
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
	anchor := cashReconAnchor()

	// No closed shift for the register.
	if _, ok, err := dbx.repo.LastClosedShiftCarryForward(ctx, "reg1"); err != nil || ok {
		t.Fatalf("expected no carry-forward, got ok=%v err=%v", ok, err)
	}

	// Two closes: the LATER one wins.
	if err := dbx.repo.InsertShift(ctx, nil, "cf1", "reg1", "user1", 1000, b8At(anchor.AddDate(0, 0, -1).Add(-4*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "cf1", 2000, 2000, 700, "", "", b8At(anchor.AddDate(0, 0, -1).Add(5*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "cf2", "reg1", "user1", 700, b8At(anchor.Add(-4*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "cf2", 5000, 5000, 1500, "", "", b8At(anchor.Add(5*time.Hour))); err != nil {
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
