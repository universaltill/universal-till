package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// --- pure logic ---
// eodDue and buildEODDoc already have coverage in eod_test.go
// (TestEODDue, TestBuildEODDoc) — only adding what that file doesn't
// cover: direct regex verification and the no-sales-yet footer case.

// ADR-0040 §2 / ut-docs#571 card 1: pure logic behind the report_archive
// retention prune step hosted on StartEODScheduler's own tick.

func TestReportPruneDue(t *testing.T) {
	cases := []struct {
		today, lastPruneDay string
		want                bool
	}{
		{"2026-08-12", "", true},            // never run yet today
		{"2026-08-12", "2026-08-11", true},  // last run was a previous day
		{"2026-08-12", "2026-08-12", false}, // already ran today
		{"2026-01-01", "2025-12-31", true},  // year boundary
	}
	for _, c := range cases {
		if got := reportPruneDue(c.today, c.lastPruneDay); got != c.want {
			t.Errorf("reportPruneDue(%q, %q) = %v, want %v", c.today, c.lastPruneDay, got, c.want)
		}
	}
}

func TestReportRetentionCutoff_TenYearsBackFormattedAsPeriod(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 4, 5, 0, time.UTC)
	got := reportRetentionCutoff(now)
	want := "2016-08-12"
	if got != want {
		t.Fatalf("reportRetentionCutoff(%v) = %q, want %q", now, got, want)
	}
}

// ut-docs#659 review finding B2: reportRetentionCutoff (calendar years) and
// data.GlobalArchiveMinDays (a fixed day count, ADR-0040's single global
// floor enforced by country_settings.go's Upsert) are two independent
// spellings of "10 years" with nothing tying them together before this
// test. Pins the relationship that actually matters: the till never prunes
// a report_archive row sooner than the floor promises. Checked across a
// spread of "now" dates so a single-year sample can't hide a case where
// leap days push the calendar window *under* 3650 (they can't, by
// construction — every 10-year span contains at least 2 leap days — but
// this asserts it rather than assuming it).
func TestReportRetentionCutoffNeverShorterThanGlobalArchiveMinDays(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC),  // spans leap years 2020, 2024
		time.Date(2026, 8, 12, 15, 4, 5, 0, time.UTC), // spans leap years 2020, 2024
		time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),   // 2100 is NOT a Gregorian leap year
		time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),   // spans leap years 1996, 2000
	} {
		cutoff, err := time.Parse("2006-01-02", reportRetentionCutoff(now))
		if err != nil {
			t.Fatalf("reportRetentionCutoff(%v) did not parse as a period: %v", now, err)
		}
		spanDays := int64(now.Sub(cutoff).Hours() / 24)
		if spanDays < data.GlobalArchiveMinDays {
			t.Errorf("now=%v: retention window is only %d days, want >= GlobalArchiveMinDays (%d) -- "+
				"the till would prune a row before the country_settings floor promises it's kept",
				now, spanDays, data.GlobalArchiveMinDays)
		}
	}
}

// pruneReportArchive is the actual (non-goroutine) step StartEODScheduler
// calls each tick — testable directly without driving the 30s ticker loop.

func TestPruneReportArchive_TillModePastCutoffDeletesOldRows(t *testing.T) {
	dp := newEODTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)

	if _, err := repo.ArchiveReport(t.Context(), "eod", "2010-01-01", []byte(`{}`)); err != nil {
		t.Fatalf("seed old archive: %v", err)
	}
	if _, err := repo.ArchiveReport(t.Context(), "eod", "2026-01-01", []byte(`{}`)); err != nil {
		t.Fatalf("seed recent archive: %v", err)
	}
	// Default mode is till (unset) — explicit here for clarity.
	if err := dp.Settings.Set(t.Context(), common.KeyReportRetentionMode, "till"); err != nil {
		t.Fatal(err)
	}

	lastPruneDay := ""
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	pruneReportArchive(t.Context(), dp, repo, now, &lastPruneDay)

	if lastPruneDay != "2026-08-12" {
		t.Fatalf("expected lastPruneDay updated to today, got %q", lastPruneDay)
	}
	reports, err := repo.ListArchivedReports(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Period != "2026-01-01" {
		t.Fatalf("expected only the recent report to survive, got %+v", reports)
	}

	// A second call the same day is a no-op (gated by lastPruneDay) — seed
	// another old row and confirm it's NOT pruned until a new day.
	if _, err := repo.ArchiveReport(t.Context(), "eod", "2011-01-01", []byte(`{}`)); err != nil {
		t.Fatalf("seed another old archive: %v", err)
	}
	pruneReportArchive(t.Context(), dp, repo, now, &lastPruneDay)
	if has, _ := repo.HasArchivedReport(t.Context(), "eod", "2011-01-01"); !has {
		t.Fatal("expected the second same-day call to be a no-op (gated), but the row was pruned")
	}
}

