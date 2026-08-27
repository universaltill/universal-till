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
	// at_close: true marks this as the real CloseShift skim-to-safe path
	// (ut-docs#1146) — without it, this row would fall into PayOuts
	// instead of Skim, same as a genuine mid-shift skim.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift1", "cash_adjustment",
		map[string]any{"shift_id": "shift1", "type": "skim", "amount": -41110, "reason": "skim to safe", "at_close": true}, closedAt, ""); err != nil {
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
		map[string]any{"shift_id": "s1b", "type": "skim", "amount": -400, "reason": "skim to safe", "at_close": true}, b8At(anchor.Add(-1*time.Hour)), ""); err != nil {
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
		map[string]any{"shift_id": "s2", "type": "skim", "amount": -2500, "reason": "skim to safe", "at_close": true}, b8At(anchor), ""); err != nil {
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

// A skim recorded WHILE THE SHIFT IS STILL OPEN (ut-docs#1146, via
// pos.RecordCashAdjustment — not reachable through the shipped UI, but valid
// at the API layer per TestRecordCashAdjustment_SkimType) already sits in
// audit_log by close time, so SumShiftAdjustments/ComputeExpectedCash nets it
// into Calculated the same as any other mid-shift adjustment. Before this
// fix, CashReconciliationForLocalDay bucketed EVERY type='skim' row into
// rec.Skim regardless of when it was written, excluding it from
// PayIns/PayOuts — so the printed identity (OpeningFloat + CashSales +
// TipsHeldOut + PayIns + PayOuts == Calculated) broke by exactly the
// mid-shift skim amount. The fix distinguishes a close-time skim by an
// explicit `at_close: true` marker pos.CloseShift stamps on the row it
// writes (ut-docs#1146 review finding F1 — an earlier version of this fix
// compared created_at to closed_at instead, but that string match is only
// second-precision and could misclassify a mid-shift skim landing in the
// same wall-clock second as its own shift's close; see
// TestCashReconciliationForLocalDay_MidShiftSkimSameSecondAsClose below for
// that exact scenario). Only a row carrying the flag lands in Skim; every
// other skim row now falls into PayOuts like any other negative adjustment,
// keeping the identity whole. shift-mid-skim has NO close-time skim at all,
// so rec.Skim must be zero and the entire -3000 lands in PayOuts.
func TestCashReconciliationForLocalDay_MidShiftSkimIncludedInPayOuts(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	openedAt := b8At(anchor.Add(-3 * time.Hour))
	midShiftAt := b8At(anchor.Add(-2 * time.Hour)) // well before closedAt
	closedAt := b8At(anchor)

	if err := dbx.repo.InsertShift(ctx, nil, "shift-mid-skim", "reg1", "user1", 10000, openedAt); err != nil {
		t.Fatal(err)
	}
	// Mid-shift skim of -30.00, recorded while the shift was still open —
	// no at_close marker, unlike the close-time skim fixture in
	// TestCashReconciliationForLocalDay_ReferenceFigures.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-mid-skim", "cash_adjustment",
		map[string]any{"shift_id": "shift-mid-skim", "type": "skim", "amount": -3000, "reason": "midday skim"}, midShiftAt, ""); err != nil {
		t.Fatal(err)
	}
	// No cash sales in this fixture, so ComputeExpectedCash = opening
	// (10000) + adjustments (-3000) = 7000 — the mid-shift skim is already
	// netted in, same as pos.CloseShift's own real call sequence.
	expectedCash, err := dbx.repo.ComputeExpectedCash(ctx, "shift-mid-skim", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if expectedCash != 7000 {
		t.Fatalf("ComputeExpectedCash: want 7000 (10000 opening - 3000 mid-shift skim), got %d", expectedCash)
	}
	// Close with no further skim: counted matches expected (variance 0),
	// so new_float falls back to closing_cash (ut-docs#1146 review finding
	// F2 — a real CloseShift can never produce new_float > closing_cash,
	// since new_float = closing_cash - skim and skim is always >= 0).
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-mid-skim", expectedCash, expectedCash, expectedCash, "", "", closedAt); err != nil {
		t.Fatal(err)
	}

	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a reconciliation")
	}
	if rec.Skim != 0 {
		t.Errorf("Skim: want 0 (no close-time skim on this shift), got %d", rec.Skim)
	}
	if rec.PayOuts != -3000 {
		t.Errorf("PayOuts: want -3000 (the mid-shift skim), got %d", rec.PayOuts)
	}
	if rec.PayIns != 0 {
		t.Errorf("PayIns: want 0, got %d", rec.PayIns)
	}
	if rec.Calculated != 7000 {
		t.Errorf("Calculated: want 7000, got %d", rec.Calculated)
	}
	if sum := rec.OpeningFloat + rec.CashSales + rec.TipsHeldOut + rec.PayIns + rec.PayOuts; sum != rec.Calculated {
		t.Errorf("reconciliation identity broken: OpeningFloat(%d)+CashSales(%d)+TipsHeldOut(%d)+PayIns(%d)+PayOuts(%d) = %d, want Calculated %d",
			rec.OpeningFloat, rec.CashSales, rec.TipsHeldOut, rec.PayIns, rec.PayOuts, sum, rec.Calculated)
	}
}

