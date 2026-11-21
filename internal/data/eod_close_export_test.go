package data

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestEODClosesForExport covers the ut-docs#1005 conversion from archived
// report rows to the export payload's eod_closes: only kind="eod" rows
// qualify, each carries its own Z-number, and the Report is the archived
// content_json unmarshaled — never a fresh recomputation.
func TestEODClosesForExport(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	repo := dbx.repo

	// ADR-0066 Decision 5: ArchivedReportsInRange (which this method calls)
	// now filters on each row's own created_at, not a period BETWEEN text
	// compare -- a zero closedAt stamps the schema default's real "now"
	// regardless of what period string is given, so a fixed 2026-08
	// literal no longer proves anything about range bounding once the
	// calendar rolls past August. Anchored on the host's own real "now"
	// (never a hardcoded year) with an explicit closedAt per row so each
	// genuinely lands on a distinct real day, same fix shape as
	// TestArchivedReportsInRange_BoundedAndOrdered.
	// LOCAL noon, never UTC (2026-09-04 review of ut-docs#1141): the range
	// filter compares datetime(created_at, 'localtime') against these same
	// LOCAL date bounds, so a UTC-anchored seed would encode UTC-day
	// semantics and flip this test's meaning on a non-UTC host — the exact
	// mistake eod_zreport_local_day_869_test.go's own doc comment records.
	// Noon keeps every same-day instant safely inside its calendar day for
	// any real IANA offset (-12..+14).
	anchor := time.Now()
	at := func(daysAgo int) time.Time {
		return time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo)
	}
	close1At := at(2)
	close2At := at(1)
	outOfRangeAt := at(20)

	// Two real closes with the cross-tab fields the DATEV batch reads.
	if _, err := repo.ArchiveReport(ctx, "eod", close1At.Format(time.RFC3339),
		[]byte(`{"day":"2026-08-20","gross":11900,"method_tax_bands":[{"method":"cash","rate_bp":1900,"net":10000,"tax":1900,"gross":11900}]}`),
		"R-1", "R-9", close1At); err != nil {
		t.Fatalf("archive close 1: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", close2At.Format(time.RFC3339),
		[]byte(`{"day":"2026-08-21","gross":23800,"method_tax_bands":[{"method":"card","rate_bp":1900,"net":20000,"tax":3800,"gross":23800}],"tips":[{"method":"card","count":1,"amount":320}]}`),
		"R-10", "R-20", close2At); err != nil {
		t.Fatalf("archive close 2: %v", err)
	}
	// A non-eod archived report in the same window — must be filtered out
	// by kind, not range (zero closedAt is fine here: EODClosesFromArchive
	// drops it regardless of where its real created_at lands).
	if _, err := repo.ArchiveReport(ctx, "weekly", "2026-08-21", []byte(`{"gross":1}`), "", "", time.Time{}); err != nil {
		t.Fatalf("archive weekly: %v", err)
	}
	// Outside the range — excluded by ArchivedReportsInRange itself.
	if _, err := repo.ArchiveReport(ctx, "eod", outOfRangeAt.Format(time.RFC3339), []byte(`{"day":"2026-09-01"}`), "", "", outOfRangeAt); err != nil {
		t.Fatalf("archive out-of-range close: %v", err)
	}

	from := at(3).Format("2006-01-02")
	to := at(1).Format("2006-01-02")
	closes, err := repo.EODClosesForExport(ctx, from, to)
	if err != nil {
		t.Fatalf("EODClosesForExport: %v", err)
	}
	if len(closes) != 2 {
		t.Fatalf("expected 2 eod closes in range, got %d: %+v", len(closes), closes)
	}
	// Oldest first (ArchivedReportsInRange's order), each with its own
	// Z-number — never merged.
	if closes[0].Report.Day != "2026-08-20" || closes[1].Report.Day != "2026-08-21" {
		t.Fatalf("expected period order 2026-08-20, 2026-08-21; got %q, %q", closes[0].Report.Day, closes[1].Report.Day)
	}
	if closes[0].ZNumber != 1 || closes[1].ZNumber != 2 {
		t.Fatalf("expected z_numbers 1 and 2, got %d and %d", closes[0].ZNumber, closes[1].ZNumber)
	}
	if len(closes[0].Report.MethodTaxBands) != 1 || closes[0].Report.MethodTaxBands[0].Gross != 11900 {
		t.Fatalf("close 1 should carry its archived cross-tab, got %+v", closes[0].Report.MethodTaxBands)
	}
	if len(closes[1].Report.Tips) != 1 || closes[1].Report.Tips[0].Amount != 320 {
		t.Fatalf("close 2 should carry its archived tips, got %+v", closes[1].Report.Tips)
	}
}