func TestPruneReportArchive_CloudModeIsNoOpThisCard(t *testing.T) {
	dp := newEODTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)

	if _, err := repo.ArchiveReport(t.Context(), "eod", "2010-01-01", []byte(`{}`)); err != nil {
		t.Fatalf("seed old archive: %v", err)
	}
	if err := dp.Settings.Set(t.Context(), common.KeyReportRetentionMode, "cloud"); err != nil {
		t.Fatal(err)
	}

	lastPruneDay := ""
	pruneReportArchive(t.Context(), dp, repo, time.Now(), &lastPruneDay)

	if has, err := repo.HasArchivedReport(t.Context(), "eod", "2010-01-01"); err != nil || !has {
		t.Fatalf("cloud mode must not prune anything in this card, got has=%v err=%v", has, err)
	}
	if lastPruneDay == "" {
		t.Fatal("expected lastPruneDay still advanced (gate applies regardless of mode) even though nothing was pruned")
	}
}

func TestEodTimeRegex(t *testing.T) {
	valid := []string{"00:00", "09:05", "23:59", "21:00"}
	invalid := []string{"24:00", "9:05", "23:60", "abc", "", "23:5"}
	for _, v := range valid {
		if !eodTimeRe.MatchString(v) {
			t.Errorf("expected %q to be a valid HH:MM", v)
		}
	}
	for _, v := range invalid {
		if eodTimeRe.MatchString(v) {
			t.Errorf("expected %q to be rejected", v)
		}
	}
}

func TestBuildEODDoc_NoReceiptsOmitsFooterLine(t *testing.T) {
	rep := data.EODReport{Day: "2026-01-01", GeneratedAt: "now"}
	doc := buildEODDoc(rep, "Task Runner", "utf8")
	for _, line := range doc.Footer {
		if strings.Contains(line, "Receipts") {
			t.Fatalf("expected no receipt-range footer line for a day with no sales, got %+v", doc.Footer)
		}
	}
}

// StartEODScheduler must register its goroutine on the caller's wg
// (ut-docs#153), same join shape as cloudsync.Start, so app.Run's shutdown
// drain can prove it exited before database.Close() runs.
func TestStartEODScheduler_JoinsWaitGroupAndExitsOnCtxCancel(t *testing.T) {
	dp := newEODTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartEODScheduler(ctx, dp, &wg)

	// wg.Wait() on a zero counter returns immediately, so without this
	// pre-cancel check the test would pass even if StartEODScheduler never
	// called wg.Add at all. Confirm the counter is genuinely non-zero
	// before cancelling.
	registered := make(chan struct{})
	go func() { wg.Wait(); close(registered) }()
	select {
	case <-registered:
		t.Fatal("wg.Wait() returned before ctx was even cancelled — StartEODScheduler never called wg.Add, so this test cannot prove the join")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartEODScheduler's goroutine did not call wg.Done() within 2s of ctx cancel — not joined to the shutdown drain")
	}
}

// --- generateEOD (idempotent archival) ---

func newEODTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
}

func TestGenerateEOD_ArchivesOnceThenIdempotent(t *testing.T) {
	dp := newEODTestDeps(t)

	_, created, err := generateEOD(t.Context(), dp, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected the first generation for this day to report created=true")
	}

	// Running it again for the SAME day must not re-archive (idempotent —
	// StartEODScheduler polls every 30s and must not spam a fresh report
	// each tick).
	_, created, err = generateEOD(t.Context(), dp, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected the second generation for the same day to report created=false")
	}

	repo := data.NewPOSRepo(dp.Db)
	has, err := repo.HasArchivedReport(t.Context(), "eod", "2026-01-01")
	if err != nil || !has {
		t.Fatalf("expected the report archived, got has=%v err=%v", has, err)
	}
}

