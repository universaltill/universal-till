package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	utdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// Tester additions for ut-docs#5769. These run on a file-backed, WAL,
// multi-connection pool exactly as db.Open hands it to production — the
// package's openPagesTestDB caps the pool at one connection, where
// HoldWriteLock degrades to a no-op, so none of Dev's pages tests exercise
// the lock.
func newFileBackedEODDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	t.Cleanup(pagesGlobalsBaseline)
	path := filepath.Join(t.TempDir(), "eod_5769.db")
	if err := os.WriteFile(path, realDBTemplate(t), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := utdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB := d.DB
	t.Cleanup(func() { sqlDB.Close() })
	if n := sqlDB.Stats().MaxOpenConnections; n == 1 {
		t.Fatalf("want an uncapped pool, got MaxOpenConnections=%d", n)
	}
	seedForPages(t, sqlDB)
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000},
		Marketplace: config.MarketplaceConfig{EndpointURL: "http://localhost:8081"}}
	pm, err := plugins.Init(t.Context(), cfg, sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	dp := &common.Deps{
		Cfg: cfg, Db: sqlDB, Pm: pm,
		State:    common.LoadState(t.Context(), settings.NewStore(sqlDB), cfg),
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(sqlDB),
		AuthSvc:  auth.NewService(sqlDB),
	}
	t.Cleanup(dp.WaitForAsyncWork)
	mux := http.NewServeMux()
	registerEODAPI(mux, dp)
	registerReportArchiveAPI(mux, dp)
	return mux, dp
}

func postEODRun(t *testing.T, mux *http.ServeMux) (string, time.Duration) {
	t.Helper()
	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/reports/eod/run", nil))
	took := time.Since(start)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("eod run: %d %s (took %v)", rec.Code, rec.Body.String(), took)
	}
	return rec.Body.String(), took
}

// Full close via POST /api/reports/eod/run on the production-shaped pool:
// no deadlock/busy wait inside the held section, Z N untouched, and the late
// row + late_entries note land in Z N+1's ARCHIVED content_json (Dev's test
// inspects the returned report only; its second same-day close is never
// archived because of ArchiveReport's one-close-per-local-day guard).
func TestEODRun_FileBackedPool_LateEntriesArchived(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newFileBackedEODDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Replica', 'h2')`); err != nil {
		t.Fatal(err)
	}
	if _, took := postEODRun(t, mux); took > 3*time.Second {
		t.Fatalf("close took %v on a multi-connection pool: blocked inside the held write lock?", took)
	}
	// Move Z N to yesterday so a second close is allowed today (one close
	// per local day). Nothing else exists between, so no row is affected.
	if _, err := dp.Db.Exec(`UPDATE report_archive SET created_at = datetime(created_at, '-1 day') WHERE kind='eod'`); err != nil {
		t.Fatal(err)
	}
	var closeN string
	if err := dp.Db.QueryRow(`SELECT created_at FROM report_archive WHERE kind='eod'`).Scan(&closeN); err != nil {
		t.Fatal(err)
	}
	closeAt, err := time.ParseInLocation("2006-01-02 15:04:05", closeN, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	zN := func() string {
		var s string
		if err := dp.Db.QueryRow(`SELECT z_number||'|'||period||'|'||created_at||'|'||content_json FROM report_archive WHERE z_number=1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := zN()

	origin := closeAt.Add(-10 * time.Minute)
	if applied, _, err := applyJournal(ctx, dp, "till-2", lateJournalSale("late-h1", "T2-H1", origin)); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, data.NoSaleEvent{ID: "late-h-ns", CreatedAt: origin.UTC().Format(time.RFC3339), TillID: "till-2"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, took := postEODRun(t, mux); took > 3*time.Second {
		t.Fatalf("second close took %v", took)
	}
	if got := zN(); got != before {
		t.Fatalf("Z N changed:\n%s\n%s", before, got)
	}
	var raw string
	if err := dp.Db.QueryRow(`SELECT content_json FROM report_archive WHERE z_number=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var z2 data.EODReport
	if err := json.Unmarshal([]byte(raw), &z2); err != nil {
		t.Fatal(err)
	}
	if z2.SalesCount != 1 || z2.Gross != 300 || z2.NoSaleCount != 1 {
		t.Fatalf("archived Z N+1 sales=%d gross=%d nosale=%d, want 1/300/1: %s", z2.SalesCount, z2.Gross, z2.NoSaleCount, raw)
	}
	if z2.LateEntries == nil || len(z2.LateEntries.Tills) != 1 || z2.LateEntries.Tills[0].Gross != 300 || z2.LateEntries.Tills[0].NoSaleCount != 1 {
		t.Fatalf("archived late_entries = %+v: %s", z2.LateEntries, raw)
	}
	if want := origin.Truncate(time.Second).Local().Format(time.RFC3339); z2.LateEntries.EarliestOrigin != want {
		t.Fatalf("earliest origin %q, want %q", z2.LateEntries.EarliestOrigin, want)
	}
}

// The hazard HoldWriteLock exists for: a writer stamps its sale time inside
// its BEGIN IMMEDIATE tx (CompleteSale's `now`), then commits after the close
// took `to`. Without the lock the close reads before the commit and misses
// the row, and the next Z (from = to) misses it too: in no Z. With the lock
// the close waits for the commit, then takes `to`. Dev's data-level atomic
// test passes with the lock disabled; this one does not.
func TestGenerateEOD_WaitsForInFlightSaleTx(t *testing.T) {
	_, dp := newFileBackedEODDeps(t)
	ctx := context.Background()
	tx, err := dp.Db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (_txlock=immediate)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-3 * time.Second).UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at, till_id, local_date)
VALUES ('inflight', 'R-IF', 'completed', 'sale', 'GBP', 500, 0, 0, 500, ?, ?, '', date(?, 'localtime'))`, stamp, stamp, stamp); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	type res struct {
		rep data.EODReport
		err error
	}
	out := make(chan res, 1)
	go func() {
		rep, _, err := generateEOD(ctx, dp, "system", "", "")
		out <- res{rep, err}
	}()
	time.Sleep(400 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r := <-out
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.rep.SalesCount != 1 || r.rep.Gross != 500 {
		t.Fatalf("close missed the sale committed during it: sales=%d gross=%d (stamp %s, to %s)", r.rep.SalesCount, r.rep.Gross, stamp, r.rep.To)
	}
}
