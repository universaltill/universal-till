package pages

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// efdOpenDeps mirrors newFiscalDeviceTestMux's Deps construction (same
// file's own doc comment on why Cfg/State/Settings are all needed once a
// gate reads d.CurrentState().Country), without the HTTP mux wiring this
// test doesn't need.
func efdOpenDeps(t *testing.T) (*sql.DB, *common.Deps) {
	t.Helper()
	sqldb := openPagesTestDB(t)
	t.Cleanup(func() { sqldb.Close() })
	seedForPages(t, sqldb)
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "TRY", TaxRate: 20}}
	d := &common.Deps{
		Cfg:      cfg,
		Db:       sqldb,
		Settings: settings.NewStore(sqldb),
		State:    common.LoadState(context.Background(), settings.NewStore(sqldb), cfg),
	}
	return sqldb, d
}

func efdReceipt(t *testing.T, sqldb *sql.DB, saleID, maker, serial, kind string, zNo int64, createdAt string) {
	t.Helper()
	if _, err := sqldb.Exec(`INSERT INTO fiscal_device_receipts
(sale_id, device_kind, maker, serial, receipt_no, receipt_kind, z_no, issued_at, created_at)
VALUES (?, 'okc', ?, ?, ?, ?, ?, ?, ?)`, saleID, maker, serial, "RCPT-"+saleID, kind, zNo, createdAt, createdAt); err != nil {
		t.Fatalf("seed fiscal_device_receipts: %v", err)
	}
}

// Not TR (or TR without the plugin active): attachEODFiscalDevice leaves
// rep.FiscalDevice nil — this is the guard that keeps every non-Turkish
// shop's archived content_json byte-for-byte unchanged.
func TestAttachEODFiscalDevice_NotTR_LeavesNil(t *testing.T) {
	sqldb, d := efdOpenDeps(t)
	setCountry(t, d, "DE")
	repo := data.NewPOSRepo(sqldb)
	rep := data.EODReport{}
	attachEODFiscalDevice(context.Background(), d, repo, &rep, time.Time{}, time.Now())
	if rep.FiscalDevice != nil {
		t.Fatalf("FiscalDevice = %+v, want nil for a non-TR shop", rep.FiscalDevice)
	}
}

// TR shop with the plugin active and device evidence in window: rep.
// FiscalDevice is filled from the repo's real read.
func TestAttachEODFiscalDevice_TRActive_Fills(t *testing.T) {
	sqldb, d := efdOpenDeps(t)
	setCountry(t, d, "TR")
	seedActiveTaxTrPlugin(t, sqldb, true)
	repo := data.NewPOSRepo(sqldb)

	now := time.Now()
	from := now.Add(-1 * time.Hour)
	to := now.Add(1 * time.Hour)
	efdReceipt(t, sqldb, "efd-sale-1", "beko", "AV123", "mali_fis", 3, now.UTC().Format(time.RFC3339))

	rep := data.EODReport{}
	attachEODFiscalDevice(context.Background(), d, repo, &rep, from, to)
	if rep.FiscalDevice == nil {
		t.Fatal("FiscalDevice is nil, want it filled for an active TR shop")
	}
	if rep.FiscalDevice.MaliFis != 1 || rep.FiscalDevice.Serial != "AV123" {
		t.Fatalf("FiscalDevice = %+v, want MaliFis=1 Serial=AV123", rep.FiscalDevice)
	}
}

// ut-docs#2410 (orchestrator finding before review): the range export's
// dates arrive at UTC midnight from time.Parse; the device window must be
// re-anchored to LOCAL midnight (the shop's day, as EndOfDayRange buckets
// it), and the upper bound must be exclusive of the day AFTER `to`.
func TestLocalDayWindow_LocalMidnightAndExclusiveEnd(t *testing.T) {
	from, _ := time.Parse("2006-01-02", "2026-09-17")
	to, _ := time.Parse("2006-01-02", "2026-09-18")
	gotFrom, gotTo := localDayWindow(from, to)
	wantFrom := time.Date(2026, 9, 17, 0, 0, 0, 0, time.Local)
	wantTo := time.Date(2026, 9, 19, 0, 0, 0, 0, time.Local)
	if !gotFrom.Equal(wantFrom) || gotFrom.Location() != time.Local {
		t.Fatalf("from = %v (%v), want %v local", gotFrom, gotFrom.Location(), wantFrom)
	}
	if !gotTo.Equal(wantTo) {
		t.Fatalf("to = %v, want %v (exclusive, day after `to`)", gotTo, wantTo)
	}
	if gotTo.Sub(gotFrom) != 48*time.Hour {
		t.Fatalf("window = %v, want 48h for a two-day range", gotTo.Sub(gotFrom))
	}
}
