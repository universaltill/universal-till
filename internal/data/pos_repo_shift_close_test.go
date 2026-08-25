package data

import (
	"context"
	"database/sql"
	"testing"
)

// This file covers the shift open/close write path and the expected-cash
// reconciliation math (opening + cash sales - refunds + manual adjustments)
// that end-of-shift/end-of-day cash counts are checked against — getting
// this wrong means a cashier's till "doesn't balance" for no real reason,
// or worse, a genuine shortage goes unflagged.

func TestFindOpenShiftForRegisterAndInsertShift(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if id, err := dbx.repo.FindOpenShiftForRegister(ctx, nil, "reg1"); err != nil || id != "" {
		t.Fatalf("expected no open shift yet, got id=%q err=%v", id, err)
	}

	if err := dbx.repo.InsertShift(ctx, nil, "shift1", "reg1", "user1", 5000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	id, err := dbx.repo.FindOpenShiftForRegister(ctx, nil, "reg1")
	if err != nil || id != "shift1" {
		t.Fatalf("expected shift1 open on reg1, got id=%q err=%v", id, err)
	}

	// A different register has no open shift of its own.
	if id, err := dbx.repo.FindOpenShiftForRegister(ctx, nil, "reg-other"); err != nil || id != "" {
		t.Fatalf("expected no open shift on a different register, got id=%q err=%v", id, err)
	}
}

func TestShiftOpenExistsAndLoadShiftForClose(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if err := dbx.repo.InsertShift(ctx, nil, "shift1", "reg1", "user1", 5000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	if open, err := dbx.repo.ShiftOpenExists(ctx, nil, "shift1"); err != nil || !open {
		t.Fatalf("expected shift1 to be open, got open=%v err=%v", open, err)
	}
	if open, err := dbx.repo.ShiftOpenExists(ctx, nil, "missing"); err != nil || open {
		t.Fatalf("expected no open shift for an unknown id, got open=%v err=%v", open, err)
	}

	regID, cashierID, opening, err := dbx.repo.LoadShiftForClose(ctx, nil, "shift1")
	if err != nil {
		t.Fatal(err)
	}
	if regID != "reg1" || cashierID != "user1" || opening != 5000 {
		t.Fatalf("unexpected shift-for-close data: reg=%q cashier=%q opening=%d", regID, cashierID, opening)
	}

	// Close it, then LoadShiftForClose must refuse to load it again — an
	// already-closed shift can't be closed twice.
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift1", 7000, 7000, 7000, "end of day", "", "2026-01-01T17:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := dbx.repo.LoadShiftForClose(ctx, nil, "shift1"); err == nil {
		t.Fatal("expected an error loading an already-closed shift for close")
	}
	if open, err := dbx.repo.ShiftOpenExists(ctx, nil, "shift1"); err != nil || open {
		t.Fatalf("expected shift1 to no longer be open, got open=%v err=%v", open, err)
	}

	// LoadShiftForClose on a never-existed id.
	if _, _, _, err := dbx.repo.LoadShiftForClose(ctx, nil, "never-existed"); err == nil {
		t.Fatal("expected an error for an unknown shift id")
	}
}

func TestSumCashPaymentsForShift_WindowedByOpenAndCloseTime(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg-shift','Shift Till',1)`); err != nil {
		t.Fatal(err)
	}

	seed := func(id, receipt, methodID string, amount int64, completedAt string) {
		if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', ?, 0, 0, ?, 'reg-shift', ?, ?)`, id, receipt, amount, amount, completedAt, completedAt); err != nil {
			t.Fatalf("seed sale %s: %v", id, err)
		}
		if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES(?,?,?,?,'GBP',0,?)`,
			id+"-pay", id, methodID, amount, completedAt); err != nil {
			t.Fatalf("seed payment for %s: %v", id, err)
		}
	}
	// Inside the shift window.
	seed("during1", "R1", "cash", 1000, "2026-01-01T10:00:00Z")
	// A card payment inside the window must NOT count as cash.
	seed("during2", "R2", "card", 5000, "2026-01-01T11:00:00Z")
	// Before the shift opened.
	seed("before1", "R3", "cash", 2000, "2026-01-01T08:00:00Z")
	// After the shift closed.
	seed("after1", "R4", "cash", 3000, "2026-01-01T18:00:00Z")

	opened := sql.NullString{String: "2026-01-01T09:00:00Z", Valid: true}
	closed := sql.NullString{String: "2026-01-01T17:00:00Z", Valid: true}
	total, err := dbx.repo.SumCashPaymentsForShift(ctx, "reg-shift", opened, closed)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000 {
		t.Fatalf("expected only the in-window cash payment (1000), got %d", total)
	}

	// A still-open shift (closedAt invalid/NULL) must include everything
	// from opened_at onward, with no upper bound.
	openOnly := sql.NullString{}
	total, err = dbx.repo.SumCashPaymentsForShift(ctx, "reg-shift", opened, openOnly)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000+3000 {
		t.Fatalf("expected both in-window and after-close-time cash payments for a still-open shift (4000), got %d", total)
	}
}

func TestSumShiftAdjustments(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if err := dbx.repo.InsertShift(ctx, nil, "shift1", "reg1", "user1", 5000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	// A positive cash-in adjustment and a negative cash-out (paid-out) one.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift1", "cash_adjustment", map[string]any{"amount": 500}, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift1", "cash_adjustment", map[string]any{"amount": -200}, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	// A different action type on the same shift must not be summed in.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift1", "note", map[string]any{"amount": 9999}, "2026-01-01T12:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	total, err := dbx.repo.SumShiftAdjustments(ctx, "shift1")
	if err != nil {
		t.Fatal(err)
	}
	if total != 300 {
		t.Fatalf("expected net adjustment 500-200=300, got %d", total)
	}
}

func TestComputeExpectedCash(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg-ec','EC Till',1)`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertShift(ctx, nil, "shift-ec", "reg-ec", "user1", 5000, "2026-01-01T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES('sale-ec', 'R-EC', 'completed', 'sale', 'GBP', 1000, 0, 0, 1000, 'reg-ec', '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-ec','sale-ec','cash',1000,'GBP',0,'2026-01-01T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "shift-ec", "cash_adjustment", map[string]any{"amount": -150}, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	expected, err := dbx.repo.ComputeExpectedCash(ctx, "shift-ec", 5000)
	if err != nil {
		t.Fatal(err)
	}
	// opening 5000 + cash sales 1000 + adjustment -150 = 5850.
	if expected != 5850 {
		t.Fatalf("expected 5000+1000-150=5850, got %d", expected)
	}

	if _, err := dbx.repo.ComputeExpectedCash(ctx, "", 5000); err == nil {
		t.Fatal("expected an error for an empty shift id")
	}
}
