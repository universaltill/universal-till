package data

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// ADR-0066 card 1 (ut-docs#1140): the close-to-close instant-window query
// family behind the "eod" Z-report — dateRangeSummaryInstant and its
// siblings, LatestArchivedAt, and ArchiveReport's explicit closedAt write +
// atomic same-local-day double-close guard.
//
// Timezone discipline, same as eod_zreport_local_day_869_test.go: every
// seeded instant is anchored on the HOST'S OWN local noon (time.Now(),
// never a hardcoded date), so nothing here encodes one timezone's
// semantics. The datetime(...) instant compares are absolute-instant
// comparisons and hold in any host timezone by construction; the one
// genuinely local-day-sensitive piece (the double-close guard's
// date(created_at, 'localtime')) keeps all its same-day instants within
// one hour of local noon, safely inside a single local calendar day for
// any real IANA offset.

// iwAnchor returns host-local noon today — see the header comment.
func iwAnchor() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
}

// The window is half-open [from, to): a sale AT from is included, a sale AT
// to belongs to the NEXT close — the exact boundary rule that makes
// consecutive close windows partition time with no double-counted and no
// uncovered instant. Exercised across every fragment of the family: totals,
// cancellations (voided_at window), payment methods, departments, tills
// (always computed — no from==to gate), vouchers, and the tax-band loader.
func TestDateRangeSummaryInstant_HalfOpenInstantBoundary(t *testing.T) {
	d := b8OpenDB(t, "eod-instant-boundary.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	from := iwAnchor()
	to := from.Add(time.Hour)

	b8Item(t, d, "iw-a", 500, nil, 1)

	// One sale on each side of both boundaries, three inside.
	b8Sale(t, d, "iw0", b8At(from.Add(-time.Second)), "completed", "sale", 0, 111) // before from: out
	b8Sale(t, d, "iw1", b8At(from), "completed", "sale", 0, 300)                   // exactly from: IN
	b8Sale(t, d, "iw2", b8At(from.Add(30*time.Minute)), "completed", "sale", 0, 200)
	b8Sale(t, d, "iw3", b8At(to.Add(-time.Second)), "completed", "sale", 0, 100)
	b8Sale(t, d, "iw4", b8At(to), "completed", "sale", 0, 999) // exactly to: OUT (next close's)
	for i, saleID := range []string{"iw0", "iw1", "iw2", "iw3", "iw4"} {
		b8Line(t, d, saleID, 1, "iw-a", "", "Name iw-a", 1, 0, 0, 100, 100+int64(i))
	}

	// Payments: cash inside the window, card on the excluded sales too, so a
	// leak across either boundary shows up as a merged/extra method total.
	mustExec(t, d, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES
('p-iw0','iw0','card',111,'EUR',0,?),
('p-iw1','iw1','cash',300,'EUR',0,?),
('p-iw2','iw2','card',200,'EUR',0,?),
('p-iw3','iw3','cash',100,'EUR',0,?),
('p-iw4','iw4','card',999,'EUR',0,?)`,
		b8At(from.Add(-time.Second)), b8At(from), b8At(from.Add(30*time.Minute)),
		b8At(to.Add(-time.Second)), b8At(to))

	// Second till on an in-window sale: the per-till breakdown must be
	// populated with NO from==to gate (ADR-0066 — always computed for the
	// "eod" kind).
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Register 2', 'h')`)
	mustExec(t, d, `UPDATE sales SET till_id = 'till-2' WHERE id = 'iw2'`)

	// Cancellations window on COALESCE(voided_at, created_at): voided
	// mid-window counts as this close's Storno even though the sale was
	// completed long before the window; voided exactly AT to is the next
	// close's.
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
VALUES ('iw-void-in', 'R-iw-void-in', 'voided', 'sale', 400, 400, ?, ?)`,
		b8At(from.Add(-48*time.Hour)), b8At(from.Add(30*time.Minute)))
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
VALUES ('iw-void-out', 'R-iw-void-out', 'voided', 'sale', 800, 800, ?, ?)`,
		b8At(from.Add(-48*time.Hour)), b8At(to))
	// Lower bound of the same window (review finding N3, ut-docs#1140): a
	// void exactly BEFORE from must also be excluded — the boundary test
	// above only exercised the upper bound (voided at to).
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
VALUES ('iw-void-before', 'R-iw-void-before', 'voided', 'sale', 1600, 1600, ?, ?)`,
		b8At(from.Add(-48*time.Hour)), b8At(from.Add(-time.Second)))

	// Voucher flows (the sibling that lives in voucher_repo.go).
	vSeedVoucher(t, ctx, repo, "GS-IW", 9000)
	for _, tx := range []struct {
		id, typ string
		amount  int64
		at      time.Time
	}{
		{"vt-iw-in", "issue", 1500, from},                             // exactly from: IN
		{"vt-iw-red", "redemption", 1000, from.Add(30 * time.Minute)}, // IN
		{"vt-iw-out", "issue", 7777, to},                              // exactly to: OUT
	} {
		if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
			ID: tx.id, VoucherID: "GS-IW", SaleID: "iw1", Type: tx.typ,
			AmountMinor: tx.amount, CreatedAt: b8At(tx.at),
		}); err != nil {
			t.Fatalf("RecordVoucherTransaction %s: %v", tx.id, err)
		}
	}

	rep, err := repo.dateRangeSummaryInstant(ctx, from, to)
	if err != nil {
		t.Fatalf("dateRangeSummaryInstant: %v", err)
	}
	if rep.SalesCount != 3 || rep.Gross != 600 {
		t.Fatalf("want 3 sales / gross 600 (300+200+100; at-from in, at-to out), got %d/%d", rep.SalesCount, rep.Gross)
	}
	if rep.FirstReceipt != "R-iw1" || rep.LastReceipt != "R-iw3" {
		t.Fatalf("want receipt range R-iw1..R-iw3, got %q..%q", rep.FirstReceipt, rep.LastReceipt)
	}
	if rep.CancelCount != 1 || rep.CancelTotal != 400 {
		t.Fatalf("want 1 cancellation / 400 (voided mid-window in, voided at to out), got %d/%d",
			rep.CancelCount, rep.CancelTotal)
	}
	if len(rep.Methods) != 2 {
		t.Fatalf("want 2 payment methods (cash, card) inside the window only, got %+v", rep.Methods)
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
	if cashIn != 400 || cardIn != 200 {
		t.Fatalf("want cash 400 / card 200 (boundary sales excluded), got cash=%d card=%d", cashIn, cardIn)
	}
	if len(rep.Departments) != 1 || rep.Departments[0].Qty != 3 {
		t.Fatalf("want 1 department row with qty 3 (in-window lines only), got %+v", rep.Departments)
	}
	if len(rep.Tills) != 2 {
		t.Fatalf("want 2 till breakdown rows with NO from==to gate, got %+v", rep.Tills)
	}
	if rep.VouchersIssuedCount != 1 || rep.VouchersIssued != 1500 {
		t.Fatalf("want vouchers issued 1/1500 (at-from in, at-to out), got %d/%d",
			rep.VouchersIssuedCount, rep.VouchersIssued)
	}
	if rep.VouchersRedeemedCount != 1 || rep.VouchersRedeemed != 1000 {
		t.Fatalf("want vouchers redeemed 1/1000, got %d/%d", rep.VouchersRedeemedCount, rep.VouchersRedeemed)
	}

	// The tax-band loader obeys the same boundary.
	sales, err := repo.SalesForTaxBandsInstant(ctx, from, to)
	if err != nil {
		t.Fatalf("SalesForTaxBandsInstant: %v", err)
	}
	if len(sales) != 3 {
		t.Fatalf("want 3 band sales (same half-open window), got %d: %+v", len(sales), sales)
	}
	for _, s := range sales {
		if s.ID == "iw0" || s.ID == "iw4" {
			t.Fatalf("boundary sale %s leaked into the band window", s.ID)
		}
		if len(s.Lines) != 1 {
			t.Fatalf("band sale %s: want 1 line attached, got %+v", s.ID, s.Lines)
		}
		if len(s.Payments) != 1 {
			t.Fatalf("band sale %s: want 1 payment attached, got %+v", s.ID, s.Payments)
		}
	}

	// And the direct department sibling.
	depts, err := repo.DepartmentsForInstantWindow(ctx, from, to)
	if err != nil {
		t.Fatalf("DepartmentsForInstantWindow: %v", err)
	}
	if len(depts) != 1 || depts[0].Qty != 3 || depts[0].Revenue != 306 {
		t.Fatalf("want qty 3 / revenue 306 (101+102+103) inside the window, got %+v", depts)
	}
}

// A zero `from` is the till's first-ever close (ADR-0066 Decision 3): the
// lower bound is omitted entirely, so every completed sale ever recorded —
// including one from years back — is in scope for the first close.
func TestDateRangeSummaryInstant_FirstCloseUnboundedLowerBound(t *testing.T) {
	d := b8OpenDB(t, "eod-instant-firstclose.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	ancient := time.Date(2020, 6, 1, 12, 0, 0, 0, time.UTC)
	recent := iwAnchor()
	to := recent.Add(time.Hour)

	b8Item(t, d, "fc-a", 500, nil, 1)
	b8Sale(t, d, "fc-old", b8At(ancient), "completed", "sale", 0, 700)
	b8Line(t, d, "fc-old", 1, "fc-a", "", "Name fc-a", 1, 0, 0, 700, 700)
	b8Sale(t, d, "fc-new", b8At(recent), "completed", "sale", 0, 300)
	b8Line(t, d, "fc-new", 1, "fc-a", "", "Name fc-a", 1, 0, 0, 300, 300)
	// Upper bound still applies with an unbounded lower bound (review
	// finding N2, ut-docs#1140): a zero `from` must not degenerate into no
	// bound at all. A sale exactly AT `to` belongs to the NEXT close.
	b8Sale(t, d, "fc-out", b8At(to), "completed", "sale", 0, 9999)
	b8Line(t, d, "fc-out", 1, "fc-a", "", "Name fc-a", 1, 0, 0, 9999, 9999)

	vSeedVoucher(t, ctx, repo, "GS-FC", 5000)
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "vt-fc-old", VoucherID: "GS-FC", SaleID: "fc-old", Type: "issue",
		AmountMinor: 1200, CreatedAt: b8At(ancient),
	}); err != nil {
		t.Fatalf("RecordVoucherTransaction: %v", err)
	}

	rep, err := repo.dateRangeSummaryInstant(ctx, time.Time{}, to)
	if err != nil {
		t.Fatalf("dateRangeSummaryInstant (zero from): %v", err)
	}
	if rep.SalesCount != 2 || rep.Gross != 1000 {
		t.Fatalf("want the 2020 sale included with no lower bound (2 sales / 1000), got %d/%d",
			rep.SalesCount, rep.Gross)
	}
	if rep.VouchersIssuedCount != 1 || rep.VouchersIssued != 1200 {
		t.Fatalf("want the 2020 voucher issue included with no lower bound, got %d/%d",
			rep.VouchersIssuedCount, rep.VouchersIssued)
	}

	sales, err := repo.SalesForTaxBandsInstant(ctx, time.Time{}, to)
	if err != nil {
		t.Fatalf("SalesForTaxBandsInstant (zero from): %v", err)
	}
	if len(sales) != 2 {
		t.Fatalf("want both sales in the unbounded band window, got %d", len(sales))
	}

	depts, err := repo.DepartmentsForInstantWindow(ctx, time.Time{}, to)
	if err != nil {
		t.Fatalf("DepartmentsForInstantWindow (zero from): %v", err)
	}
	if len(depts) != 1 || depts[0].Revenue != 1000 {
		t.Fatalf("want unbounded department revenue 1000, got %+v", depts)
	}
}

// Shift-closure windowing over instants: a shift closed exactly AT `to`
// falls into the NEXT close's reconciliation — the ADR calls this out as
// the intended close-to-close semantic, not a bug. Same nil, nil on zero
// shifts closed, same latest-close-per-register new-float rule, same
// shift-scoped adjustment split as the local-day sibling.
func TestCashReconciliationForInstantWindow(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	from := iwAnchor()
	to := from.Add(time.Hour)

	// Zero shifts closed in the window: nil, nil — never an error (EOD
	// generation must not fail because nobody closed a shift).
	rec, err := dbx.repo.CashReconciliationForInstantWindow(ctx, from, to)
	if err != nil || rec != nil {
		t.Fatalf("want nil, nil with no closed shifts, got %+v err=%v", rec, err)
	}

	open := b8At(from.Add(-3 * time.Hour))
	for _, s := range []struct {
		id       string
		closedAt time.Time
		closing  int64
	}{
		{"sh-before", from.Add(-time.Second), 7777}, // before from: out
		{"sh-in", from, 5500},                       // exactly from: IN
		{"sh-at-to", to, 9999},                      // exactly to: OUT (next close's)
	} {
		if err := dbx.repo.InsertShift(ctx, nil, s.id, "reg1", "user1", 5000, open); err != nil {
			t.Fatal(err)
		}
		if err := dbx.repo.UpdateShiftClose(ctx, nil, s.id, s.closing, s.closing-100, 1000, "", "", b8At(s.closedAt)); err != nil {
			t.Fatal(err)
		}
	}
	// A pay-in on the in-window shift counts; one on an out-of-window shift
	// must not.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "sh-in", "cash_adjustment",
		map[string]any{"shift_id": "sh-in", "amount": 500, "reason": "float top-up"}, b8At(from.Add(5*time.Minute)), ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "shift", "sh-before", "cash_adjustment",
		map[string]any{"shift_id": "sh-before", "amount": 700, "reason": "not this close"}, b8At(from.Add(-time.Minute)), ""); err != nil {
		t.Fatal(err)
	}

	rec, err = dbx.repo.CashReconciliationForInstantWindow(ctx, from, to)
	if err != nil {
		t.Fatalf("CashReconciliationForInstantWindow: %v", err)
	}
	if rec == nil {
		t.Fatal("want a reconciliation for the one in-window shift close, got nil")
	}
	if rec.ShiftsClosed != 1 {
		t.Fatalf("want exactly the shift closed AT from (half-open [from, to)), got %d closed", rec.ShiftsClosed)
	}
	if rec.OpeningFloat != 5000 || rec.Counted != 5500 || rec.Calculated != 5400 || rec.Variance != 100 {
		t.Fatalf("want opening/counted/calculated/variance 5000/5500/5400/100 from sh-in only, got %+v", rec)
	}
	if rec.NewFloat != 1000 {
		t.Fatalf("want new float 1000 from the in-window close only, got %d", rec.NewFloat)
	}
	if rec.PayIns != 500 || rec.PayOuts != 0 || rec.Skim != 0 {
		t.Fatalf("want only the in-window shift's pay-in (500), got %+v", rec)
	}

	// The summary attaches it with NO from==to gate.
	rep, err := dbx.repo.dateRangeSummaryInstant(ctx, from, to)
	if err != nil {
		t.Fatalf("dateRangeSummaryInstant: %v", err)
	}
	if rep.CashReconciliation == nil || rep.CashReconciliation.ShiftsClosed != 1 {
		t.Fatalf("want the reconciliation attached to the instant summary ungated, got %+v", rep.CashReconciliation)
	}
}

// LatestArchivedAt: nil, nil on an empty archive; the greatest created_at of
// the requested kind only; parsed as UTC-naive text regardless of the host
// timezone. Go caches time.Local at process start, so t.Setenv("TZ", ...)
// alone cannot un-hide a time.ParseInLocation(..., time.Local) mistake —
// the test overrides time.Local directly (restored on cleanup) so a
// local-parse bug shifts the returned instant by 5h and fails loudly even
// under CI's TZ=UTC.
func TestLatestArchivedAt(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	origLocal := time.Local
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	t.Cleanup(func() { time.Local = origLocal })

	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	got, err := dbx.repo.LatestArchivedAt(ctx, "eod")
	if err != nil || got != nil {
		t.Fatalf("want nil, nil on an empty archive, got %v err=%v", got, err)
	}

	// Rows across two kinds — MAX must be per-kind, and the later 'weekly'
	// row must not bleed into the 'eod' answer.
	mustExec(t, dbx.d, `INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES
('la-1', 'eod', '2026-08-20', '{}', '2026-08-20 10:00:00'),
('la-2', 'eod', '2026-08-22', '{}', '2026-08-22 19:19:05'),
('la-3', 'weekly', '2026-W34', '{}', '2026-08-25 09:00:00')`)

	wantEOD := time.Date(2026, 8, 22, 19, 19, 5, 0, time.UTC)
	got, err = dbx.repo.LatestArchivedAt(ctx, "eod")
	if err != nil || got == nil {
		t.Fatalf("LatestArchivedAt(eod): got %v err=%v", got, err)
	}
	if !got.Equal(wantEOD) {
		t.Fatalf("want the eod MAX parsed as UTC (%v), got %v — a local-time parse would be 5h off", wantEOD, got)
	}
	wantWeekly := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	if got, err = dbx.repo.LatestArchivedAt(ctx, "weekly"); err != nil || got == nil || !got.Equal(wantWeekly) {
		t.Fatalf("LatestArchivedAt(weekly): want %v, got %v err=%v", wantWeekly, got, err)
	}

	// A corrupt created_at is an ERROR, never a silent fallback — this value
	// becomes a fiscal window boundary (unlike formatArchiveTimestamp's
	// display-only fallback).
	mustExec(t, dbx.d, `INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES
('la-bad', 'monthly', '2026-08', '{}', 'not-a-timestamp')`)
	if _, err := dbx.repo.LatestArchivedAt(ctx, "monthly"); err == nil {
		t.Fatal("want a parse error for a corrupt created_at, got nil")
	}
}

// A non-zero closedAt is written INTO created_at (the ADR's clock-skew fix):
// the stored value round-trips to exactly the close instant, to the second,
// never a second, independent datetime('now') stamp — so the next close's
// `from` (LatestArchivedAt) is byte-identical to this close's `to`.
func TestArchiveReport_ClosedAtRoundTrip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Nanoseconds on purpose: storage is second-precision text, so the
	// stored value must equal the instant truncated to the second.
	closedAt := time.Date(2026, 8, 24, 19, 19, 5, 987654321, time.UTC)
	created, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-08-24T19:19:05Z", []byte(`{}`), "R1", "R9", closedAt)
	if err != nil || !created {
		t.Fatalf("archive with closedAt: created=%v err=%v", created, err)
	}

	row := findArchived(t, dbx.repo, "eod", "2026-08-24T19:19:05Z")
	if want := "2026-08-24T19:19:05Z"; row.CreatedAt != want {
		t.Fatalf("want stored created_at %q (the close instant, not datetime('now')), got %q", want, row.CreatedAt)
	}

	latest, err := dbx.repo.LatestArchivedAt(ctx, "eod")
	if err != nil || latest == nil {
		t.Fatalf("LatestArchivedAt after closedAt write: got %v err=%v", latest, err)
	}
	if !latest.Equal(closedAt.Truncate(time.Second)) {
		t.Fatalf("want LatestArchivedAt == closedAt to the second (%v), got %v — from(n+1) must equal to(n)",
			closedAt.Truncate(time.Second), latest)
	}
}

// The atomic double-close guard (ADR-0066 Decision 4): genuinely concurrent
// closes — goroutines against the same pooled *sql.DB, DISTINCT periods so
// (kind, period) uniqueness cannot be what saves us — with closedAt values
// on the same local calendar day must produce exactly ONE archived row; the
// losers get created=false with no error (the same contract the UI's
// "already ran" path reads) and consume no Z-number. A close on the NEXT
// local day then proceeds normally, continuing the gapless sequence.
func TestArchiveReport_ConcurrentSameLocalDayDoubleClose(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	anchor := iwAnchor()

	const n = 10
	var wg sync.WaitGroup
	createds := make([]bool, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			closedAt := anchor.Add(time.Duration(i) * time.Minute) // all within local noon..noon+9m: one local day
			period := closedAt.UTC().Format(time.RFC3339)
			createds[i], errs[i] = dbx.repo.ArchiveReport(context.Background(), "eod", period,
				[]byte(`{}`), "", "", closedAt)
		}()
	}
	wg.Wait()

	wins := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("concurrent close %d errored: %v — a guard hit must be created=false, never an error", i, errs[i])
		}
		if createds[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("want exactly ONE of %d same-local-day concurrent closes to win, got %d", n, wins)
	}
	rows, err := dbx.repo.ListArchivedReports(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ZNumber != 1 {
		t.Fatalf("want a single archived row with z_number=1 (losers consumed no number), got %+v", rows)
	}

	// A different local calendar day is NOT blocked (no false positive), and
	// the sequence continues gaplessly.
	nextDay := anchor.AddDate(0, 0, 1)
	created, err := dbx.repo.ArchiveReport(context.Background(), "eod",
		nextDay.UTC().Format(time.RFC3339), []byte(`{}`), "", "", nextDay)
	if err != nil || !created {
		t.Fatalf("next-local-day close must succeed: created=%v err=%v", created, err)
	}
	row := findArchived(t, dbx.repo, "eod", nextDay.UTC().Format(time.RFC3339))
	if row.ZNumber != 2 || row.PrevZNumber == nil || *row.PrevZNumber != 1 {
		t.Fatalf("want the next-day close chained as z=2 after z=1, got %+v", row)
	}
}

// The double-close guard must compare LOCAL calendar days, never a bare
// UTC date (review finding N4, ut-docs#1140 — ADR-0066 Decision 5
// explicitly calls for this: "CI's TZ=UTC cannot catch a mistake here").
// Under a real, large positive UTC offset, two closes on the SAME local
// day can land on DIFFERENT UTC calendar days: a host at TZ=Pacific/
// Kiritimati (UTC+14) has local 2026-08-27 01:00 stored as UTC-naive text
// "2026-08-26 11:00:00", and local 2026-08-27 20:00 stored as
// "2026-08-27 06:00:00" — same local day, different UTC day. A bare
// date(created_at) (no 'localtime') would treat those as different days
// and fail to block the second, silently burning a real gapless Z-number
// on a same-local-day duplicate Z-Bon. modernc.org/sqlite's 'localtime'
// resolves from the process TZ (confirmed empirically: overriding
// time.Local does NOT affect it, only a real process-level TZ does), so
// this test sets it via t.Setenv, not by faking Go's own time.Local.
//
// t.Setenv("TZ", ...) is NOT enough to prove this (tried first, and it's
// wrong): modernc.org/sqlite's 'localtime' resolution is cached the first
// time any query in the process evaluates it, so a mid-process env change
// after an earlier test has already exercised 'localtime' under the
// default TZ silently has no effect — the test would pass or fail based on
// test execution ORDER, not on the guard's actual correctness, which is
// exactly the kind of flaky-looking-but-actually-meaningless test this
// pipeline's own tester skill warns about. The real fix (same pattern
// already used by internal/logging's TestFatalfExitsProcess and
// internal/selfupdate's subprocess tests) is a genuine fresh PROCESS with
// TZ set in its environment before the Go runtime — and therefore
// modernc.org/sqlite's tzdata — ever initializes.
func TestArchiveReport_GuardComparesLocalDayNotUTCDay(t *testing.T) {
	if os.Getenv("UT_TEST_GUARD_TZ") == "1" {
		runGuardComparesLocalDayNotUTCDayBody(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestArchiveReport_GuardComparesLocalDayNotUTCDay$", "-test.v")
	cmd.Env = append(os.Environ(), "UT_TEST_GUARD_TZ=1", "TZ=Pacific/Kiritimati")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("subprocess (TZ=Pacific/Kiritimati) failed: err=%v\n%s", err, out)
	}
}

func runGuardComparesLocalDayNotUTCDayBody(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	closedAt1 := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC) // local 2026-08-27 01:00
	closedAt2 := time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC)  // local 2026-08-27 20:00 — same local day, next UTC day

	created, err := dbx.repo.ArchiveReport(ctx, "eod", closedAt1.Format(time.RFC3339), []byte(`{}`), "", "", closedAt1)
	if err != nil || !created {
		t.Fatalf("first close: created=%v err=%v", created, err)
	}
	created, err = dbx.repo.ArchiveReport(ctx, "eod", closedAt2.Format(time.RFC3339), []byte(`{}`), "", "", closedAt2)
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
	if created {
		t.Fatal("want the second close BLOCKED — same LOCAL day (UTC+14) despite landing on the next UTC calendar day; " +
			"a bare date(created_at) with no 'localtime' would wrongly let this through")
	}

	// A close genuinely on the NEXT local day is not blocked (no
	// false-positive from over-widening the guard).
	closedAt3 := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC) // local 2026-08-28 01:00
	created, err = dbx.repo.ArchiveReport(ctx, "eod", closedAt3.Format(time.RFC3339), []byte(`{}`), "", "", closedAt3)
	if err != nil || !created {
		t.Fatalf("next-local-day close must succeed: created=%v err=%v", created, err)
	}
}

// The guard is scoped exactly: it never applies to a zero closedAt (legacy
// callers — several same-real-day "eod" periods still all insert, exactly
// as before this card) and never to another kind even with closedAt set
// (only "eod" is the archived, Z-numbered, once-per-day close).
func TestArchiveReport_ClosedAtGuardIsEODOnlyAndOptIn(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	anchor := iwAnchor()

	// Zero closedAt: both eod inserts get today's datetime('now') stamp —
	// same real local day — and must BOTH succeed, byte-for-byte legacy
	// behavior.
	for _, p := range []string{"2026-01-01", "2026-01-02"} {
		created, err := dbx.repo.ArchiveReport(ctx, "eod", p, []byte(`{}`), "", "", time.Time{})
		if err != nil || !created {
			t.Fatalf("zero-closedAt archive %s: created=%v err=%v — the guard must not apply", p, created, err)
		}
	}

	// Non-eod kind with closedAt set: created_at is written, but two
	// same-local-day closes both succeed — the guard is eod-only.
	for i, p := range []string{"2026-W10", "2026-W11"} {
		created, err := dbx.repo.ArchiveReport(ctx, "weekly", p, []byte(`{}`), "", "",
			anchor.Add(time.Duration(i)*time.Minute))
		if err != nil || !created {
			t.Fatalf("weekly archive %s with closedAt: created=%v err=%v — the eod guard must not apply", p, created, err)
		}
	}
	row := findArchived(t, dbx.repo, "weekly", "2026-W10")
	if want := anchor.UTC().Format(time.RFC3339); row.CreatedAt != want {
		t.Fatalf("want the weekly row's created_at written from closedAt (%q), got %q", want, row.CreatedAt)
	}

	// Positive control (review finding N1, ut-docs#1140): the two negative
	// checks above ("must not apply") pass trivially if the guard were
	// deleted entirely, so on their own they don't prove it's actually
	// armed. This does: a genuine "eod" close with a non-zero closedAt on
	// the SAME local day as an existing "eod" row must be BLOCKED.
	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-03-01T09:00:00Z", []byte(`{}`), "", "", anchor); err != nil {
		t.Fatalf("seed first guarded eod close: %v", err)
	}
	created, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-03-01T09:05:00Z", []byte(`{}`), "", "",
		anchor.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("second same-local-day eod close: %v", err)
	}
	if created {
		t.Fatal("want the guard to block a second same-local-day eod close (created=false), got created=true — the guard is not armed")
	}
}
