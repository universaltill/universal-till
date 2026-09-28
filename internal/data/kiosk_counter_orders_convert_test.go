package data

import (
	"context"
	"sync"
	"testing"
)

// ut-docs#2703 (reopened): a legacy "open" pay-at-counter order carries no
// prices and could only be closed unpaid ("Mark collected"). Opening it on
// the till converts it into a held sale under the SAME id, in one
// transaction with the row moving to "held" -- so the order has one
// identity end to end, and a second tap can never mint a second sale.

func seedLegacyOpenCounterOrder(t *testing.T, repo *KioskCounterOrdersRepo) KioskCounterOrder {
	t.Helper()
	o, err := repo.Create(context.Background(), KioskCounterOrder{
		OrderType: "takeaway",
		Lines:     []KioskCounterOrderLine{{Name: "Flat White", Qty: 2, Modifiers: []string{"Oat milk"}}},
	})
	if err != nil {
		t.Fatalf("seed open counter order: %v", err)
	}
	if o.Status != KioskCounterOrderStatusOpen {
		t.Fatalf("seed: want status open, got %q", o.Status)
	}
	return o
}

func TestKioskCounterOrdersRepo_GetReturnsRowWithLines(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_get.db")
	repo := NewKioskCounterOrdersRepo(d.DB)
	o := seedLegacyOpenCounterOrder(t, repo)

	got, found, err := repo.Get(context.Background(), o.ID)
	if err != nil || !found {
		t.Fatalf("Get(%s) = found %v, err %v", o.ID, found, err)
	}
	if got.DisplayNo != o.DisplayNo || got.Status != KioskCounterOrderStatusOpen || got.OrderType != "takeaway" {
		t.Fatalf("Get returned %+v, want the seeded open takeaway order %s", got, o.DisplayNo)
	}
	if len(got.Lines) != 1 || got.Lines[0].Name != "Flat White" || got.Lines[0].Qty != 2 || len(got.Lines[0].Modifiers) != 1 {
		t.Fatalf("Get lines = %+v, want the one Flat White x2 + Oat milk line", got.Lines)
	}
	if _, found, err := repo.Get(context.Background(), "nope"); err != nil || found {
		t.Fatalf("Get(unknown) = found %v, err %v; want not found, no error", found, err)
	}
}

func TestKioskCounterOrdersRepo_ConvertOpenToHeld_WritesHeldSaleUnderSameID(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_convert.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	o := seedLegacyOpenCounterOrder(t, repo)

	converted, err := repo.ConvertOpenToHeld(ctx, HeldSale{
		ID: o.ID, Label: "C-1 · Takeaway", TotalMinor: 640, LineCount: 1,
		Payload: `{"lines":[]}`, CreatedAt: "2026-09-01 10:00:00",
	})
	if err != nil || !converted {
		t.Fatalf("ConvertOpenToHeld = %v, %v; want converted", converted, err)
	}
	got, _, _ := repo.Get(ctx, o.ID)
	if got.Status != KioskCounterOrderStatusHeld {
		t.Fatalf("row status after convert = %q, want held", got.Status)
	}
	h, found, err := NewHeldSalesRepo(d.DB).Get(ctx, o.ID)
	if err != nil || !found {
		t.Fatalf("held sale %s: found %v err %v", o.ID, found, err)
	}
	if h.TotalMinor != 640 || h.Label != "C-1 · Takeaway" || h.CreatedAt != "2026-09-01 10:00:00" {
		t.Fatalf("held sale = %+v; want total 640, the label, and the order's own created_at kept", h)
	}
	open, err := repo.ListOpen(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("ListOpen after convert = %d rows, err %v; want none", len(open), err)
	}
}

// Two taps (two tills, or a double tap) racing: exactly one conversion.
func TestKioskCounterOrdersRepo_ConvertOpenToHeld_IsIdempotentUnderRace(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_convert_race.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	o := seedLegacyOpenCounterOrder(t, repo)

	const n = 6
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.ConvertOpenToHeld(ctx, HeldSale{ID: o.ID, Label: "x", Payload: `{"lines":[]}`})
		}(i)
	}
	wg.Wait()
	wins := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("convert %d: %v", i, errs[i])
		}
		if results[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d conversions won, want exactly 1", wins)
	}
	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM held_sales WHERE id = ?`, o.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("held_sales rows for %s = %d (err %v), want 1", o.ID, count, err)
	}
	// And a later tap on the now-held order converts nothing.
	again, err := repo.ConvertOpenToHeld(ctx, HeldSale{ID: o.ID, Label: "x", Payload: `{"lines":[]}`})
	if err != nil || again {
		t.Fatalf("convert after held = %v, %v; want false, nil", again, err)
	}
}

// A historical "collected" row (the removed Mark collected action) still
// loads, is never listed as open and is never converted into a sale.
func TestKioskCounterOrdersRepo_HistoricalCollectedRowLoadsAndStaysClosed(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_collected.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	if _, err := d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at, collected_at) VALUES ('kc-old','C-3','','[{"Name":"Tea","Qty":1}]','collected','2026-07-01T10:00:00Z','2026-07-01T10:05:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.Get(ctx, "kc-old")
	if err != nil || !found || got.Status != "collected" || got.CollectedAt == "" || len(got.Lines) != 1 {
		t.Fatalf("Get = %+v found=%v err=%v, want the collected row as stored", got, found, err)
	}
	open, err := repo.ListOpen(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("ListOpen = %+v (err %v), want none", open, err)
	}
	if converted, err := repo.ConvertOpenToHeld(ctx, HeldSale{ID: "kc-old", Label: "C-3", Payload: `{"lines":[]}`}); err != nil || converted {
		t.Fatalf("ConvertOpenToHeld on a collected row = %v, %v; want false, nil", converted, err)
	}
}
