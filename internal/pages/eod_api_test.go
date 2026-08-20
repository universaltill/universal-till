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
		time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC),   // spans leap years 2020, 2024
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

	_, created, err := generateEOD(t.Context(), dp, "2026-01-01", "system", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected the first generation for this day to report created=true")
	}

	// Running it again for the SAME day must not re-archive (idempotent —
	// StartEODScheduler polls every 30s and must not spam a fresh report
	// each tick).
	_, created, err = generateEOD(t.Context(), dp, "2026-01-01", "system", "")
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

// ut-docs#794 AC: StartEODScheduler's unattended ticker calls
// generateEOD(ctx, d, day, "system", "") literally (no checkOrElevate in
// that path at all — see StartEODScheduler's check() closure), so the
// audit trail this writes must be the plain, non-elevated shape: actor
// "system", no blocked_actor_id. Exercised directly against generateEOD
// (the actual function the scheduler calls) rather than driving the 30s
// ticker, same as TestGenerateEOD_ArchivesOnceThenIdempotent above.
// ut-docs#794 review finding: driving eodSchedulerTick (the ACTUAL function
// StartEODScheduler's ticker calls, extracted so this is possible without
// racing a real 30s ticker) rather than calling generateEOD directly with
// test-supplied ("system", "") args, which only proved generateEOD honors
// whatever it's given — not that the scheduler's own real call site passes
// the right thing. Configures the settings eodDue actually reads (enabled +
// a past time) so the tick genuinely decides to run, the same decision path
// a real till takes at closing time.
func TestEODSchedulerTick_RunsAndWritesPlainSystemAudit(t *testing.T) {
	dp := newEODTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	if err := dp.Settings.Set(t.Context(), keyEODEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), keyEODTime, "00:00"); err != nil {
		t.Fatal(err)
	}

	eodSchedulerTick(t.Context(), dp, repo)

	today := time.Now().Format("2006-01-02")
	has, err := repo.HasArchivedReport(t.Context(), "eod", today)
	if err != nil || !has {
		t.Fatalf("expected the tick to have archived today's report, got has=%v err=%v", has, err)
	}

	var actorID string
	var blockedActorID *string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='eod_generated'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected an eod_generated audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("actor_id = %q, want %q (the scheduler's unattended run must never be attributed to a real user)", actorID, "system")
	}
	if blockedActorID != nil && *blockedActorID != "" {
		t.Fatalf("blocked_actor_id = %q, want empty/NULL — the scheduler path never elevates", *blockedActorID)
	}
}

// The disabled/not-due branch of the same real call site must NOT generate
// anything — pins that eodSchedulerTick's eodDue gate is actually consulted,
// not bypassed.
func TestEODSchedulerTick_NotDueGeneratesNothing(t *testing.T) {
	dp := newEODTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	// keyEODEnabled left unset (disabled) — eodDue must report false.

	eodSchedulerTick(t.Context(), dp, repo)

	today := time.Now().Format("2006-01-02")
	has, err := repo.HasArchivedReport(t.Context(), "eod", today)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("expected no report archived when EOD is disabled")
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

// newElevationTestPrincipals seeds a real manager (with a working PIN) and a
// blocked cashier session (ut-docs#794), the same pair every elevated-path
// test in this file needs — factored out of
// TestPostEODRun_ElevatesOnValidApproverPIN so the 5 sibling sites below
// don't each repeat the boilerplate.
func newElevationTestPrincipals(t *testing.T, dp *common.Deps, mgrUsername, cashierUsername, pin string) (mgrID, blockedID string) {
	t.Helper()
	dp.AuthSvc = auth.NewService(dp.Db) // canPerform() needs it non-nil once a real session reaches it
	authRepo := data.NewAuthRepo(dp.Db)
	ctx := t.Context()

	mgrID, err := authRepo.CreateUser(ctx, mgrUsername, "EOD Manager", "manager")
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	hash, err := auth.HashPIN(pin)
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, mgrID, hash); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	blockedID, err = authRepo.CreateUser(ctx, cashierUsername, "Blocked Cashier", "cashier")
	if err != nil {
		t.Fatalf("create cashier: %v", err)
	}
	return mgrID, blockedID
}