// TestEODClosesForExport_CorruptContentSkippedNotFatal: a single archive row
// whose content_json fails to unmarshal is skipped (logged) — it must never
// take every other close's export down with it.
func TestEODClosesForExport_CorruptContentSkippedNotFatal(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	repo := dbx.repo

	// Same real-clock anchoring fix as TestEODClosesForExport above (ADR-0066
	// Decision 5) — a fixed 2026-08 literal doesn't survive the range
	// filter moving off period onto created_at.
	// LOCAL noon, never UTC (2026-09-04 review of ut-docs#1141): the range
	// filter compares datetime(created_at, 'localtime') against these same
	// LOCAL date bounds, so a UTC-anchored seed would encode UTC-day
	// semantics and flip this test's meaning on a non-UTC host — the exact
	// mistake eod_zreport_local_day_869_test.go's own doc comment records.
	// Noon keeps every same-day instant safely inside its calendar day for
	// any real IANA offset (-12..+14).
	anchor := time.Now()
	at := func(daysAgo int) time.Time {
		return time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo)
	}
	closeGood1At := at(3)
	closeCorruptAt := at(2)
	closeGood2At := at(1)

	if _, err := repo.ArchiveReport(ctx, "eod", closeGood1At.Format(time.RFC3339), []byte(`{"day":"2026-08-20"}`), "", "", closeGood1At); err != nil {
		t.Fatalf("archive good close: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", closeCorruptAt.Format(time.RFC3339), []byte(`this is not json`), "", "", closeCorruptAt); err != nil {
		t.Fatalf("archive corrupt close: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", closeGood2At.Format(time.RFC3339), []byte(`{"day":"2026-08-22"}`), "", "", closeGood2At); err != nil {
		t.Fatalf("archive second good close: %v", err)
	}

	from := at(4).Format("2006-01-02")
	to := at(1).Format("2006-01-02")
	closes, err := repo.EODClosesForExport(ctx, from, to)
	if err != nil {
		t.Fatalf("a corrupt archive row must not fail the batch, got error: %v", err)
	}
	if len(closes) != 2 {
		t.Fatalf("expected the 2 parseable closes, got %d: %+v", len(closes), closes)
	}
	if closes[0].Report.Day != "2026-08-20" || closes[1].Report.Day != "2026-08-22" {
		t.Fatalf("expected the corrupt middle row skipped, got %q and %q", closes[0].Report.Day, closes[1].Report.Day)
	}
}

// TestEODClosesFromRows_FiltersKind exercises the pure conversion
// directly: non-eod kinds never convert, whatever their content looks like,
// and an unparseable CreatedAt keeps the row with an empty ClosedAt.
func TestEODClosesFromRows_FiltersKind(t *testing.T) {
	rows := []eodExportRow{
		{ArchivedReportRow: ArchivedReportRow{ID: "a", Kind: "weekly", Period: "2025-06-20", Content: `{"day":"2025-06-20"}`, ZNumber: 5}, TradingDate: "2025-06-20"},
		{ArchivedReportRow: ArchivedReportRow{ID: "b", Kind: "eod", Period: "2025-06-21", Content: `{"day":"2025-06-21"}`, ZNumber: 6, CreatedAt: "not a time"}, TradingDate: "2025-06-21"},
	}
	closes := eodClosesFromRows(rows)
	if len(closes) != 1 {
		t.Fatalf("expected only the eod row, got %d: %+v", len(closes), closes)
	}
	if closes[0].ZNumber != 6 || closes[0].Report.Day != "2025-06-21" || closes[0].TradingDate != "2025-06-21" {
		t.Fatalf("unexpected converted close: %+v", closes[0])
	}
	if closes[0].ClosedAt != "" {
		t.Fatalf("unparseable created_at must give empty closed_at, got %q", closes[0].ClosedAt)
	}
	if got := eodClosesFromRows(nil); got == nil {
		t.Fatal("empty input must give a non-nil slice (wire [] vs null)")
	}
}

// seedEODRow inserts an archived eod row straight into report_archive with
// an explicit created_at, bypassing ArchiveReport's one-close-per-local-day
// guard (these tests need several closes on one local day).
func seedEODRow(t *testing.T, dbx *posTestDB, z int64, period string, createdAtUTC time.Time, content string) {
	t.Helper()
	_, err := dbx.d.DB.ExecContext(context.Background(), `
INSERT INTO report_archive (id, kind, period, content_json, z_number, created_at)
VALUES (?, 'eod', ?, ?, ?, ?)`, "id-"+period+"-"+time.Duration(z).String(), period, content, z,
		createdAtUTC.UTC().Format(archiveTimestampFmt))
	if err != nil {
		t.Fatalf("seed eod row z=%d: %v", z, err)
	}
}

func setBusinessDayStart(t *testing.T, dbx *posTestDB, v string) {
	t.Helper()
	if v == "<unset>" {
		return
	}
	if err := NewSettingsRepo(dbx.d.DB).Set(context.Background(), "reports.business_day_start", v); err != nil {
		t.Fatalf("set business_day_start: %v", err)
	}
}

func localAt(day string, hh, mm, ss int) time.Time {
	d, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		panic(err)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, 0, time.Local)
}