// A shift can carry BOTH a mid-shift skim and a close-time skim — the two
// must not be conflated: only the close-time one (the row carrying
// at_close: true) counts toward Skim, and only the mid-shift one (no flag)
// falls into PayOuts.
func TestCashReconciliationForLocalDay_MidShiftAndCloseTimeSkimBothPresent(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	openedAt := b8At(anchor.Add(-3 * time.Hour))
	midShiftAt := b8At(anchor.Add(-2 * time.Hour))
	closedAt := b8At(anchor)

	if err := dbx.repo.InsertShift(ctx, nil, "shift-both-skims", "reg1", "user1", 10000, openedAt); err != nil {
		t.Fatal(err)
	}
	// Mid-shift skim of -20.00 while open.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-both-skims", "cash_adjustment",
		map[string]any{"shift_id": "shift-both-skims", "type": "skim", "amount": -2000, "reason": "midday skim"}, midShiftAt, ""); err != nil {
		t.Fatal(err)
	}
	// expected_cash computed BEFORE the close-time skim below is written,
	// same call order pos.CloseShift itself uses.
	expectedCash, err := dbx.repo.ComputeExpectedCash(ctx, "shift-both-skims", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if expectedCash != 8000 {
		t.Fatalf("ComputeExpectedCash: want 8000 (10000 - 2000 mid-shift skim), got %d", expectedCash)
	}
	// Close counting 8000 (matches expected, so variance 0), skimming a
	// further -50.00 to the safe at close — new_float = 8000 - 5000 = 3000.
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-both-skims", 8000, expectedCash, 3000, "", "", closedAt); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-both-skims", "cash_adjustment",
		map[string]any{"shift_id": "shift-both-skims", "type": "skim", "amount": -5000, "reason": "close skim to safe", "at_close": true}, closedAt, ""); err != nil {
		t.Fatal(err)
	}

	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a reconciliation")
	}
	if rec.Skim != -5000 {
		t.Errorf("Skim: want -5000 (only the close-time skim), got %d", rec.Skim)
	}
	if rec.PayOuts != -2000 {
		t.Errorf("PayOuts: want -2000 (only the mid-shift skim), got %d", rec.PayOuts)
	}
	if rec.Calculated != 8000 {
		t.Errorf("Calculated: want 8000, got %d", rec.Calculated)
	}
	if sum := rec.OpeningFloat + rec.CashSales + rec.TipsHeldOut + rec.PayIns + rec.PayOuts; sum != rec.Calculated {
		t.Errorf("reconciliation identity broken: OpeningFloat(%d)+CashSales(%d)+TipsHeldOut(%d)+PayIns(%d)+PayOuts(%d) = %d, want Calculated %d",
			rec.OpeningFloat, rec.CashSales, rec.TipsHeldOut, rec.PayIns, rec.PayOuts, sum, rec.Calculated)
	}
}