// ut-docs#794: POST /api/reports/eod/run moved off the flat 403 onto
// checkOrElevate — a denied caller now gets the in-place elevation prompt
// (200, htmx-swappable), same shape as ut-docs#557's 3 original sites.
func TestPostEODRun_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") || !strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Fatalf("expected the elevation prompt dialog, got: %s", rec.Body.String())
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
	// ut-docs#794: every role now gets 200 (checkOrElevate never flat-403s)
	// — a denied role's 200 carries the elevation prompt instead of the
	// generated report, so what distinguishes "denied" from "allowed" now
	// is the response body, not the status code.
	allowed := map[string]bool{
		"cashier": false, "manager": true, "admin": true, "super_admin": true,
	}
	for role, wantAllowed := range allowed {
		t.Run(role, func(t *testing.T) {
			mux, _ := newEODAPITestMux(t)
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil), auth.User{ID: "u1", Role: role})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("role=%s: expected 200, got %d: %s", role, rec.Code, rec.Body.String())
			}
			gotElevationPrompt := strings.Contains(rec.Body.String(), "elevation-dialog")
			if gotElevationPrompt == wantAllowed {
				t.Fatalf("role=%s: expected elevation prompt=%v, got body: %s", role, !wantAllowed, rec.Body.String())
			}
		})
	}
}

// ut-docs#794: a cashier session denied eod_report gets past the gate with a
// valid manager approver PIN — the report is attributed to the approver, and
// the audit trail records both (dual attribution), same as ut-docs#557's
// TestBackupNow_ElevatesOnValidApproverPIN.
func TestPostEODRun_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp := newEODAPITestMux(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-eod", "blocked-cashier-eod", "445566")

	form := strings.NewReader("override_pin=445566")
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", form), auth.User{ID: blockedID, Role: "cashier"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected a success indicator, got: %s", rec.Body.String())
	}

	var actorID, blockedActorID string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='eod_generated'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected an eod_generated audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
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
	mux, dp := newEODAPITestMux(t)
	// ut-docs#794 review finding (nit): the period is now validated to
	// exist BEFORE elevating (don't burn a PIN entry on a request that
	// 404s either way), so this test needs a real archived report or it
	// 404s before ever reaching checkOrElevate — seed one directly.
	if _, _, err := generateEOD(t.Context(), dp, "2026-01-01", "system", ""); err != nil {
		t.Fatalf("setup: generateEOD: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/print/2026-01-01", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("expected the elevation prompt dialog, got: %s", rec.Body.String())
	}
}

// ut-docs#794 review finding (should-fix — partially addressed): unlike the
// other 5 sites, print/{period}'s elevated-path + audit write is NOT
// exercised here — its success path requires a real/fake configured
// printer to ever get past printerConfig(ctx,d).Enabled(), and this file
// (like the rest of the package) has no such test double today; only the
// "no printer configured" 502 branch is covered, pre-existing and
// unrelated to elevation. checkOrElevate's needsElevation branch IS
// covered (TestPostEODPrint_RequiresManager), same as every other site.
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

// ut-docs#794: this endpoint is a raw fetch() caller (file download via
// Content-Disposition, not htmx — see app.js's utPostWithElevation), but
// the SERVER-side gate is the identical checkOrElevate shape as every
// htmx-driven site: 200 + the elevation dialog HTML, distinguished from a
// real error by the explicit Content-Type text/html renderElevationPrompt
// sets (elevation.go).
func TestPostEODRange_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/eod/range", strings.NewReader("from=2026-01-01&to=2026-01-31"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html so the raw-fetch caller can distinguish this from the real JSON download, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("expected the elevation prompt dialog, got: %s", rec.Body.String())
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