func zNumbers(cs []EODCloseExport) []int64 {
	out := []int64{}
	for _, c := range cs {
		out = append(out, c.ZNumber)
	}
	return out
}

func eqInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// T1: with the default (or unparseable) setting nothing changes: the
// boundary is local midnight with no -1 second.
func TestEODClosesForExport_DefaultBoundaryUnchanged(t *testing.T) {
	const d, d1 = "2025-06-10", "2025-06-11"
	for _, setting := range []string{"<unset>", "", "4:00", "25:00", "ab", "00:00"} {
		t.Run("setting="+setting, func(t *testing.T) {
			dbx := newPOSLifecycleTestDB(t)
			ctx := context.Background()
			setBusinessDayStart(t, dbx, setting)
			seedEODRow(t, dbx, 1, localAt(d, 23, 59, 59).Format(time.RFC3339), localAt(d, 23, 59, 59), `{}`)
			seedEODRow(t, dbx, 2, localAt(d1, 0, 0, 0).Format(time.RFC3339), localAt(d1, 0, 0, 0), `{}`)
			seedEODRow(t, dbx, 3, localAt(d1, 0, 30, 0).Format(time.RFC3339), localAt(d1, 0, 30, 0), `{}`)
			seedEODRow(t, dbx, 4, localAt(d1, 12, 0, 0).Format(time.RFC3339), localAt(d1, 12, 0, 0), `{}`)

			for _, rg := range []struct {
				from, to string
				want     []int64
			}{{d, d, []int64{1}}, {d1, d1, []int64{2, 3, 4}}} {
				got, err := dbx.repo.EODClosesForExport(ctx, rg.from, rg.to)
				if err != nil {
					t.Fatal(err)
				}
				if !eqInts(zNumbers(got), rg.want) {
					t.Fatalf("[%s,%s] = %v, want %v", rg.from, rg.to, zNumbers(got), rg.want)
				}
				old, err := dbx.repo.ArchivedReportsInRange(ctx, rg.from, rg.to)
				if err != nil {
					t.Fatal(err)
				}
				oldZ := []int64{}
				for _, o := range old {
					oldZ = append(oldZ, o.ZNumber)
				}
				if !eqInts(oldZ, rg.want) {
					t.Fatalf("ArchivedReportsInRange diverged: %v want %v", oldZ, rg.want)
				}
			}
		})
	}
}

// T2: business_day_start 04:00 moves closes before 04:00 to the previous day.
func TestEODClosesForExport_TradingDateShift(t *testing.T) {
	const d, d1 = "2025-06-10", "2025-06-11"
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	setBusinessDayStart(t, dbx, "04:00")
	seedEODRow(t, dbx, 1, localAt(d1, 0, 30, 0).Format(time.RFC3339), localAt(d1, 0, 30, 0), `{}`)
	seedEODRow(t, dbx, 2, localAt(d1, 3, 59, 59).Format(time.RFC3339), localAt(d1, 3, 59, 59), `{}`)
	seedEODRow(t, dbx, 3, localAt(d1, 4, 0, 0).Format(time.RFC3339), localAt(d1, 4, 0, 0), `{}`)

	got, err := dbx.repo.EODClosesForExport(ctx, d, d)
	if err != nil {
		t.Fatal(err)
	}
	if !eqInts(zNumbers(got), []int64{1, 2}) {
		t.Fatalf("[D,D] = %v, want [1 2]", zNumbers(got))
	}
	for _, c := range got {
		if c.TradingDate != d {
			t.Fatalf("z%d trading_date = %q, want %s", c.ZNumber, c.TradingDate, d)
		}
	}
	got, err = dbx.repo.EODClosesForExport(ctx, d1, d1)
	if err != nil {
		t.Fatal(err)
	}
	if !eqInts(zNumbers(got), []int64{3}) || got[0].TradingDate != d1 {
		t.Fatalf("[D+1,D+1] = %+v, want only z3 with trading_date %s", got, d1)
	}
}