// --- HTTP handlers ---

func newEODAPITestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	dp := newEODTestDeps(t)
	mux := http.NewServeMux()
	registerEODAPI(mux, dp)
	registerReportArchiveAPI(mux, dp)
	return mux, dp
}

func TestPostEODRun_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

// Positive counterpart to TestPostEODRun_RequiresManager with a REAL session
// (ut-docs#709 review finding — every other test in this file either uses
// UT_AUTH=off or no session, so none of them ever exercised canPerform's
// real d.AuthSvc.Can() path; this is the only test in the file that does).
// super_admin is #554/#555's noted broadening vs. the old isManagerOrAuthOff
// gate — accepted and inert since nothing today creates that role.
func TestPostEODRun_RealSessionGatesByRole(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for role, wantCode := range map[string]int{
		"cashier": http.StatusForbidden, "manager": http.StatusOK,
		"admin": http.StatusOK, "super_admin": http.StatusOK,
	} {
		t.Run(role, func(t *testing.T) {
			mux, _ := newEODAPITestMux(t)
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil), auth.User{ID: "u1", Role: role})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != wantCode {
				t.Fatalf("role=%s: expected %d, got %d: %s", role, wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPostEODRun_GeneratesThenReportsAlreadyExists(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected a success indicator, got %s", rec.Body.String())
	}

	// Running again the same day: still 200, but reports "already exists"
	// rather than a second success.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on the second run, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected NOT a fresh success indicator on the second same-day run, got %s", rec.Body.String())
	}
}

func TestPostEODPrint_NotFoundForUnknownPeriod(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/print/2099-12-31", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a period that was never archived, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostEODPrint_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/print/2026-01-01", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostEODPrint_NoPrinterConfiguredFailsGracefully(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)

	// Archive a report first.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: expected 200 generating the report, got %d", rec.Code)
	}
	today := time.Now().Format("2006-01-02")

	// No printer configured (printer.mode defaults to "off") — reprinting
	// must fail cleanly (502), not panic or hang trying to reach hardware.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reports/eod/print/"+today, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 with no printer configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostEODRange_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/range", strings.NewReader("from=2026-01-01&to=2026-01-31"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostEODRange_ValidatesFromTo(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)

	cases := []struct {
		name, body string
	}{
		{"missing from", "to=2026-01-31"},
		{"missing to", "from=2026-01-01"},
		{"from after to", "from=2026-02-01&to=2026-01-01"},
		// Malformed dates must be rejected outright, not silently reach the
		// SQL BETWEEN as raw text (2026-08-02 review finding): an un-padded
		// date matches nothing (silently reports zero sales for real days),
		// and non-date garbage in `to` sorts after every real date and
		// silently widens the range with no error.
		{"unpadded from", "from=2026-1-1&to=2026-01-31"},
		{"garbage to", "from=2026-01-01&to=not-a-date"},
		{"garbage from", "from=not-a-date&to=2026-01-31"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/range", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestPostEODRange_DownloadsJSONReport(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/range", strings.NewReader("from=2026-01-01&to=2026-01-31"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "z-report-2026-01-01-to-2026-01-31.json") {
		t.Fatalf("expected an attachment Content-Disposition naming the range, got %q", cd)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content type, got %q", ct)
	}
	var rep data.EODReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("expected valid JSON body: %v (%s)", err, rec.Body.String())
	}
	if rep.From != "2026-01-01" || rep.To != "2026-01-31" {
		t.Fatalf("expected From/To echoed in the body, got %+v", rep)
	}
}

func TestPostSettingsEOD_ValidatesTimeFormat(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("enabled=on&time=not-a-time"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed time when enabled, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("enabled=on&time=22:30"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a valid time, got %d: %s", rec.Code, rec.Body.String())
	}
	val, _, err := dp.Settings.Get(t.Context(), keyEODTime)
	if err != nil || val != "22:30" {
		t.Fatalf("expected the time setting persisted, got %q err=%v", val, err)
	}

	// Disabling doesn't require a valid time at all.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("enabled=off&time="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 disabling with no time, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ut-docs#519: business_day_start is a sibling field on this SAME
// settings panel/endpoint, validated with the same eodTimeRe pattern as the
// EOD schedule time above.
func TestPostSettingsEOD_BusinessDayStart_ValidatesAndPersists(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("business_day_start=not-a-time"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed business_day_start, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("business_day_start=06:00"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a valid business_day_start, got %d: %s", rec.Code, rec.Body.String())
	}
	val, _, err := dp.Settings.Get(t.Context(), keyReportsBusinessDayStart)
	if err != nil || val != "06:00" {
		t.Fatalf("expected business_day_start persisted, got %q err=%v", val, err)
	}

	// Blank is allowed (never configured / cleared back to the default).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("business_day_start="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a blank business_day_start, got %d: %s", rec.Code, rec.Body.String())
	}
	val, _, err = dp.Settings.Get(t.Context(), keyReportsBusinessDayStart)
	if err != nil || val != "" {
		t.Fatalf("expected business_day_start cleared, got %q err=%v", val, err)
	}
}

// --- ADR-0040 §1/§7 / ut-docs#571 card 1: report retention mode + export ---

func TestPostSettingsReportRetention_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/report-retention", strings.NewReader("mode=till"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostSettingsReportRetention_AcceptsTillPersists(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/report-retention", strings.NewReader("mode=till"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for mode=till, got %d: %s", rec.Code, rec.Body.String())
	}
	val, _, err := dp.Settings.Get(t.Context(), common.KeyReportRetentionMode)
	if err != nil || val != "till" {
		t.Fatalf("expected report_retention_mode=till persisted, got %q err=%v", val, err)
	}
}

// This card (ut-docs#571 card 1) implements NO cloud gate at all -- selecting
// cloud/both must be rejected outright with a 400, not silently accepted, per
// the card's explicit scope carve-out.
func TestPostSettingsReportRetention_RejectsCloudAndBoth(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)

	for _, mode := range []string{"cloud", "both", "bogus", ""} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/settings/report-retention", strings.NewReader("mode="+mode))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("mode=%q: expected 400, got %d: %s", mode, rec.Code, rec.Body.String())
		}
		val, _, err := dp.Settings.Get(t.Context(), common.KeyReportRetentionMode)
		if err != nil {
			t.Fatal(err)
		}
		if val == mode && mode != "" {
			t.Fatalf("mode=%q: must not have been persisted", mode)
		}
	}
}

