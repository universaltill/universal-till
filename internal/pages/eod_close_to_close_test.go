package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0066 / ut-docs#1141 acceptance-criteria regression coverage: the
// close-to-close EOD window, wired end-to-end through generateEOD/
// eodSchedulerTick/the manual-run and reprint handlers/the Reports page/the
// archive export, rather than only at the internal/data query layer
// (already covered by ut-docs#1140's eod_instant_window_test.go).

// ectcSeedSale inserts a minimal completed sale row directly with an
// explicit created_at, matching the RFC3339 form internal/pos/sales.go's
// real insert path writes (data_api_test.go's own direct-SQL sale seeding
// uses the same shape for the schema-default 'now' case; this one needs a
// controlled instant instead).
func ectcSeedSale(t *testing.T, dp *common.Deps, id, receiptNo string, total int64, createdAt time.Time) {
	t.Helper()
	if _, err := dp.Db.Exec(`INSERT INTO sales
(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES (?, ?, 'completed', 'sale', 'GBP', ?, 0, 0, ?, ?)`,
		id, receiptNo, total, total, createdAt.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed sale %s: %v", id, err)
	}
}

// TestGenerateEOD_AbuttingWindowsNoDoubleCountNoGap is the end-to-end wiring
// proof ADR-0066 requires: two REAL successive generateEOD calls (not a
// direct dateRangeSummaryInstant call, which ut-docs#1140 already covers)
// must produce abutting, non-overlapping windows — the second close's
// aggregation must not re-count what the first already covered, AND a sale
// landing exactly at the first close's boundary must not fall through the
// gap between the two (the half-open [from, to) rule, ADR-0066 Decision 2).
func TestGenerateEOD_AbuttingWindowsNoDoubleCountNoGap(t *testing.T) {
	dp := newEODTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)

	// A safety margin below "now" (query comparisons are whole-second
	// granularity — reportWindowFmt/instantWindow — so a sale landing in
	// the SAME second as the close instant that follows it would be a
	// flaky, not a real, test of the window boundary).
	ectcSeedSale(t, dp, "ectc-s1", "ECTC-R1", 1000, time.Now().Add(-5*time.Second))

	// The till's first-ever close: from is unbounded (ADR-0066 Decision 3).
	rep1, created1, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created1 {
		t.Fatal("expected the first close to be created")
	}
	if rep1.From != "" {
		t.Fatalf("expected an unbounded From on the till's first-ever close, got %q", rep1.From)
	}
	if rep1.SalesCount != 1 || rep1.Gross != 1000 {
		t.Fatalf("expected the first close to cover exactly sale 1 (1/1000), got %d/%d", rep1.SalesCount, rep1.Gross)
	}

	// The SECOND close's `from` is read back via LatestArchivedAt off the
	// first close's own stored created_at (ADR-0066 Decision 5's
	// clock-skew fix) -- capture the REAL boundary production code will
	// use, not the in-Go `to` generateEOD captured moments earlier (which
	// may differ from the stored value by sub-second storage rounding).
	boundary, err := repo.LatestArchivedAt(t.Context(), "eod")
	if err != nil || boundary == nil {
		t.Fatalf("expected a stored boundary after the first close, got %v err=%v", boundary, err)
	}

	// A sale landing EXACTLY at that boundary belongs to the SECOND close
	// (half-open [from, to) -- the same boundary rule
	// TestDateRangeSummaryInstant_HalfOpenInstantBoundary pins at the query
	// layer): a naive `>` instead of `>=` lower bound on the next close
	// would silently lose this sale in the gap between the two closes.
	ectcSeedSale(t, dp, "ectc-s2", "ECTC-R2", 2500, *boundary)

	// Give the wall clock a full second of headroom past the boundary
	// before the second close's `to` is captured -- otherwise `to` could
	// land in the SAME whole second as the boundary sale's created_at,
	// which the exclusive upper bound (`< to`) would then wrongly exclude,
	// making this a test of storage-precision timing rather than of the
	// window wiring this test is actually about.
	time.Sleep(1100 * time.Millisecond)

	rep2, _, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// created2 (whether the second close was actually ARCHIVED) is
	// deliberately not asserted: a second close on the SAME real local day
	// this test runs on hits ArchiveReport's own atomic double-close guard
	// (ADR-0066 Decision 4, already proven correct under real concurrency
	// by ut-docs#1140's TestArchiveReport_ConcurrentSameLocalDayDoubleClose)
	// -- generateEOD still computes and returns rep2 from a real window
	// BEFORE attempting the archive write, which is what this test is
	// about: the window's CONTENT, not whether a second same-day row lands.
	if rep2.SalesCount != 1 || rep2.Gross != 2500 {
		t.Fatalf("expected the second close to cover EXACTLY the boundary sale (1/2500), not re-counting sale 1, got %d/%d",
			rep2.SalesCount, rep2.Gross)
	}
	if rep2.From == "" {
		t.Fatal("expected a non-empty From on the second close (not the till's first)")
	}
}

