package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// ut-docs#5796: ShiftXReport. Every instant derives from time.Now()
// (shiftWindowBase, UTC noon today), never a fixed date.

type xrFx struct {
	*posTestDB
	t0 time.Time
}

func xrSale(t *testing.T, f *xrFx, id, receipt, status, typ, reg string, total, tax int64, completedAt, voidedAt string) {
	t.Helper()
	created := completedAt
	if created == "" {
		created = voidedAt
	}
	var comp, voided any
	if completedAt != "" {
		comp = completedAt
	}
	if voidedAt != "" {
		voided = voidedAt
	}
	var regv any
	if reg != "" {
		regv = reg
	}
	mustExec(t, f.d, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, cashier_id, created_at, completed_at, voided_at)
VALUES(?, ?, ?, ?, 'GBP', ?, 0, ?, ?, ?, 'u-a', ?, ?, ?)`, id, receipt, status, typ, total, tax, total, regv, created, comp, voided)
}

func xrPay(t *testing.T, f *xrFx, id, sale, method string, amount, change, tip int64) {
	t.Helper()
	scInsertPayment(t, f.posTestDB, id, sale, method, amount, change, tip, b8At(f.t0))
}

// xrFixtureF is BA fixture F: shift S open on reg-a plus noise.
func xrFixtureF(t *testing.T) *xrFx {
	t.Helper()
	f := &xrFx{posTestDB: newPOSLifecycleTestDB(t), t0: shiftWindowBase()}
	t0 := f.t0
	at := func(d time.Duration) string { return b8At(t0.Add(d)) }
	mustExec(t, f.d, `INSERT INTO registers(id,name,is_active) VALUES('reg-a','Front',1)`)
	mustExec(t, f.d, `INSERT INTO registers(id,name,is_active) VALUES('reg-b','Back',1)`)
	mustExec(t, f.d, `INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('u-a','alice','Alice','','cashier',1)`)
	mustExec(t, f.d, `INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('u-b','bob','','','cashier',1)`)
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash) VALUES('S0','reg-a','u-a',?,?,500,500,500)`, at(-10*time.Hour), at(-2*time.Hour))
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('S','reg-a','u-a',?,10000)`, at(0))

	xrSale(t, f, "s1", "A-0002", "completed", "sale", "reg-a", 1000, 100, at(5*time.Minute), "")
	xrPay(t, f, "ps1", "s1", "cash", 2000, 1000, 0)
	xrSale(t, f, "s2", "A-0003", "completed", "sale", "reg-a", 2500, 250, at(10*time.Minute), "")
	xrPay(t, f, "ps2", "s2", "card", 2700, 0, 200)
	xrSale(t, f, "s3", "A-0004", "completed", "sale", "reg-a", 3000, 300, at(15*time.Minute), "")
	xrPay(t, f, "ps3a", "s3", "cash", 1150, 0, 150)
	xrPay(t, f, "ps3b", "s3", "card", 2000, 0, 0)
	xrSale(t, f, "r1", "A-0005", "completed", "return", "reg-a", 500, 50, at(20*time.Minute), "")
	xrPay(t, f, "pr1", "r1", "cash", 500, 0, 0)
	xrSale(t, f, "v1", "A-V1", "voided", "sale", "reg-a", 700, 0, "", at(25*time.Minute))
	xrSale(t, f, "pend", "A-PEND", "pending", "sale", "reg-a", 123, 0, at(12*time.Minute), "")
	mustExec(t, f.d, `INSERT INTO cleared_baskets(id, created_at, local_date, register_id, total_minor) VALUES('cb1',?,'',?,400)`, at(26*time.Minute), "reg-a")
	ctx := context.Background()
	if err := f.repo.InsertAudit(ctx, nil, "u-a", "shift", "S", "cash_adjustment", map[string]any{"type": "payout", "amount": -300, "reason": "Milk"}, at(27*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.InsertAudit(ctx, nil, "u-a", "shift", "S", "cash_adjustment", map[string]any{"type": "adjustment", "amount": 200, "reason": "Float top-up"}, at(28*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.InsertAudit(ctx, nil, "u-a", "shift", "S0", "cash_adjustment", map[string]any{"type": "payout", "amount": -50, "reason": "Earlier"}, at(-5*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	for i, d := range []time.Duration{30 * time.Minute, 31 * time.Minute} {
		if _, err := f.repo.InsertNoSaleEvent(ctx, nil, NoSaleEvent{ID: "ns" + string(rune('1'+i)), CreatedAt: at(d), RegisterID: "reg-a", ActorID: "u-a"}); err != nil {
			t.Fatal(err)
		}
	}
	// noise: reg-b inside the window, and reg-a one second before opening.
	xrSale(t, f, "nb1", "A-0001", "completed", "sale", "reg-b", 9999, 0, at(7*time.Minute), "")
	xrPay(t, f, "pnb1", "nb1", "cash", 9999, 0, 0)
	xrSale(t, f, "nbr", "B-RET", "completed", "return", "reg-b", 40, 0, at(8*time.Minute), "")
	xrSale(t, f, "nbv", "B-VOID", "voided", "sale", "reg-b", 41, 0, "", at(9*time.Minute))
	mustExec(t, f.d, `INSERT INTO cleared_baskets(id, created_at, local_date, register_id, total_minor) VALUES('cbb',?,'','reg-b',77)`, at(9*time.Minute))
	if _, err := f.repo.InsertNoSaleEvent(ctx, nil, NoSaleEvent{ID: "nsb", CreatedAt: at(9 * time.Minute), RegisterID: "reg-b", ActorID: "u-b"}); err != nil {
		t.Fatal(err)
	}
	xrSale(t, f, "pre", "A-0000", "completed", "sale", "reg-a", 5555, 0, at(-time.Second), "")
	xrPay(t, f, "ppre", "pre", "cash", 5555, 0, 0)
	return f
}

func xrMethod(r XReport, typ string) XMethodLine {
	for _, m := range r.Methods {
		if m.Type == typ {
			return m
		}
	}
	return XMethodLine{}
}

func TestShiftXReport_FixtureF(t *testing.T) {
	f := xrFixtureF(t)
	ctx := context.Background()
	now := f.t0.Add(45 * time.Minute)
	r, err := f.repo.ShiftXReport(ctx, "S", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.SalesCount != 3 || r.SalesGross.Minor() != 6500 || r.ReturnsCount != 1 || r.ReturnsTotal.Minor() != 500 ||
		r.Net.Minor() != 6000 || r.Tax.Minor() != 600 {
		t.Fatalf("totals = %+v", r)
	}
	if r.VoidsCount != 1 || r.VoidsTotal.Minor() != 700 || r.ClearedBasketsCount != 1 || r.ClearedBasketsTotal.Minor() != 400 {
		t.Fatalf("voids/cleared = %+v", r)
	}
	if r.FirstReceipt != "A-0002" || r.LastReceipt != "A-0005" {
		t.Fatalf("receipts = %q..%q", r.FirstReceipt, r.LastReceipt)
	}
	cash, card := xrMethod(r, "cash"), xrMethod(r, "card")
	if cash.In.Minor() != 2150 || cash.Refunded.Minor() != 500 || cash.Net.Minor() != 1650 || cash.Tips.Minor() != 150 {
		t.Fatalf("cash line = %+v", cash)
	}
	if card.In.Minor() != 4700 || card.Refunded.Minor() != 0 || card.Net.Minor() != 4700 || card.Tips.Minor() != 200 {
		t.Fatalf("card line = %+v", card)
	}
	c := r.Cash
	if c.Opening.Minor() != 10000 || c.CashSales.Minor() != 1500 || c.TipsHeldOut.Minor() != 150 ||
		c.AdjustmentsTotal.Minor() != -100 || c.Expected.Minor() != 11550 {
		t.Fatalf("cash = %+v", c)
	}
	if len(c.Adjustments) != 2 || c.Adjustments[0].Type != "payout" || c.Adjustments[0].Amount.Minor() != -300 || c.Adjustments[0].Reason != "Milk" ||
		c.Adjustments[1].Type != "adjustment" || c.Adjustments[1].Amount.Minor() != 200 || c.Adjustments[1].Reason != "Float top-up" {
		t.Fatalf("adjustments = %+v", c.Adjustments)
	}
	if sum := c.Opening.Add(c.CashSales).Add(c.TipsHeldOut).Add(c.AdjustmentsTotal); sum != c.Expected {
		t.Fatalf("identity: %v != %v", sum, c.Expected)
	}
	exp, err := f.repo.ComputeExpectedCash(ctx, "S", 10000)
	if err != nil || exp != c.Expected.Minor() {
		t.Fatalf("ComputeExpectedCash = %d, %v; want %d", exp, err, c.Expected.Minor())
	}
	var adjSum int64
	for _, a := range c.Adjustments {
		adjSum += a.Amount.Minor()
	}
	if adjSum != c.AdjustmentsTotal.Minor() {
		t.Fatalf("sum(list) %d != total %d", adjSum, c.AdjustmentsTotal.Minor())
	}
	if cash.Net != c.CashSales.Add(c.TipsHeldOut) || cash.Tips != c.TipsHeldOut {
		t.Fatalf("cash method line %+v disagrees with Cash %+v", cash, c)
	}
	n, ops, err := f.repo.NoSaleForShift(ctx, "S")
	if err != nil {
		t.Fatal(err)
	}
	if r.NoSale != 2 || n != 2 || len(r.NoSaleOperators) != len(ops) {
		t.Fatalf("no-sale = %d %+v", r.NoSale, r.NoSaleOperators)
	}
	if r.RegisterName != "Front" || r.CashierName != "Alice" {
		t.Fatalf("names = %q %q", r.RegisterName, r.CashierName)
	}
	if r.GeneratedAt != now.UTC().Format(time.RFC3339) {
		t.Fatalf("GeneratedAt = %q", r.GeneratedAt)
	}
}

func TestShiftXReport_Names(t *testing.T) {
	f := xrFixtureF(t)
	ctx := context.Background()
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('Sb','reg-b','u-b',?,0)`, b8At(f.t0))
	// a restored shift can name a register/cashier with no row of its own.
	mustExec(t, f.d, `PRAGMA foreign_keys = OFF`)
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('Sx','reg-x','ghost',?,0)`, b8At(f.t0))
	for _, c := range []struct{ id, reg, cashier string }{
		{"Sb", "Back", "bob"},
		{"Sx", "reg-x", "ghost"},
	} {
		r, err := f.repo.ShiftXReport(ctx, c.id, f.t0.Add(time.Hour))
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if r.RegisterName != c.reg || r.CashierName != c.cashier {
			t.Fatalf("%s names = %q %q, want %q %q", c.id, r.RegisterName, r.CashierName, c.reg, c.cashier)
		}
	}
}

func TestShiftXReport_UnknownShift(t *testing.T) {
	f := xrFixtureF(t)
	ctx := context.Background()
	for _, id := range []string{"nope", ""} {
		if _, err := f.repo.ShiftXReport(ctx, id, f.t0); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ShiftXReport(%q) err = %v, want ErrNoRows", id, err)
		}
		if _, err := f.repo.GetShift(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetShift(%q) err = %v, want ErrNoRows", id, err)
		}
	}
	s, err := f.repo.GetShift(ctx, "S0")
	if err != nil || s.Open || s.RegisterID != "reg-a" || s.ClosingCash != 500 || s.Variance != 0 {
		t.Fatalf("GetShift(S0) = %+v, %v", s, err)
	}
	s, err = f.repo.GetShift(ctx, "S")
	if err != nil || !s.Open || s.OpeningCash != 10000 {
		t.Fatalf("GetShift(S) = %+v, %v", s, err)
	}
}

func TestShiftXReport_ZeroShift(t *testing.T) {
	f := &xrFx{posTestDB: newPOSLifecycleTestDB(t), t0: shiftWindowBase()}
	ctx := context.Background()
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('Z','reg1','user1',?,10000)`, b8At(f.t0))
	now := f.t0.Add(time.Minute)
	r, err := f.repo.ShiftXReport(ctx, "Z", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.SalesCount != 0 || r.SalesGross != 0 || r.ReturnsCount != 0 || r.Net != 0 || r.Tax != 0 || r.VoidsCount != 0 ||
		r.ClearedBasketsCount != 0 || r.NoSale != 0 || r.FirstReceipt != "" || r.LastReceipt != "" {
		t.Fatalf("zero report = %+v", r)
	}
	if r.Methods != nil || len(r.Cash.Adjustments) != 0 || r.Cash.Expected.Minor() != 10000 || r.Cash.Opening.Minor() != 10000 {
		t.Fatalf("zero cash/methods = %+v", r)
	}
	if r.GeneratedAt != now.UTC().Format(time.RFC3339) {
		t.Fatalf("GeneratedAt = %q", r.GeneratedAt)
	}
}

