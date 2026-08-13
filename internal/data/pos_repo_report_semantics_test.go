package data

import (
	"context"
	"testing"
	"time"
)

// Report/sale-status correctness fixes (2026-07-30, from coverage batch 8's
// independent review): sales reports must not count completed RETURNS as
// sales. The file's established semantics are exclusion (DayTotal, SlowItems,
// busyBuckets all filter sale_type='sale'); SalesByDay, TopItems and
// DeadStock's has-sold subquery were the stragglers. TaxSummary deliberately
// keeps NETTING returns — that's the fiscal view, asserted elsewhere.
// Written failing-first against the unfixed queries.

func TestSalesByDay_ExcludesReturns(t *testing.T) {
	d := b8OpenDB(t, "salesbyday-returns.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := b8At(time.Now().Add(-2 * time.Hour))
	b8Sale(t, d, "rs1", now, "completed", "sale", 100, 1000)
	b8Sale(t, d, "rs2", now, "completed", "return", 30, 300) // must not appear
	b8Sale(t, d, "rs3", now, "voided", "sale", 0, 99999)     // must not appear

	daily, err := repo.SalesByDay(ctx, winFrom(7), winTo(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 {
		t.Fatalf("want 1 day row, got %+v", daily)
	}
	got := daily[0]
	if got.Count != 1 || got.Total != 1000 || got.TaxTotal != 100 {
		t.Fatalf("day = %+v, want count 1 / total 1000 / tax 100 (returns excluded, matching DayTotal on the same dashboard)", got)
	}
}

func TestTopItems_ExcludesReturns(t *testing.T) {
	d := b8OpenDB(t, "topitems-returns.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := b8At(time.Now().Add(-2 * time.Hour))
	b8Item(t, d, "ri-a", 500, nil, 1)
	b8Sale(t, d, "rt1", now, "completed", "sale", 0, 500)
	b8Line(t, d, "rt1", 1, "ri-a", "", "Widget", 1, 0, 0, 500, 500)
	b8Sale(t, d, "rt2", now, "completed", "return", 0, 200)
	b8Line(t, d, "rt2", 1, "ri-a", "", "Widget", 1, 0, 0, 200, 200)

	top, err := repo.TopItems(ctx, winFrom(7), winTo(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].Name != "Widget" {
		t.Fatalf("want just Widget, got %+v", top)
	}
	if top[0].Qty != 1 || top[0].Revenue != 500 {
		t.Fatalf("Widget = %+v, want qty 1 / revenue 500 (the return's line must not inflate it)", top[0])
	}
}

func TestDeadStock_ReturnDoesNotCountAsSold(t *testing.T) {
	d := b8OpenDB(t, "deadstock-returns.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// Neutralize the demo seed inventory so the assertion set is exact.
	mustExec(t, d, `DELETE FROM inventory`)

	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc_rd', 'Dead Shop')`)
	b8Item(t, d, "rd-a", 400, nil, 1)
	b8Stock(t, d, "rdinv1", "rd-a", "loc_rd", 5)
	// Its ONLY window activity is a completed RETURN — customers bringing
	// an item back is not it "selling"; it must still count as dead stock.
	now := b8At(time.Now().Add(-24 * time.Hour))
	b8Sale(t, d, "rd1", now, "completed", "return", 0, 400)
	b8Line(t, d, "rd1", 1, "rd-a", "", "Name rd-a", 1, 0, 0, 400, 400)

	dead, err := repo.DeadStock(ctx, winFrom(30), winTo(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range dead {
		if row.Name == "Name rd-a" {
			found = true
			if row.Qty != 5 || row.StockValue != 2000 {
				t.Fatalf("rd-a = %+v, want qty 5 / value 2000", row)
			}
		}
	}
	if !found {
		t.Fatalf("item whose only activity is a return must still be dead stock, got %+v", dead)
	}
}

// The /reports page renders SalesByDay's headline NEXT TO the department,
// till and payment breakdowns — if the breakdowns keep counting returns,
// the page contradicts itself (batch: headline 500 vs breakdowns 700).
// DepartmentsForDay feeds the Z-report, whose own till query already
// filtered sale_type='sale' — this also makes EndOfDay internally coherent.
func TestWindowReports_ExcludeReturns_DeptTillPayments(t *testing.T) {
	d := b8OpenDB(t, "window-reports-returns.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	tm := time.Now().Add(-2 * time.Hour)
	when := b8At(tm)
	b8Item(t, d, "ri-b", 500, nil, 1)
	b8Sale(t, d, "wr1", when, "completed", "sale", 0, 500)
	b8Line(t, d, "wr1", 1, "ri-b", "", "Gadget", 1, 0, 0, 500, 500)
	mustExec(t, d, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('wrp1','wr1','cash',500,'GBP',0,?)`, when)
	// The completed return that must not inflate any breakdown.
	b8Sale(t, d, "wr2", when, "completed", "return", 0, 200)
	b8Line(t, d, "wr2", 1, "ri-b", "", "Gadget", 1, 0, 0, 200, 200)
	mustExec(t, d, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('wrp2','wr2','cash',200,'GBP',0,?)`, when)

	dept, err := repo.SalesByDepartment(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatal(err)
	}
	if len(dept) != 1 || dept[0].Qty != 1 || dept[0].Revenue != 500 {
		t.Fatalf("SalesByDepartment = %+v, want one row qty 1 / revenue 500", dept)
	}

	tills, err := repo.SalesByTill(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatal(err)
	}
	if len(tills) != 1 || tills[0].Count != 1 || tills[0].Revenue != 500 {
		t.Fatalf("SalesByTill = %+v, want one row count 1 / revenue 500", tills)
	}

	day, err := repo.DepartmentsForDay(ctx, when[:10])
	if err != nil {
		t.Fatal(err)
	}
	if len(day) != 1 || day[0].Qty != 1 || day[0].Revenue != 500 {
		t.Fatalf("DepartmentsForDay = %+v, want one row qty 1 / revenue 500", day)
	}

	pay, err := repo.PaymentBreakdown(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatal(err)
	}
	if len(pay) != 1 || pay[0].Method != "cash" || pay[0].Count != 1 || pay[0].Amount != 500 {
		t.Fatalf("PaymentBreakdown = %+v, want cash count 1 / applied 500", pay)
	}
}

func TestGetLowStockItems_LocationFilterIncludesNeverStocked(t *testing.T) {
	d := b8OpenDB(t, "lowstock-locfilter.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	mustExec(t, d, `DELETE FROM inventory`)
	mustExec(t, d, `UPDATE items SET reorder_level = 0`)
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc_lf', 'Filter Main')`)

	b8Item(t, d, "lf-never", 500, nil, 1)
	mustExec(t, d, `UPDATE items SET reorder_level = 5 WHERE id = 'lf-never'`)

	// An item stocked (and low) at a DIFFERENT location must stay out of
	// this location's filtered list — the IS NULL widening must not
	// over-match rows that belong elsewhere.
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc_other', 'Elsewhere')`)
	b8Item(t, d, "lf-elsewhere", 500, nil, 1)
	mustExec(t, d, `UPDATE items SET reorder_level = 5 WHERE id = 'lf-elsewhere'`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-lf-o', 'lf-elsewhere', 'loc_other', 1)`)

	// "New item, reorder level set, never received" is exactly what a
	// per-location reorder list exists to surface (batch 8 review).
	items, err := repo.GetLowStockItems(ctx, "loc_lf")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "lf-never" || items[0].CurrentQty != 0 {
		t.Fatalf("location-filtered report must include ONLY the never-stocked item (not other locations' rows), got %+v", items)
	}
}