// TestReportsPage_EODRowPeriodDisplay_LegacyBareDateVsCloseToCloseRange
// covers ADR-0066/ut-docs#1141 acceptance criteria (b) and (c) together:
// a freshly generated close-to-close report ("eod" kind, Day=="", From/To
// set — NOT the till's first close) must render the "reports.eod.
// period_range"-formatted en-dash-joined From – To text on the Reports
// page, while a pre-cutover legacy calendar-date row (Day set, From/To
// empty) sitting in the SAME table must keep rendering its bare Period
// exactly as before — no cross-contamination between the two rows' template
// branches.
func TestReportsPage_EODRowPeriodDisplay_LegacyBareDateVsCloseToCloseRange(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := context.Background()

	// Legacy calendar-date row: Day set, From/To empty in the archived
	// content -- the template must keep showing its bare Period.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json)
VALUES('r-legacy','eod','2020-06-15','{"day":"2020-06-15","sales_count":4,"net":800}')`); err != nil {
		t.Fatal(err)
	}
	// Fresh close-to-close row: Day empty, From/To set (not the till's
	// first close) -- the template must render the "From – To" range.
	// Plain "Z"-suffixed timestamps here, not a "+02:00" local offset —
	// this test is about the template's From-set-vs-empty branching, not
	// about offset display, and html/template HTML-escapes "+" to "&#43;"
	// in text context, which would just be noise in this assertion.
	from := "2026-08-23T19:10:00Z"
	to := "2026-08-24T19:19:00Z"
	content := fmt.Sprintf(`{"from":%q,"to":%q,"sales_count":3,"net":500}`, from, to)
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r-ctc','eod',?,?)`,
		to, content); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "eod", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "2020-06-15") {
		t.Fatalf("expected the legacy row's bare period, got: %s", body)
	}
	wantRange := from + " – " + to
	if !strings.Contains(body, wantRange) {
		t.Fatalf("expected the close-to-close row rendered as %q, got: %s", wantRange, body)
	}
	// The legacy row's bare period must NOT get joined with an en dash --
	// confirms its .From stayed empty (the template's {{ if .From }}
	// branch correctly fell through to the bare {{ .Period }} for THIS
	// row specifically, not just in isolation).
	if strings.Contains(body, "2020-06-15 – ") {
		t.Fatalf("legacy row must not render a From – To range, got: %s", body)
	}
}