// The exact race ut-docs#1146 review finding F1 identified: a mid-shift skim
// landing in the SAME wall-clock second as its own shift's close. An earlier
// version of this fix identified a close-time skim by comparing the skim
// row's created_at to the shift's closed_at — both stamped from pos.
// CloseShift's own `now` in one transaction, so a real close-time skim's
// created_at always exactly equals closed_at. But time.RFC3339 (what both
// pos.CloseShift and pos.RecordCashAdjustment format `now` with) is only
// second-precision, so a mid-shift skim recorded in that same second would
// ALSO have created_at == closed_at by pure coincidence — misclassifying it
// as the close-time skim and reproducing the #1146 bug verbatim. The fixed
// query never compares timestamps at all: only pos.CloseShift's own skim row
// carries at_close: true, and pos.RecordCashAdjustment never sets it, so
// this mid-shift skim is unambiguous regardless of timing — same-second or
// not — and must still land in PayOuts, not Skim.
func TestCashReconciliationForLocalDay_MidShiftSkimSameSecondAsClose(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	openedAt := b8At(anchor.Add(-3 * time.Hour))
	closedAt := b8At(anchor)

	if err := dbx.repo.InsertShift(ctx, nil, "shift-same-second", "reg1", "user1", 10000, openedAt); err != nil {
		t.Fatal(err)
	}
	// Mid-shift skim of -30.00 recorded at the EXACT same created_at string
	// as the close below — no at_close marker, since RecordCashAdjustment
	// never sets it, regardless of timing.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-same-second", "cash_adjustment",
		map[string]any{"shift_id": "shift-same-second", "type": "skim", "amount": -3000, "reason": "midday skim"}, closedAt, ""); err != nil {
		t.Fatal(err)
	}
	expectedCash, err := dbx.repo.ComputeExpectedCash(ctx, "shift-same-second", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if expectedCash != 7000 {
		t.Fatalf("ComputeExpectedCash: want 7000 (10000 opening - 3000 mid-shift skim), got %d", expectedCash)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-same-second", expectedCash, expectedCash, expectedCash, "", "", closedAt); err != nil {
		t.Fatal(err)
	}

	rec, err := dbx.repo.CashReconciliationForLocalDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a reconciliation")
	}
	if rec.Skim != 0 {
		t.Errorf("Skim: want 0 — a same-second mid-shift skim must not be misread as the close-time skim, got %d", rec.Skim)
	}
	if rec.PayOuts != -3000 {
		t.Errorf("PayOuts: want -3000 (the same-second mid-shift skim), got %d", rec.PayOuts)
	}
	if sum := rec.OpeningFloat + rec.CashSales + rec.TipsHeldOut + rec.PayIns + rec.PayOuts; sum != rec.Calculated {
		t.Errorf("reconciliation identity broken: OpeningFloat(%d)+CashSales(%d)+TipsHeldOut(%d)+PayIns(%d)+PayOuts(%d) = %d, want Calculated %d",
			rec.OpeningFloat, rec.CashSales, rec.TipsHeldOut, rec.PayIns, rec.PayOuts, sum, rec.Calculated)
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

// A cash payment carrying a tip (ut-docs#1046): CashSales must hold the tip
// out, the same way ut-docs#1007 already holds every method's tips out of
// revenue, rather than letting it inflate the drawer's expected cash. Cash
// tipping is off in the till UI today, but nothing at the pos.CompleteSale
// validation layer rejects a cash MethodID carrying TipAmount (only
// negative tips and voucher-redemption payments are rejected), so this must
// hold correct rather than rely on an invariant the API doesn't actually
// enforce.
func TestEndOfDay_CashReconciliation_ExcludesCashTips(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	closedAt := b8At(anchor)

	// One cash sale of 400.00 tendered with a 20.00 tip riding the cash
	// payment (amount 420.00 = 400.00 sale + 20.00 tip, same
	// amount-already-includes-tip convention InsertPayment/EODTip's own
	// doc comment establish for every payment method).
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES('sale-cr-tip', 'R-CR-TIP', 'completed', 'sale', 'GBP', 40000, 0, 0, 40000, 'reg1', ?, ?)`, closedAt, closedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at)
VALUES('pay-cr-tip', 'sale-cr-tip', 'cash', 42000, 'GBP', 0, 2000, ?)`, closedAt); err != nil {
		t.Fatal(err)
	}

	if err := dbx.repo.InsertShift(ctx, nil, "shift-cr-tip", "reg1", "user1", 10000, b8At(anchor.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	// Calculated must come from the real ComputeExpectedCash path (opening
	// float + the tip-inclusive tendered cash, same as pos.CloseShift calls
	// at close time), not a hand-picked literal — a hardcoded expected_cash
	// here would let CashSales/TipsHeldOut drift out of sync with Calculated
	// without this test noticing (ut-docs#1124).
	expectedCash, err := dbx.repo.ComputeExpectedCash(ctx, "shift-cr-tip", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if expectedCash != 52000 {
		t.Fatalf("ComputeExpectedCash: want 52000 (10000 float + 42000 tip-inclusive cash), got %d", expectedCash)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-cr-tip", expectedCash, expectedCash, 10000, "", "", closedAt); err != nil {
		t.Fatal(err)
	}

	rep, err := dbx.repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CashReconciliation == nil {
		t.Fatal("expected a cash reconciliation")
	}
	rc := rep.CashReconciliation
	// EODMethod{cash}.In is the full tendered 420.00 (sale + tip); CashSales
	// must hold the 20.00 tip back out, leaving 400.00.
	if rc.CashSales != 40000 {
		t.Errorf("CashSales: want 40000 (tip held out), got %d", rc.CashSales)
	}
	if rc.TipsHeldOut != 2000 {
		t.Errorf("TipsHeldOut: want 2000, got %d", rc.TipsHeldOut)
	}
	// ut-docs#1124: the printed CASH RECONCILIATION block's own visible line
	// items -- opening float, cash sales, tips held out, pay-ins, pay-outs --
	// must sum to Calculated once a cash tip is held out, the same identity
	// eod_api.go's buildEODDoc prints top-to-bottom. A regression that held
	// tips out of CashSales without also accounting for them in Calculated
	// -- or vice versa -- would break this while each field's own value
	// still looked individually plausible. This shift records no skim at
	// all, so it does NOT cover the separate mid-shift-skim case (ut-docs#1146)
	// -- see TestCashReconciliationForLocalDay_MidShiftSkimIncludedInPayOuts
	// and its close-time-skim-too sibling for that fixture.
	if sum := rc.OpeningFloat + rc.CashSales + rc.TipsHeldOut + rc.PayIns + rc.PayOuts; sum != rc.Calculated {
		t.Errorf("reconciliation identity broken: OpeningFloat(%d)+CashSales(%d)+TipsHeldOut(%d)+PayIns(%d)+PayOuts(%d) = %d, want Calculated %d",
			rc.OpeningFloat, rc.CashSales, rc.TipsHeldOut, rc.PayIns, rc.PayOuts, sum, rc.Calculated)
	}
	// The report's own Tips breakdown must show the same cash-tip figure
	// CashReconciliation subtracted -- the two are read from the same
	// underlying data, not permitted to disagree.
	found := false
	for _, tp := range rep.Tips {
		if tp.Method == "cash" {
			found = true
			if tp.Amount != 2000 {
				t.Errorf("Tips[cash].Amount: want 2000, got %d", tp.Amount)
			}
		}
	}
	if !found {
		t.Error("expected a cash entry in rep.Tips")
	}
}

// A day with no cash tips must show TipsHeldOut as the zero value and never
// touch CashSales -- the ordinary case, covered separately from the
// tip-bearing one above so a regression that always subtracts something
// can't hide behind the happy-path totals matching by coincidence.
func TestEndOfDay_CashReconciliation_ZeroTipsHeldOutWhenNoCashTip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	closedAt := b8At(anchor)

	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES('sale-cr-notip', 'R-CR-NOTIP', 'completed', 'sale', 'GBP', 41110, 0, 0, 41110, 'reg1', ?, ?)`, closedAt, closedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES('pay-cr-notip', 'sale-cr-notip', 'cash', 41110, 'GBP', 0, ?)`, closedAt); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-cr-notip", "reg1", "user1", 10000, b8At(anchor.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-cr-notip", 51110, 51110, 10000, "", "", closedAt); err != nil {
		t.Fatal(err)
	}

	rep, err := dbx.repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CashReconciliation == nil {
		t.Fatal("expected a cash reconciliation")
	}
	if rep.CashReconciliation.CashSales != 41110 {
		t.Errorf("CashSales: want 41110, got %d", rep.CashReconciliation.CashSales)
	}
	if rep.CashReconciliation.TipsHeldOut != 0 {
		t.Errorf("TipsHeldOut: want 0, got %d", rep.CashReconciliation.TipsHeldOut)
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