// ut-docs#794 review finding (should-fix): the elevated-path proof for a
// raw-fetch (non-htmx) site — the server-side gate is identical to the
// htmx-driven sites', so this exercises the SAME checkOrElevate/
// InsertAuditElevated code as TestPostEODRun_ElevatesOnValidApproverPIN,
// just reached the way app.js's utPostWithElevation actually calls it (a
// plain form-encoded POST with override_pin alongside the replayed
// from/to).
func TestPostEODRange_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp := newEODAPITestMux(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-eod-range", "blocked-cashier-eod-range", "334455")

	form := strings.NewReader("from=2026-01-01&to=2026-01-31&override_pin=334455")
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/reports/eod/range", form), auth.User{ID: blockedID, Role: "cashier"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Fatalf("expected the real download to go through once elevated, got Content-Disposition %q body %s", cd, rec.Body.String())
	}

	var actorID, blockedActorID string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='eod_range_exported'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected an eod_range_exported audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
	}
}

// ut-docs#794: POST /api/settings/eod moved off the flat 403 onto
// checkOrElevate, same as the other 5 eod_report sites in this file.
func TestPostSettingsEOD_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader("enabled=on&time=22:30"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("expected the elevation prompt dialog, got: %s", rec.Body.String())
	}
}

// ut-docs#794 review finding (should-fix): exercises the Hidden-field
// replay specifically — a real dialog retry re-submits override_pin
// ALONGSIDE the original enabled/time/business_day_start fields (the
// server-rendered hidden inputs), not override_pin alone.
func TestPostSettingsEOD_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp := newEODAPITestMux(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-eod-settings", "blocked-cashier-eod-settings", "778899")

	form := strings.NewReader("enabled=on&time=21:45&business_day_start=06:00&override_pin=778899")
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/eod", form), auth.User{ID: blockedID, Role: "cashier"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevated confirmation), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected a success indicator, got: %s", rec.Body.String())
	}

	val, _, err := dp.Settings.Get(t.Context(), keyEODTime)
	if err != nil || val != "21:45" {
		t.Fatalf("expected the replayed time persisted, got %q err=%v", val, err)
	}

	var actorID, blockedActorID string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='eod_settings_changed'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected an eod_settings_changed audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
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
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("expected the elevation prompt dialog, got: %s", rec.Body.String())
	}
}

// ut-docs#794 review finding (should-fix): elevated retry now gets a real
// 200 confirmation body (not the bare 204 the plain-session path above
// keeps), since a 204 never swaps under htmx at all — see the handler's own
// comment for why.
func TestPostSettingsReportRetention_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp := newEODAPITestMux(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-eod-retention", "blocked-cashier-eod-retention", "998877")

	form := strings.NewReader("mode=till&override_pin=998877")
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/report-retention", form), auth.User{ID: blockedID, Role: "cashier"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevated confirmation), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected a success indicator, got: %s", rec.Body.String())
	}

	val, _, err := dp.Settings.Get(t.Context(), common.KeyReportRetentionMode)
	if err != nil || val != "till" {
		t.Fatalf("expected report_retention_mode=till persisted, got %q err=%v", val, err)
	}

	var actorID, blockedActorID string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='report_retention_mode_changed'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected a report_retention_mode_changed audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
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

// ut-docs#794: same raw-fetch shape as TestPostEODRange_RequiresManager.
func TestPostReportArchiveExport_RequiresManager(t *testing.T) {
	mux, _ := newEODAPITestMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", strings.NewReader("from=2026-01-01&to=2026-01-31&format=json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html so the raw-fetch caller can distinguish this from the real file download, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("expected the elevation prompt dialog, got: %s", rec.Body.String())
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

// ut-docs#794 review finding (should-fix): the second raw-fetch (non-htmx)
// site's elevated-path proof, mirroring TestPostEODRange_ElevatesOnValidApproverPIN.
func TestPostReportArchiveExport_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp := newEODAPITestMux(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-archive-export", "blocked-cashier-archive-export", "112233")

	form := strings.NewReader("from=2026-01-01&to=2026-01-31&format=json&override_pin=112233")
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/reports/archive/export", form), auth.User{ID: blockedID, Role: "cashier"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Fatalf("expected the real download to go through once elevated, got Content-Disposition %q body %s", cd, rec.Body.String())
	}

	var actorID, blockedActorID string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='report_archive_exported'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected a report_archive_exported audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
	}
}

func TestPostReportArchiveExport_JSONAndCSVDownloads(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)

	if _, _, err := generateEOD(t.Context(), dp, "2026-01-15", "system", ""); err != nil {
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