// T3: a legacy bare-date close is never shifted.
func TestEODClosesForExport_LegacyDayCloseNotShifted(t *testing.T) {
	const d, dPrev = "2025-06-10", "2025-06-09"
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	setBusinessDayStart(t, dbx, "04:00")
	seedEODRow(t, dbx, 1, d, localAt(d, 0, 20, 0), `{"day":"`+d+`"}`)

	got, err := dbx.repo.EODClosesForExport(ctx, d, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TradingDate != d {
		t.Fatalf("[D,D] = %+v, want the legacy close with trading_date %s", got, d)
	}
	got, err = dbx.repo.EODClosesForExport(ctx, dPrev, dPrev)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("[D-1,D-1] = %+v, want none", got)
	}
}

// T4: a settings read error fails the export (never falls back to 00:00).
func TestEODClosesForExport_SettingsReadErrorFails(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedEODRow(t, dbx, 1, "2025-06-10", localAt("2025-06-10", 12, 0, 0), `{}`)
	if _, err := dbx.d.DB.ExecContext(ctx, `ALTER TABLE settings RENAME TO settings_x`); err != nil {
		t.Fatalf("break settings: %v", err)
	}
	got, err := dbx.repo.EODClosesForExport(ctx, "2025-06-10", "2025-06-10")
	if err == nil {
		t.Fatalf("want an error when the setting cannot be read, got %+v", got)
	}
}

// T5/T7: local-not-UTC and wall-clock DST behaviour, in fresh processes with
// a pre-set TZ (modernc sqlite caches the process TZ on first use).
func TestEODClosesForExport_TradingDateLocalNotUTC(t *testing.T) {
	if os.Getenv("UT_TEST_EOD_TZ") == "1" {
		runEODTradingDateLocalBody(t)
		return
	}
	for _, tc := range []struct{ tz, offset string }{{"Asia/Tokyo", "+09:00"}, {"Europe/London", "+01:00"}} {
		t.Run(tc.tz, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestEODClosesForExport_TradingDateLocalNotUTC$", "-test.v")
			cmd.Env = append(os.Environ(), "UT_TEST_EOD_TZ=1", "TZ="+tc.tz, "UT_TEST_EOD_OFFSET="+tc.offset)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "--- PASS") {
				t.Fatalf("subprocess (TZ=%s) failed: err=%v\n%s", tc.tz, err, out)
			}
		})
	}
}

func runEODTradingDateLocalBody(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	setBusinessDayStart(t, dbx, "04:00")

	closedAt := localAt("2025-07-02", 0, 30, 0)
	period := closedAt.Format(time.RFC3339)
	seedEODRow(t, dbx, 1, period, closedAt, `{"to":"`+period+`"}`)

	got, err := dbx.repo.EODClosesForExport(ctx, "2025-07-01", "2025-07-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("[07-01] = %+v, want the after-midnight close (local trading day 07-01)", got)
	}
	c := got[0]
	if c.TradingDate != "2025-07-01" {
		t.Fatalf("trading_date = %q, want 2025-07-01", c.TradingDate)
	}
	if c.ClosedAt != c.Report.To || c.ClosedAt != period {
		t.Fatalf("closed_at = %q, report.to = %q, want both %q", c.ClosedAt, c.Report.To, period)
	}
	if !strings.HasSuffix(c.ClosedAt, os.Getenv("UT_TEST_EOD_OFFSET")) {
		t.Fatalf("closed_at %q must carry the local offset %s", c.ClosedAt, os.Getenv("UT_TEST_EOD_OFFSET"))
	}
	got, err = dbx.repo.EODClosesForExport(ctx, "2025-07-02", "2025-07-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("[07-02] = %+v, want none", got)
	}

	// T7: wall-clock subtraction across the UK spring-forward day (the
	// SalesByDay convention): 02:10 BST minus 01:30 is 00:40 on 03-30.
	if os.Getenv("TZ") == "Europe/London" {
		setBusinessDayStart(t, dbx, "01:30")
		at := time.Date(2025, 3, 30, 2, 10, 0, 0, time.Local) // BST began 2025-03-30
		seedEODRow(t, dbx, 2, at.Format(time.RFC3339), at, `{}`)
		got, err := dbx.repo.EODClosesForExport(ctx, "2025-03-30", "2025-03-30")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].TradingDate != "2025-03-30" {
			t.Fatalf("spring-forward close = %+v, want trading_date 2025-03-30", got)
		}
	}
}
