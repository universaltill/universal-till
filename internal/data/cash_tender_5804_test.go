package data

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// ut-docs#5804: one definition of a cash payment — the method's
// payment_methods.type = 'cash' — on shift close, the Z/X day-end report
// (both paths) and the cloud sales aggregate.

type cashTenderFixture struct {
	dbx    *posTestDB
	anchor time.Time
	day    string
	shift  string
}

// seedCashTender5804 builds one local day: sale A 10.00 cash, sale B 5.00 via
// till-cash-2 (type cash, 1.00 tip), a 2.00 return via till-cash-2, a 7.00 card
// sale and a 4.00 sale on cashback-voucher (id looks like cash, type voucher),
// plus one closed shift on reg1.
func seedCashTender5804(t *testing.T) cashTenderFixture {
	t.Helper()
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := cashReconAnchor()
	day := b8ExpectedDay(t, dbx.d, anchor, 0, 0)
	at := b8At(anchor)

	mustExec(t, dbx.d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES
('till-cash-2', 'Second Drawer', 'cash', 1, 7),
('cashback-voucher', 'Cashback Voucher', 'voucher', 1, 8)`)

	type sale struct {
		id, typ, method string
		amount, tip     int64
	}
	for _, s := range []sale{
		{"s5804-a", "sale", "cash", 1000, 0},
		{"s5804-b", "sale", "till-cash-2", 500, 100},
		{"s5804-r", "return", "till-cash-2", 200, 0},
		{"s5804-c", "sale", "card", 700, 0},
		{"s5804-v", "sale", "cashback-voucher", 400, 0},
	} {
		mustExec(t, dbx.d, `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at, local_date)
VALUES(?, ?, 'completed', ?, 'GBP', ?, 0, 0, ?, 'reg1', ?, ?, date(?, 'localtime'))`,
			s.id, "R-"+s.id, s.typ, s.amount, s.amount, at, at, at)
		mustExec(t, dbx.d, `
INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at)
VALUES(?, ?, ?, ?, 'GBP', 0, ?, ?)`, "p-"+s.id, s.id, s.method, s.amount, s.tip, at)
	}

	if err := dbx.repo.InsertShift(ctx, nil, "shift-5804", "reg1", "user1", 0, b8At(anchor.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	exp, err := dbx.repo.ComputeExpectedCash(ctx, "shift-5804", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.UpdateShiftClose(ctx, nil, "shift-5804", exp, exp, 0, "", "", at); err != nil {
		t.Fatal(err)
	}
	return cashTenderFixture{dbx: dbx, anchor: anchor, day: day, shift: "shift-5804"}
}

// A (10.00) + B (5.00) - return (2.00) - tip (1.00).
const (
	cash5804Sales = int64(1200)
	cash5804Tips  = int64(100)
)

func (f cashTenderFixture) eodSingle(t *testing.T) *CashReconciliation {
	t.Helper()
	rep, err := f.dbx.repo.EndOfDay(context.Background(), f.day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CashReconciliation == nil {
		t.Fatal("no cash reconciliation")
	}
	return rep.CashReconciliation
}

func (f cashTenderFixture) eodInstant(t *testing.T) *CashReconciliation {
	t.Helper()
	rep, err := f.dbx.repo.EndOfDayInstant(context.Background(), f.anchor.Add(-time.Hour), f.anchor.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rep.CashReconciliation == nil {
		t.Fatal("no cash reconciliation (instant)")
	}
	return rep.CashReconciliation
}

func (f cashTenderFixture) aggregate(t *testing.T) map[string]SalesAggregatePayment {
	t.Helper()
	agg, err := f.dbx.repo.SalesAggregateForTill(context.Background(), f.day, "reg1", "reg1", false)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]SalesAggregatePayment{}
	for _, p := range agg.Payments {
		out[p.Method] = p
	}
	return out
}

func checkCash5804(t *testing.T, f cashTenderFixture) {
	t.Helper()
	for name, rc := range map[string]*CashReconciliation{"single-day": f.eodSingle(t), "instant": f.eodInstant(t)} {
		if rc.CashSales != cash5804Sales || rc.TipsHeldOut != cash5804Tips {
			t.Errorf("%s EOD: CashSales=%d TipsHeldOut=%d, want %d / %d", name, rc.CashSales, rc.TipsHeldOut, cash5804Sales, cash5804Tips)
		}
	}
	agg := f.aggregate(t)
	want := map[string]int64{"cash": 1000, "till-cash-2": 200, "card": 0, "cashback-voucher": 0}
	var sum int64
	for m, w := range want {
		p, ok := agg[m]
		if !ok {
			t.Errorf("aggregate: no row for %s", m)
			continue
		}
		if p.ExpectedCash.Minor() != w {
			t.Errorf("aggregate %s ExpectedCash=%d, want %d", m, p.ExpectedCash.Minor(), w)
		}
		sum += p.ExpectedCash.Minor()
	}
	if sum != cash5804Sales {
		t.Errorf("aggregate sum ExpectedCash=%d, want %d", sum, cash5804Sales)
	}
}

func TestCashByType5804_SecondCashMethodCounts(t *testing.T) {
	checkCash5804(t, seedCashTender5804(t))
}

func TestCashByType5804_ShiftCloseAgreesWithEOD(t *testing.T) {
	f := seedCashTender5804(t)
	ctx := context.Background()
	// Shift close does not net refunds out of the window (ut-docs#5803 owns
	// that), so compare on a day without the return.
	mustExec(t, f.dbx.d, `DELETE FROM payments WHERE id = 'p-s5804-r'`)
	mustExec(t, f.dbx.d, `DELETE FROM sales WHERE id = 's5804-r'`)
	opened := sql.NullString{String: b8At(f.anchor.Add(-3 * time.Hour)), Valid: true}
	closed := sql.NullString{String: b8At(f.anchor), Valid: true}
	pay, err := f.dbx.repo.SumCashPaymentsForShift(ctx, "reg1", opened, closed)
	if err != nil {
		t.Fatal(err)
	}
	tips, err := f.dbx.repo.SumCashTipsForShift(ctx, "reg1", opened, closed)
	if err != nil {
		t.Fatal(err)
	}
	if got := pay - tips; got != f.eodSingle(t).CashSales {
		t.Errorf("shift close cash %d != EOD CashSales %d", got, f.eodSingle(t).CashSales)
	}
	if got := pay - tips; got != f.eodInstant(t).CashSales {
		t.Errorf("shift close cash %d != instant EOD CashSales %d", got, f.eodInstant(t).CashSales)
	}
}

func TestCashByType5804_InactiveCashMethodStillCounts(t *testing.T) {
	f := seedCashTender5804(t)
	mustExec(t, f.dbx.d, `UPDATE payment_methods SET is_active = 0 WHERE id = 'till-cash-2'`)
	checkCash5804(t, f)
}

func TestCashByType5804_UnknownPlaceholderIsNotCash(t *testing.T) {
	f := seedCashTender5804(t)
	ctx := context.Background()
	if err := f.dbx.repo.EnsurePaymentMethod(ctx, "sumup"); err != nil {
		t.Fatal(err)
	}
	var name, typ string
	var active int
	if err := f.dbx.d.DB.QueryRow(`SELECT name, type, is_active FROM payment_methods WHERE id = 'sumup'`).Scan(&name, &typ, &active); err != nil {
		t.Fatal(err)
	}
	if typ != "unknown" || name != "sumup" || active != 1 {
		t.Fatalf("placeholder row: name=%q type=%q active=%d, want sumup/unknown/1", name, typ, active)
	}
	at := b8At(f.anchor)
	mustExec(t, f.dbx.d, `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at, local_date)
