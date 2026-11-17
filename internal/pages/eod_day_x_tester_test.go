package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#5800, added by the Tester: AC1 on a SECOND close (non-zero from),
// with a cleared basket and a no-sale in the window, every sale banded, and
// sum(band.Tax) == TaxNet on the X itself. Instants derive from time.Now().
func TestDayX_MatchesNextZ_AfterAClose_FullDay(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Replica', 'h2')`); err != nil {
		t.Fatal(err)
	}
	// close N yesterday (a same-local-day second close is refused by
	// ArchiveReport), so the next window's from is non-zero.
	closeN := time.Now().Add(-26 * time.Hour)
	if ok, err := repo.ArchiveReport(ctx, "eod", closeN.Local().Format(time.RFC3339), []byte(`{}`), "", "", closeN); err != nil || !ok {
		t.Fatalf("seed close N: %v %v", ok, err)
	}
	at := time.Now().Add(-30 * time.Minute)
	stamp := at.UTC().Format(time.RFC3339)
	for i, id := range []string{"dx-a", "dx-b"} {
		if applied, _, err := applyJournal(ctx, dp, "till-2", lateJournalSale(id, "T2-DX"+id, at.Add(time.Duration(i)*time.Minute))); err != nil || !applied {
			t.Fatalf("apply %s: applied=%v err=%v", id, applied, err)
		}
	}
	if _, err := repo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{CreatedAt: stamp, ActorID: "user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertClearedBasket(ctx, data.ClearedBasket{CreatedAt: stamp, LineCount: 2, Total: money.FromMinor(450)}); err != nil {
		t.Fatal(err)
	}
	// Journaled sales count at their apply time (completed_at = now, whole
	// seconds) and the window is [from, to): step past this second.
	time.Sleep(1100 * time.Millisecond)

	x, err := readEODDayX(ctx, dp, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if x.From == "" || x.SalesCount != 2 || x.NoSaleCount != 1 || x.ClearedBasketCount != 1 {
		t.Fatalf("X from=%q sales=%d nosale=%d cleared=%d, want non-empty/2/1/1", x.From, x.SalesCount, x.NoSaleCount, x.ClearedBasketCount)
	}
	var tax int64
	for _, b := range x.TaxBands {
		tax += b.Tax
	}
	if len(x.TaxBands) != 2 || tax != x.TaxNet || x.TaxNet == 0 {
		t.Fatalf("X: sum(band.Tax)=%d TaxNet=%d bands=%+v", tax, x.TaxNet, x.TaxBands)
	}
	if len(x.MethodTaxBands) == 0 {
		t.Fatal("X lacks the method x rate cross-tab")
	}
	z, created, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil || !created {
		t.Fatalf("close N+1: created=%v err=%v", created, err)
	}
	if a, b := dxNorm(t, x), dxNorm(t, z); a != b {
		t.Errorf("X differs from close N+1:\nX=%s\nZ=%s", a, b)
	}
}

// The routes are mounted by the production chain (registerEODAPI), not only by
// the tests' direct registerEODDayX call.
func TestDayX_RegisteredByEODAPI(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)
	for _, p := range []string{dxView, dxPrint} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s not mounted by registerEODAPI: %d", p, rec.Code)
		}
	}
}

// AC7: the gate is eod_report, not reports. A role granted `reports` but not
// `eod_report` gets the prompt on both routes; granting eod_report lets it in.
func TestDayX_ReportsWithoutEODReportIsPrompted(t *testing.T) {
	f := dxFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-r", "cash-r", "445566")
	authRepo := data.NewAuthRepo(f.dp.Db)
	if err := authRepo.SetRolePermission(t.Context(), nil, "cashier", "reports", true); err != nil {
		t.Fatal(err)
	}
	if err := authRepo.SetRolePermission(t.Context(), nil, "cashier", "eod_report", false); err != nil {
		t.Fatal(err)
	}
	u := &auth.User{ID: cashierID, Role: "cashier"}
	for _, p := range []string{dxView, dxPrint} {
		if rec := f.post(t, p, url.Values{}, nil, u); rec.Header().Get("X-UT-Response") != "elevation-prompt" {
			t.Errorf("%s: reports-only role must be prompted, got %d %q", p, rec.Code, rec.Body.String())
		}
	}
	if err := authRepo.SetRolePermission(t.Context(), nil, "cashier", "eod_report", true); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, dxView, url.Values{}, nil, u)
	if rec.Header().Get("X-UT-Response") != "" || !strings.Contains(rec.Body.String(), "X REPORT - DAY SO FAR") {
		t.Errorf("eod_report granted: %d %q", rec.Code, rec.Body.String())
	}
}