// TestOldArchivedReports_ListAndReprintUnaffectedByCutover is ADR-0066/
// ut-docs#1141 acceptance criterion (c): a pre-cutover legacy archived
// report (calendar-date period, zero closedAt — exactly how generateEOD
// wrote every row before this ADR) must keep listing and reprinting
// correctly once new-format ("eod" kind, close-instant period) rows exist
// alongside it — the two period conventions must coexist, not just the new
// one working in isolation.
func TestOldArchivedReports_ListAndReprintUnaffectedByCutover(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)
	repo := data.NewPOSRepo(dp.Db)

	// Seeded via direct SQL, not ArchiveReport with a zero closedAt: a zero
	// closedAt stamps the schema default's real "now" into created_at,
	// which would coincidentally land on the SAME local day as the fresh
	// close below and wrongly trip ArchiveReport's same-local-day
	// double-close guard (ADR-0066 Decision 4) — the guard reads
	// created_at, not period, so a period of "2020-06-15" alone doesn't
	// protect against that. An explicit historical created_at (matching
	// how a genuinely pre-cutover row's timestamp would read) sidesteps it.
	if _, err := dp.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('legacy-list-reprint', 'eod', '2020-06-15', '{"day":"2020-06-15","sales_count":4,"net":800}', '2020-06-15 10:00:00')`); err != nil {
		t.Fatalf("seed legacy close: %v", err)
	}
	rep, created, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected the fresh close created")
	}

	// ListArchivedReports returns both, unaffected by the period format mix
	// (ADR-0066 Decision 4's sort-safety claim: a bare date is a strict
	// text prefix of same-day RFC3339, so ORDER BY period never corrupts).
	rows, err := repo.ListArchivedReports(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both the legacy and fresh close listed, got %+v", rows)
	}
	var haveLegacy, haveFresh bool
	for _, r := range rows {
		if r.Period == "2020-06-15" {
			haveLegacy = true
		}
		if r.Period == rep.To {
			haveFresh = true
		}
	}
	if !haveLegacy || !haveFresh {
		t.Fatalf("expected both periods listed, got legacy=%v fresh=%v rows=%+v", haveLegacy, haveFresh, rows)
	}

	// Reprinting the LEGACY period still routes correctly through the same
	// handler the new-format period now uses (no printer configured -> the
	// same 502 TestPostEODPrint_NoPrinterConfiguredFailsGracefully proves
	// for a fresh close) — the legacy bare-date period isn't broken by the
	// close-instant convention now living alongside it in the same table.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/print/2020-06-15", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (no printer configured) reprinting the legacy period, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPostReportArchiveExport_MixedLegacyAndNewFormatClosesBothIncluded is
// ADR-0066 Decision 5's own named regression: "an RFC3339 period falling on
// the export range's own last day sorts after that bare date bound and is
// silently excluded — a Betriebsprüfer's export request covering the exact
// day of a new-format close would come back missing that close, with a 200
// and no error." Seeds a legacy row earlier in the range and a new-format
// row (via the real ArchiveReport write path, a real closedAt) landing
// exactly on the query range's own LAST day, and proves both come back —
// the exact scenario the old `period BETWEEN` text filter would have
// silently broken for the new-format row. (The two rows are deliberately
// on DIFFERENT calendar days, not the same one: ArchiveReport's
// same-local-day double-close guard, ADR-0066 Decision 4, would otherwise
// block a second "eod" archival on the legacy row's own day regardless of
// format — that guard is proven separately by ut-docs#1140's
// TestArchiveReport_ConcurrentSameLocalDayDoubleClose; this test is about
// the EXPORT RANGE FILTER, not the write-time guard.)
func TestPostReportArchiveExport_MixedLegacyAndNewFormatClosesBothIncluded(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)

	// Legacy pre-cutover row: period is a bare calendar date, created_at in
	// the schema's pre-ADR-0066 space-separated form, well inside the
	// range but NOT on its last day.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('legacy-mixed', 'eod', '2026-08-10', '{"day":"2026-08-10"}', '2026-08-10 20:00:00')`); err != nil {
		t.Fatal(err)
	}

	// New-format row: a real close instant landing on the range's own LAST
	// day (2026-08-24). LOCAL noon with a local-offset RFC3339 period —
	// exactly what generateEOD writes, and what the range filter's
	// datetime(created_at, 'localtime') comparison bounds against, so this
	// holds in every host timezone rather than only under CI's TZ=UTC
	// (2026-09-04 review of ut-docs#1141). The regression pinned here is
	// unaffected: a local-offset RFC3339 period still sorts AFTER the bare
	// date bound "2026-08-24" under the old `period BETWEEN` filter.
	newClosedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.Local)
	newPeriod := newClosedAt.Format(time.RFC3339)
	created, err := repo.ArchiveReport(ctx, "eod", newPeriod, []byte(`{"to":"`+newPeriod+`"}`), "", "", newClosedAt)
	if err != nil || !created {
		t.Fatalf("archive new-format close: created=%v err=%v", created, err)
	}

	// A raw `period BETWEEN "2026-08-01" AND "2026-08-24"` text compare
	// would have EXCLUDED newPeriod (an RFC3339 string sorting after the
	// bare date bound "2026-08-24") even though it genuinely falls on the
	// range's own last day.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/archive/export",
		strings.NewReader("from=2026-08-01&to=2026-08-24&format=json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var rows []data.ArchivedReportRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("expected a JSON array body: %v (%s)", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("expected BOTH the legacy and new-format close in the export, got %d: %+v", len(rows), rows)
	}
	var gotLegacy, gotNew bool
	for _, r := range rows {
		if r.Period == "2026-08-10" {
			gotLegacy = true
		}
		if r.Period == newPeriod {
			gotNew = true
		}
	}
	if !gotLegacy || !gotNew {
		t.Fatalf("expected both periods present, got legacy=%v new=%v rows=%+v", gotLegacy, gotNew, rows)
	}
}