VALUES('s5804-u', 'R-s5804-u', 'completed', 'sale', 'GBP', 900, 0, 0, 900, 'reg1', ?, ?, date(?, 'localtime'))`, at, at, at)
	mustExec(t, f.dbx.d, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES('p-s5804-u','s5804-u','sumup',900,'GBP',0,0,?)`, at)

	checkCash5804(t, f) // figures unchanged by the 9.00 sumup sale
	rep, err := f.dbx.repo.EndOfDay(ctx, f.day)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range rep.Methods {
		if m.Method == "sumup" && m.In == 900 {
			found = true
		}
	}
	if !found {
		t.Errorf("sumup must still be listed in rep.Methods: %+v", rep.Methods)
	}
	if p, ok := f.aggregate(t)["sumup"]; !ok || p.Amount.Minor() != 900 {
		t.Errorf("sumup must still be an aggregate payment row, got %+v ok=%v", p, ok)
	}
}

func TestEnsurePaymentMethod5804_CashIdKeepsCashType(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	mustExec(t, dbx.d, `PRAGMA foreign_keys = OFF`)
	mustExec(t, dbx.d, `DELETE FROM payment_methods WHERE id = 'cash'`)
	if err := dbx.repo.EnsurePaymentMethod(ctx, "cash"); err != nil {
		t.Fatal(err)
	}
	var typ string
	if err := dbx.d.DB.QueryRow(`SELECT type FROM payment_methods WHERE id = 'cash'`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "cash" {
		t.Fatalf("recreated cash row type=%q, want cash", typ)
	}
}
