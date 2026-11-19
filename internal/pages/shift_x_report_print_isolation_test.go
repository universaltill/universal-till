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

// ut-docs#5798 AC4 / AC12: printing an X changes nothing the Z, the shift or
// the exports read. The only write is one audit_log row.

func xpIsolationFixture(t *testing.T) *xpFx {
	t.Helper()
	f := xpFixture(t)
	registerEODAPI(f.mux, f.dp)
	registerReportArchiveAPI(f.mux, f.dp)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	return f
}

func TestXReportPrint_ZAndShiftUnchanged(t *testing.T) {
	f := xpIsolationFixture(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(f.dp.Db)
	// In the future, so the X audit rows written below fall INSIDE the
	// window: a reader that counted them would change the result.
	to := time.Now().Add(10 * time.Minute)

	snap := func() (string, []data.EODTaxBandSale, int, *time.Time, int64) {
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
		exp, err := repo.ComputeExpectedCash(ctx, "sh1", 10000)
		if err != nil {
			t.Fatal(err)
		}
		return string(b), bands, f.count(t, "report_archive"), latest, exp
	}
	j0, b0, n0, l0, e0 := snap()

	if rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil); rec.Code != 200 {
		t.Fatalf("view: %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		if rec := f.post(t, xpPrintPath, url.Values{}, nil, nil); rec.Code != 200 {
			t.Fatalf("print %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if n := len(xpAuditRows(t, f)); n != 2 {
		t.Fatalf("audit rows = %d, want 2 (so the window check is meaningful)", n)
	}

	j1, b1, n1, l1, e1 := snap()
	if n1 != n0 {
		t.Errorf("report_archive %d -> %d", n0, n1)
	}
	if !reflect.DeepEqual(l0, l1) {
		t.Errorf("LatestArchivedAt %v -> %v", l0, l1)
	}
	if j0 != j1 {
		t.Errorf("EndOfDayInstant changed:\n%s\n%s", j0, j1)
	}
	if !reflect.DeepEqual(b0, b1) {
		t.Errorf("tax bands changed")
	}
	if e0 != e1 {
		t.Errorf("expected cash %d -> %d", e0, e1)
	}
	var closedAt, closing, expected *string
	if err := f.dp.Db.QueryRow(`SELECT closed_at, CAST(closing_cash AS TEXT), CAST(expected_cash AS TEXT) FROM shifts WHERE id='sh1'`).Scan(&closedAt, &closing, &expected); err != nil {
		t.Fatal(err)
	}
	if closedAt != nil || closing != nil || expected != nil {
		t.Errorf("shift changed: %v %v %v", closedAt, closing, expected)
	}

	_, created, err := generateEOD(ctx, f.dp, "system", "", "")
	if err != nil || !created {
		t.Fatalf("generateEOD created=%v err=%v", created, err)
	}
	if n := f.count(t, "report_archive"); n != n0+1 {
		t.Errorf("Z archived %d rows, want exactly 1", n-n0)
	}
}

// stripKeys removes every key named k at any depth.
func stripKeys(v any, k string) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, k)
		for kk, vv := range x {
			x[kk] = stripKeys(vv, k)
		}
	case []any:
		for i := range x {
			x[i] = stripKeys(x[i], k)
		}
	}
	return v
}

func TestXReportPrint_ExportsUnchanged(t *testing.T) {
	f := xpIsolationFixture(t)
	ctx := t.Context()
	if _, _, err := generateEOD(ctx, f.dp, "system", "", ""); err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02")
	post := func(path, extra string) any {
		form := url.Values{"from": {today}, "to": {today}}
		if extra != "" {
			form.Set("format", extra)
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
	// /api/data/export needs an installed export plugin, so pin the exact
	// repo calls its handler makes (data_api.go) instead.
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
		if rec := f.post(t, xpPrintPath, url.Values{}, nil, nil); rec.Code != 200 {
			t.Fatalf("print: %d %s", rec.Code, rec.Body.String())
		}
	}
	r1, a1, g1 := capture(ctx)
	if !reflect.DeepEqual(r0, r1) {
		t.Errorf("eod/range changed")
	}
	if !reflect.DeepEqual(a0, a1) {
		t.Errorf("archive/export changed")
	}
	if !reflect.DeepEqual(g0, g1) {
		t.Errorf("data export gather changed")
	}
}