func TestPostReportArchiveExport_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", strings.NewReader("from=2026-01-01&to=2026-01-31&format=json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostReportArchiveExport_ValidatesFromTo(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)

	cases := []struct{ name, body string }{
		{"missing from", "to=2026-01-31&format=json"},
		{"missing to", "from=2026-01-01&format=json"},
		{"from after to", "from=2026-02-01&to=2026-01-01&format=json"},
		{"garbage from", "from=not-a-date&to=2026-01-31&format=json"},
		{"bad format", "from=2026-01-01&to=2026-01-31&format=xml"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestPostReportArchiveExport_JSONAndCSVDownloads(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)

	if _, _, err := generateEOD(t.Context(), dp, "2026-01-15"); err != nil {
		t.Fatalf("setup: generateEOD: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", strings.NewReader("from=2026-01-01&to=2026-01-31&format=json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("json export: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Fatalf("json export: expected attachment Content-Disposition, got %q", cd)
	}
	var rows []data.ArchivedReportRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("json export: expected a JSON array body: %v (%s)", err, rec.Body.String())
	}
	if len(rows) != 1 || rows[0].Period != "2026-01-15" {
		t.Fatalf("json export: expected the seeded 2026-01-15 report, got %+v", rows)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", strings.NewReader("from=2026-01-01&to=2026-01-31&format=csv"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("csv export: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Fatalf("csv export: expected attachment Content-Disposition, got %q", cd)
	}
	if !strings.Contains(rec.Body.String(), "2026-01-15") {
		t.Fatalf("csv export: expected the seeded period in the CSV body, got %s", rec.Body.String())
	}
}

func TestPostReportArchiveExport_EmptyRangeStillDownloadsEmptyFile(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", strings.NewReader("from=2099-01-01&to=2099-01-31&format=json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a valid but empty range, got %d: %s", rec.Code, rec.Body.String())
	}
	var rows []data.ArchivedReportRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("expected a JSON array (possibly empty) body: %v (%s)", err, rec.Body.String())
	}
	if len(rows) != 0 {
		t.Fatalf("expected zero rows for an unmatched range, got %+v", rows)
	}
}
