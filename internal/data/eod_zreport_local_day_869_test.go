package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#869: DepartmentsForDay and dateRangeSummary (backing EndOfDay/
// EndOfDayRange, the archived/printed EOD Z-report) matched bare UTC
// date(created_at) — but their caller, eodSchedulerTick (eod_api.go), hands
// them a day string computed from Go's local wall-clock time.Now(), and the
// SQLite-side /reports UI shares the same local convention (parseReportWindow
// / businessDateFor). Changed to date(created_at, 'localtime') — the SAME
// convention DayTotal already uses, and the one ut-docs#774/PR#417 already
// chose for ListSalesJournal's Day filter (not SalesByDay's business-day-
// start shift, a different semantic for trading-night merging — out of
// scope here, see ADR-0057).
//
// Independent review finding (first draft of this file): hardcoding both
// the seeded timestamps AND the expected day boundary as fixed UTC literals
// (e.g. "2026-08-15") made the assertions encode the OLD (bare-UTC)
// semantics — passing against the pre-fix code and FAILING against the
// fix itself on any non-UTC host (confirmed: TZ=Asia/Tokyo turned both
// tests backwards). That's exactly the mistake ut-docs#559 already found
// and fixed elsewhere in this package (see b8ExpectedDay's own doc comment
// below). Fixed here the same way: anchor every seeded instant on the HOST'S
// OWN local noon (time.Now().Local(), not a hardcoded date) — noon keeps
// every same-day instant safely inside its calendar day for any real IANA
// offset (-12..+14), so a midnight-adjacent literal can't silently drift
// across the boundary under an extreme offset — and derive the "day"
// argument passed to the repo methods via the same date(?, 'localtime')
// control query the production code itself uses (b8ExpectedDay), never a
// Go-side literal. This holds in every timezone, not just UTC CI's.

func TestDepartmentsForDay_LocalDayBoundary(t *testing.T) {
	d := b8OpenDB(t, "depts-local-day.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "dp-a", 500, nil, 1)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)

	// Previous local day — must NOT appear in today's report.
	b8Sale(t, d, "sale-prev", b8At(yesterday), "completed", "sale", 0, 500)
	b8Line(t, d, "sale-prev", 1, "dp-a", "", "Name dp-a", 1, 0, 0, 500, 500)
	// Two sales on the target day.
	b8Sale(t, d, "sale-morn", b8At(today), "completed", "sale", 0, 300)
	b8Line(t, d, "sale-morn", 1, "dp-a", "", "Name dp-a", 1, 0, 0, 300, 300)
	b8Sale(t, d, "sale-eve", b8At(today.Add(6*time.Hour)), "completed", "sale", 0, 200)
	b8Line(t, d, "sale-eve", 1, "dp-a", "", "Name dp-a", 1, 0, 0, 200, 200)

	targetDay := b8ExpectedDay(t, d, today, 0, 0)
	depts, err := repo.DepartmentsForDay(ctx, targetDay)
	if err != nil {
		t.Fatalf("DepartmentsForDay: %v", err)
	}
	if len(depts) != 1 {
		t.Fatalf("want 1 department row for %s, got %+v", targetDay, depts)
	}
	if depts[0].Qty != 2 || depts[0].Revenue != 500 {
		t.Fatalf("want qty 2 / revenue 500 (morning 300 + evening 200, excluding the prior-day sale), got %+v", depts[0])
	}
}

func TestEndOfDay_LocalDayBoundary(t *testing.T) {
	d := b8OpenDB(t, "eod-local-day.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)
	morn, eve := today, today.Add(6*time.Hour)

	// Same boundary shape as TestDepartmentsForDay_LocalDayBoundary, plus a
	// second till (dateRangeSummary's per-till breakdown) and a payments row
	// on both the excluded and the included sale (dateRangeSummary's
	// methods query — independent review finding: the first draft never
	// seeded payments, so that fragment had zero boundary coverage).
	b8Sale(t, d, "eod-prev", b8At(yesterday), "completed", "sale", 0, 9999)
	b8Sale(t, d, "eod-morn", b8At(morn), "completed", "sale", 10, 110)
	b8Sale(t, d, "eod-eve", b8At(eve), "completed", "sale", 5, 55)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Register 2', 'h')`); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `UPDATE sales SET till_id = 'till-2' WHERE id = 'eod-eve'`); err != nil {
		t.Fatalf("assign till: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES
('pay-prev','eod-prev','cash',9999,'GBP',0,?),
('pay-morn','eod-morn','cash',110,'GBP',0,?),
('pay-eve','eod-eve','card',55,'GBP',0,?)`, b8At(yesterday), b8At(morn), b8At(eve)); err != nil {
		t.Fatalf("seed payments: %v", err)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if rep.SalesCount != 2 {
		t.Fatalf("want 2 sales for %s (excluding the prior-day 9999 sale), got %d (gross=%d)", day, rep.SalesCount, rep.Gross)
	}
	if rep.Gross != 165 {
		t.Fatalf("want gross 110+55=165, got %d", rep.Gross)
	}
	if len(rep.Tills) != 2 {
		t.Fatalf("want 2 till breakdown rows (primary + till-2), got %+v", rep.Tills)
	}
	if len(rep.Methods) != 2 {
		t.Fatalf("want 2 payment methods (cash, card) — the prior-day cash payment must not merge into today's, got %+v", rep.Methods)
	}
	var cashIn, cardIn int64
	for _, m := range rep.Methods {
		switch m.Method {
		case "cash":
			cashIn = m.In
		case "card":
			cardIn = m.In
		}
	}
	if cashIn != 110 {
		t.Fatalf("want today's cash total 110 (excluding yesterday's 9999), got %d", cashIn)
	}
	if cardIn != 55 {
		t.Fatalf("want today's card total 55, got %d", cardIn)
	}
}
