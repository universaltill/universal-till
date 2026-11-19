package pages

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5841 AC9: printing a shift slip changes nothing the Z, the shift
// or the exports read. The only write is one audit_log row per print.

func shiftSlipIsolationFixture(t *testing.T) *xpFx {
	t.Helper()
	f := shiftSlipClosedFixture(t)
	registerEODAPI(f.mux, f.dp)
	registerReportArchiveAPI(f.mux, f.dp)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	return f
}

func TestShiftCloseSlipPrint_ZAndShiftUnchanged(t *testing.T) {
	f := shiftSlipIsolationFixture(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(f.dp.Db)
	to := time.Now().Add(10 * time.Minute) // slip audit rows fall INSIDE the window

	shiftRow := func() [6]string {
		var r [6]string
		if err := f.dp.Db.QueryRow(`SELECT COALESCE(closed_at,''), CAST(closing_cash AS TEXT), CAST(expected_cash AS TEXT), COALESCE(CAST(new_float AS TEXT),''), COALESCE(note,''), COALESCE(count_protocol,'') FROM shifts WHERE id='sh1'`).
			Scan(&r[0], &r[1], &r[2], &r[3], &r[4], &r[5]); err != nil {
			t.Fatal(err)
		}
		return r
	}
	snap := func() (string, []data.EODTaxBandSale, int, *time.Time) {
		rep, err := repo.EndOfDayInstant(ctx, time.Time{}, to)
		if err != nil {
			t.Fatal(err)
		}
		rep.GeneratedAt = ""
		b, _ := json.Marshal(rep)
		bands, err := repo.SalesForTaxBandsInstant(ctx, time.Time{}, to)
		if err != nil {
			t.Fatal(err)
		}
		latest, err := repo.LatestArchivedAt(ctx, "eod")
		if err != nil {
			t.Fatal(err)
		}
		return string(b), bands, f.count(t, "report_archive"), latest
	}
	j0, b0, n0, l0 := snap()
	s0 := shiftRow()
	for i := 0; i < 2; i++ {
		if rec := f.post(t, slipPrintPath, url.Values{}, nil, nil); rec.Code != 200 {
			t.Fatalf("print %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 2 {
		t.Fatalf("audit rows = %d, want 2 (re-print = another row)", n)
	}
	j1, b1, n1, l1 := snap()
	if n1 != n0 || !reflect.DeepEqual(l0, l1) {
		t.Errorf("archive %d -> %d, latest %v -> %v", n0, n1, l0, l1)
	}
	if j0 != j1 {
		t.Errorf("EndOfDayInstant changed:\n%s\n%s", j0, j1)
	}
	if !reflect.DeepEqual(b0, b1) {
		t.Errorf("tax bands changed")
	}
	if s1 := shiftRow(); s0 != s1 {
		t.Errorf("shift row changed: %v -> %v", s0, s1)
	}
	if _, created, err := generateEOD(ctx, f.dp, "system", "", ""); err != nil || !created {
		t.Fatalf("generateEOD created=%v err=%v", created, err)
	}
	if n := f.count(t, "report_archive"); n != n0+1 {
		t.Errorf("Z archived %d rows, want exactly 1", n-n0)
	}
}

func TestShiftCloseSlipPrint_ExportsUnchanged(t *testing.T) {
	f := shiftSlipIsolationFixture(t)
	ctx := t.Context()
	if _, _, err := generateEOD(ctx, f.dp, "system", "", ""); err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02")
	post := func(path, format string) any {
		form := url.Values{"from": {today}, "to": {today}}
		if format != "" {
			form.Set("format", format)
		}
		rec := f.post(t, path, form, nil, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var v any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s: not JSON: %v", path, err)
		}
		return stripKeys(v, "generated_at")
	}
	repo := data.NewPOSRepo(f.dp.Db)
	type gather struct {
		Sales  []data.ExportSaleRow
		Count  int
		Closes []data.EODCloseExport
		Stock  []data.ExportStockRow
	}
	capture := func(ctx context.Context) (any, any, gather) {
		var g gather
		var err error
		if g.Sales, err = repo.SalesForExport(ctx, today, today); err != nil {
			t.Fatal(err)
		}
		if g.Count, err = repo.CountSalesForExport(ctx, today, today); err != nil {
			t.Fatal(err)
		}
		if g.Closes, err = repo.EODClosesForExport(ctx, today, today); err != nil {
			t.Fatal(err)
		}
		if g.Stock, err = repo.StockForExport(ctx); err != nil {
			t.Fatal(err)
		}
		return post("/api/reports/eod/range", ""), post("/api/reports/archive/export", "json"), g
	}
	r0, a0, g0 := capture(ctx)
	for i := 0; i < 2; i++ {
		if rec := f.post(t, slipPrintPath, url.Values{}, nil, nil); rec.Code != 200 {
			t.Fatalf("print: %d %s", rec.Code, rec.Body.String())
		}
	}
	r1, a1, g1 := capture(ctx)
	if !reflect.DeepEqual(r0, r1) || !reflect.DeepEqual(a0, a1) || !reflect.DeepEqual(g0, g1) {
		t.Errorf("an export changed after printing a shift slip")
	}
}
