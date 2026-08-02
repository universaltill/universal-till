package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