func xrCounts(t *testing.T, f *xrFx) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"sales", "payments", "audit_log", "shifts", "report_archive", "cleared_baskets"} {
		var n int
		if err := f.d.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		out[tbl] = n
	}
	return out
}

func TestShiftXReport_ReadOnly(t *testing.T) {
	f := xrFixtureF(t)
	ctx := context.Background()
	before := xrCounts(t, f)
	for i := 0; i < 2; i++ {
		if _, err := f.repo.ShiftXReport(ctx, "S", f.t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	after := xrCounts(t, f)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s rows %d -> %d", k, v, after[k])
		}
	}
	var closed, closing, expected sql.NullString
	if err := f.d.QueryRow(`SELECT closed_at, closing_cash, expected_cash FROM shifts WHERE id='S'`).Scan(&closed, &closing, &expected); err != nil {
		t.Fatal(err)
	}
	if closed.Valid || closing.Valid || expected.Valid {
		t.Fatalf("shift S mutated: %v %v %v", closed, closing, expected)
	}
}

func TestShiftXReport_OtherRegisterExcluded(t *testing.T) {
	f := xrFixtureF(t)
	ctx := context.Background()
	r, err := f.repo.ShiftXReport(ctx, "S", f.t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if r.SalesGross.Minor() != 6500 || r.VoidsCount != 1 || r.ClearedBasketsCount != 1 || r.NoSale != 2 || r.FirstReceipt == "A-0001" {
		t.Fatalf("reg-b leaked: %+v", r)
	}
	// a sale with no register matches no shift
	xrSale(t, f, "nul", "A-NULL", "completed", "sale", "", 31337, 0, b8At(f.t0.Add(time.Minute)), "")
	r, err = f.repo.ShiftXReport(ctx, "S", f.t0.Add(time.Hour))
	if err != nil || r.SalesGross.Minor() != 6500 {
		t.Fatalf("NULL-register sale counted: %+v %v", r.SalesGross, err)
	}
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('Sb','reg-b','u-b',?,0)`, b8At(f.t0))
	rb, err := f.repo.ShiftXReport(ctx, "Sb", f.t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rb.SalesCount != 1 || rb.SalesGross.Minor() != 9999 || rb.ReturnsCount != 1 || rb.ReturnsTotal.Minor() != 40 ||
		rb.VoidsCount != 1 || rb.VoidsTotal.Minor() != 41 || rb.ClearedBasketsTotal.Minor() != 77 || rb.NoSale != 1 {
		t.Fatalf("reg-b shift = %+v", rb)
	}
}

func TestShiftXReport_Window(t *testing.T) {
	f := xrFixtureF(t)
	ctx := context.Background()
	t0 := f.t0
	now := t0.Add(45 * time.Minute)
	r1, err := f.repo.ShiftXReport(ctx, "S", now)
	if err != nil {
		t.Fatal(err)
	}
	// pre (t0-1s) is excluded everywhere: cash sales stay 1500, gross 6500.
	if r1.Cash.CashSales.Minor() != 1500 || r1.SalesGross.Minor() != 6500 {
		t.Fatalf("pre leaked: %+v", r1.Cash)
	}
	// (b) later sale appears on the next call; one after `now` counts too (one window).
	for _, c := range []struct {
		id  string
		at  time.Duration
		amt int64
	}{{"late1", 40 * time.Minute, 250}, {"late2", 46 * time.Minute, 150}} {
		xrSale(t, f, c.id, "L-"+c.id, "completed", "sale", "reg-a", c.amt, 0, b8At(t0.Add(c.at)), "")
		xrPay(t, f, "p"+c.id, c.id, "cash", c.amt, 0, 0)
	}
	r2, err := f.repo.ShiftXReport(ctx, "S", now)
	if err != nil {
		t.Fatal(err)
	}
	if r2.SalesCount != r1.SalesCount+2 || r2.Cash.Expected.Minor() != r1.Cash.Expected.Minor()+400 ||
		xrMethod(r2, "cash").In.Minor() != xrMethod(r1, "cash").In.Minor()+400 {
		t.Fatalf("second call: %+v vs %+v", r2.Cash, r1.Cash)
	}
	// (e) sale exactly at opened_at counts.
	xrSale(t, f, "edge", "A-EDGE", "completed", "sale", "reg-a", 60, 0, b8At(t0), "")
	xrPay(t, f, "pedge", "edge", "cash", 60, 0, 0)
	r3, _ := f.repo.ShiftXReport(ctx, "S", now)
	if r3.SalesCount != r2.SalesCount+1 {
		t.Fatalf("sale at opened_at not counted")
	}

	// (c) legacy opened_at.
	legacy := t0.Format("2006-01-02 15:04:05")
	mustExec(t, f.d, `INSERT INTO registers(id,name,is_active) VALUES('reg-c','C',1)`)
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('L','reg-c','u-a',?,0)`, legacy)
	for _, c := range []struct {
		id string
		d  time.Duration
		a  int64
	}{{"c1", -time.Hour, 1000}, {"c2", -25 * time.Hour, 2000}, {"c3", 5 * time.Minute, 300}, {"c4", 0, 20}} {
		xrSale(t, f, c.id, "C-"+c.id, "completed", "sale", "reg-c", c.a, 0, b8At(t0.Add(c.d)), "")
		xrPay(t, f, "p"+c.id, c.id, "cash", c.a, 0, 0)
	}
	rl, err := f.repo.ShiftXReport(ctx, "L", now)
	if err != nil {
		t.Fatal(err)
	}
	exp, _ := f.repo.ComputeExpectedCash(ctx, "L", 0)
	if rl.SalesCount != 2 || rl.SalesGross.Minor() != 320 || rl.Cash.Expected.Minor() != 320 || exp != 320 {
		t.Fatalf("legacy window: %+v exp=%d", rl, exp)
	}
	tendered, _ := f.repo.SumCashPaymentsForShift(ctx, "reg-c", nullStr(legacy), sql.NullString{})
	if tendered != 320 {
		t.Fatalf("SumCashPaymentsForShift = %d", tendered)
	}

	// (d) closed shift bounds, inclusive at closed_at.
	tc := t0.Add(50 * time.Minute)
	mustExec(t, f.d, `INSERT INTO registers(id,name,is_active) VALUES('reg-d','D',1)`)
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash) VALUES('D','reg-d','u-a',?,?,0,0,0)`, b8At(t0), b8At(tc))
	mustExec(t, f.d, `INSERT INTO cleared_baskets(id, created_at, local_date, register_id, total_minor) VALUES('cbd1',?,'','reg-d',5)`, b8At(tc))
	mustExec(t, f.d, `INSERT INTO cleared_baskets(id, created_at, local_date, register_id, total_minor) VALUES('cbd2',?,'','reg-d',6)`, b8At(tc.Add(time.Second)))
	for _, c := range []struct {
		id string
		d  time.Duration
		a  int64
	}{{"d1", 0, 100}, {"d2", time.Second, 7}} {
		xrSale(t, f, c.id, "D-"+c.id, "completed", "sale", "reg-d", c.a, 0, b8At(tc.Add(c.d)), "")
		xrPay(t, f, "p"+c.id, c.id, "cash", c.a, 0, 0)
	}
	rd, err := f.repo.ShiftXReport(ctx, "D", tc.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rd.SalesCount != 1 || rd.SalesGross.Minor() != 100 || rd.Cash.CashSales.Minor() != 100 || xrMethod(rd, "cash").In.Minor() != 100 ||
		rd.ClearedBasketsCount != 1 || rd.ClearedBasketsTotal.Minor() != 5 {
		t.Fatalf("closed window: %+v", rd)
	}
}

// closed_at ” means open (A5).
func TestShiftXReport_ClosedEmptyIsOpen(t *testing.T) {
	f := &xrFx{posTestDB: newPOSLifecycleTestDB(t), t0: shiftWindowBase()}
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash) VALUES('E','reg1','user1',?,'',0)`, b8At(f.t0))
	mustExec(t, f.d, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at) VALUES('e1','E-1','completed','sale','GBP',9,0,0,9,'reg1',?,?)`, b8At(f.t0.Add(time.Minute)), b8At(f.t0.Add(time.Minute)))
	r, err := f.repo.ShiftXReport(context.Background(), "E", f.t0.Add(time.Hour))
	if err != nil || r.SalesCount != 1 || !r.Shift.Open {
		t.Fatalf("closed '' report = %+v, %v", r, err)
	}
}

func TestShiftXReport_Errors(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		drop    string
		wantErr bool
	}{{"no_sale_events", false}, {"cleared_baskets", true}, {"payments", true}} {
		t.Run(c.drop, func(t *testing.T) {
			f := xrFixtureF(t)
			mustExec(t, f.d, `DROP TABLE `+c.drop)
			r, err := f.repo.ShiftXReport(ctx, "S", f.t0.Add(time.Hour))
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error when %s is missing", c.drop)
				}
				return
			}
			if err != nil || r.NoSale != 0 || r.SalesCount != 3 {
				t.Fatalf("best-effort no-sale: %+v %v", r, err)
			}
		})
	}
}

// The window statements must be index range seeks on the register (migration
// 100), not status-only walks of every completed sale.
func TestShiftXReport_QueryPlans(t *testing.T) {
	f := xrFixtureF(t)
	o, c := nullStr(b8At(f.t0)), nullStr(b8At(f.t0.Add(time.Hour)))
	cashQ, cashArgs := shiftCashWindow("reg-a", o, c)
	totQ, totArgs := xTotalsQuery("reg-a", o, c)
	metQ, metArgs := xMethodsQuery("reg-a", o, c)
	clrQ, clrArgs := xClearedQuery("reg-a", o, c)
	voidQ, voidArgs := xVoidsQuery("reg-a", o, c)
	for _, q := range []struct {
		name, sql string
		args      []any
		idx       string
	}{
		{"cash", `SELECT 1` + cashQ, cashArgs, "idx_sales_register_completed_dt"},
		{"totals", totQ, totArgs, "idx_sales_register_completed_dt"},
		{"methods", metQ, metArgs, "idx_sales_register_completed_dt"},
		{"cleared", clrQ, clrArgs, "idx_cleared_baskets_register_created_dt"},
		// Voids seek only voided rows via the status index; the register
		// index would walk every sale of the register (review M1).
		{"voids", voidQ, voidArgs, "idx_sales_status_recorded_dt"},
	} {
		rows, err := f.d.Query(`EXPLAIN QUERY PLAN `+q.sql, q.args...)
		if err != nil {
			t.Fatalf("%s: %v", q.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		all := strings.Join(plan, "\n")
		if !strings.Contains(all, q.idx) || strings.Contains(all, "SCAN s ") || strings.Contains(all, "SCAN sales") || strings.Contains(all, "SCAN cleared_baskets") {
			t.Fatalf("%s plan:\n%s", q.name, all)
		}
	}
}
