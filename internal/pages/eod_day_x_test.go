package pages

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5800: the till-wide "day so far" X report on Reports -> End of day.
// Every instant derives from time.Now() (the fixed-date guard).

const (
	dxView  = "/reports/eod/x-report"
	dxPrint = "/api/reports/eod/x-report/print"
)

// dxFixture is the xpFixture day (cash sale, return) plus a card sale with two
// VAT rates and a voided sale, with the day-X routes mounted.
func dxFixture(t *testing.T) *xpFx {
	t.Helper()
	f := xpFixture(t)
	registerEODDayX(f.mux, f.dp)
	f.sale(t, "s2", "R-0003", "sale", "reg1", 1200, 129, 3*time.Minute)
	f.exec(t, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('i1','DX1','Tea',600,1),('i2','DX2','Bun',600,1)`)
	f.exec(t, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES ('s2-l1','s2',1,'i1','Tea',1,600,0,2000,100,500,600),('s2-l2','s2',2,'i2','Bun',1,600,0,500,29,571,600)`)
	f.pay(t, "p2", "s2", "card", 1200)
	f.sale(t, "v1", "R-0004", "sale", "reg1", 700, 0, 4*time.Minute)
	f.exec(t, `UPDATE sales SET status='voided' WHERE id='v1'`)
	return f
}

// dxNorm drops the per-call stamps: what the Z archives must equal what the
// preview showed, minus who/when it was produced.
func dxNorm(t *testing.T, rep data.EODReport) string {
	t.Helper()
	rep.GeneratedAt, rep.GeneratedBy, rep.Annotation, rep.To = "", "", "", ""
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dxSnapshot(t *testing.T, f *xpFx) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tbl := range []string{"report_archive", "audit_log"} {
		out[tbl] = string(rune('0' + f.count(t, tbl)))
	}
	rows, err := f.dp.Db.Query(`SELECT key||'='||value FROM settings ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		sb.WriteString(s + "\n")
	}
	out["settings"] = sb.String()
	return out
}

func TestDayX_MatchesNextZ(t *testing.T) {
	f := dxFixture(t)
	x, err := readEODDayX(t.Context(), f.dp, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if x.SalesCount == 0 || len(x.TaxBands) == 0 || len(x.MethodTaxBands) == 0 {
		t.Fatalf("fixture must produce figures and bands: %+v", x)
	}
	if x.FiscalDevice != nil || x.Annotation != "" {
		t.Errorf("X must carry no fiscal device or annotation: %+v", x)
	}
	z, created, err := generateEOD(t.Context(), f.dp, "u1", "", "")
	if err != nil || !created {
		t.Fatalf("generateEOD: %v %v", created, err)
	}
	if a, b := dxNorm(t, x), dxNorm(t, z); a != b {
		t.Errorf("X differs from the Z archived a moment later:\nX=%s\nZ=%s", a, b)
	}
}

func TestDayX_ReadOnly_NoArchiveNoSettingsNextZIdentical(t *testing.T) {
	f := dxFixture(t)
	before := dxSnapshot(t, f)
	for i := 0; i < 3; i++ {
		if rec := f.post(t, dxView, url.Values{}, nil, nil); rec.Code != 200 {
			t.Fatalf("view %d: %d %s", i, rec.Code, rec.Body.String())
		}
		if rec := f.post(t, dxView, url.Values{}, jsonHdr, nil); rec.Code != 200 {
			t.Fatalf("json view %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	after := dxSnapshot(t, f)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed by viewing: %q -> %q", k, v, after[k])
		}
	}
	if latest, err := data.NewPOSRepo(f.dp.Db).LatestArchivedAt(t.Context(), "eod"); err != nil || latest != nil {
		t.Errorf("LatestArchivedAt = %v %v, want nil", latest, err)
	}
	var n int
	if err := f.dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='eod_generated'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("eod_generated rows = %d %v", n, err)
	}
	// the next Z has the same (unbounded) window as without any X
	z, created, err := generateEOD(t.Context(), f.dp, "u1", "", "")
	if err != nil || !created || z.From != "" || z.SalesCount != 2 {
		t.Errorf("next Z: from=%q sales=%d created=%v err=%v", z.From, z.SalesCount, created, err)
	}
}

func TestDayX_FirstEverClose_SinceFirst(t *testing.T) {
	f := dxFixture(t)
	rec := f.post(t, dxView, url.Values{}, nil, nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "this till has no day close yet") || !strings.Contains(body, "Zeitraum bis ") {
		t.Fatalf("first-ever close: %d %s", rec.Code, body)
	}
	if _, _, err := generateEOD(t.Context(), f.dp, "u1", "", ""); err != nil {
		t.Fatal(err)
	}
	body = f.post(t, dxView, url.Values{}, nil, nil).Body.String()
	if strings.Contains(body, "no day close yet") || !strings.Contains(body, "Since the last day close at") || !strings.Contains(body, "Zeitraum 20") {
		t.Errorf("after a close the since line names it: %s", body)
	}
}

func TestDayX_EmptyWindow(t *testing.T) {
	f := dxFixture(t)
	if _, _, err := generateEOD(t.Context(), f.dp, "u1", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, dxView, url.Values{}, nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Nothing sold since the last day close.") {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), dxPrint) {
		t.Error("Print must still be offered on an empty window")
	}
}

func TestDayX_NoFiscalDevice_EvenWithOKCMarket(t *testing.T) {
	f := dxFixture(t)
	setCountry(t, f.dp, "TR")
	seedActiveTaxTrPlugin(t, f.dp.Db, true)
	efdReceipt(t, f.dp.Db, "s1", "beko", "AV123", "mali_fis", 3, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	x, err := readEODDayX(t.Context(), f.dp, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if x.FiscalDevice != nil {
		t.Fatalf("X must never attach a fiscal device: %+v", x.FiscalDevice)
	}
	// control: the Z itself does attach it in this market
	z, _, err := generateEOD(t.Context(), f.dp, "u1", "", "")
	if err != nil || z.FiscalDevice == nil {
		t.Fatalf("control: Z FiscalDevice = %+v err=%v", z.FiscalDevice, err)
	}
}

// A real WAL, multi-connection pool (the package default is capped at one
// connection, where HoldWriteLock is a no-op; db.Open sets _txlock=immediate
// and busy_timeout 5000). No wall-clock threshold (ut-docs#6026): a loaded CI
// runner can be slow without any lock involved.
//
//  1. Another writer holds the write lock until the view has returned. A view
//     that took the write lock would wait out busy_timeout and fail with
//     SQLITE_BUSY; the 30 s timer only stops a hang.
//  2. The converse: an insert on a connection with busy_timeout 0 fails at once
//     with SQLITE_BUSY if a view holds the write lock, instead of waiting. It
//     runs while views run back to back, so it overlaps one.
func TestDayX_NoWriteLock(t *testing.T) {
	_, dp := newFileBackedEODDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	release, err := repo.HoldWriteLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	viewErr := make(chan error, 1)
	go func() {
		_, err := readEODDayX(ctx, dp, "Alex")
		viewErr <- err
	}()
	select {
	case err := <-viewErr:
		if err != nil {
			release()
			t.Fatalf("the view failed while another writer held the lock (it must not take the write lock): %v", err)
		}
	case <-time.After(30 * time.Second):
		release()
		t.Fatal("the view did not return while another writer held the lock: it must not take the write lock")
	}
	release()

	conn, err := dp.Db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	// the connection goes back to the pool: restore db.Open's value
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA busy_timeout = 5000`) }()

	stop := make(chan struct{})
	started := make(chan struct{})
	views := make(chan error, 1)
	go func() {
		for n := 0; ; n++ {
			if n == 1 {
				close(started) // the insert overlaps the second view onwards
			}
			if _, err := readEODDayX(ctx, dp, "Alex"); err != nil {
				views <- err
				return
			}
			select {
			case <-stop:
				views <- nil
				return
			default:
			}
		}
	}()
	select {
	case <-started:
	case err := <-views: // the first view failed before the insert could start
		t.Fatalf("a view failed: %v", err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	_, insErr := conn.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at, till_id, local_date)
VALUES ('dx-live', 'R-DX', 'completed', 'sale', 'GBP', 500, 0, 0, 500, ?, ?, '', date(?, 'localtime'))`, stamp, stamp, stamp)
	close(stop)
	if err := <-views; err != nil {
		t.Fatalf("a view failed: %v", err)
	}
	if insErr != nil {
		t.Errorf("a sale insert made while views ran could not take the write lock at once: %v", insErr)
	}
}

func TestDayX_Gate(t *testing.T) {
	f := dxFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	u := &auth.User{ID: cashierID, Role: "cashier"}

	rec := f.post(t, dxView, url.Values{}, nil, u)
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("want elevation prompt, got %d %q", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	if b := rec.Body.String(); !strings.Contains(b, dxView) || strings.Contains(b, "R-0001") || strings.Contains(b, "987.65") {
		t.Errorf("prompt must replay the route and carry no figures: %s", b)
	}
	if rec := f.post(t, dxView, url.Values{"override_pin": {"000000"}}, nil, u); rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Errorf("wrong PIN must re-prompt: %d", rec.Code)
	}
	if rec := f.post(t, dxView+"?override_pin=445566", url.Values{}, nil, u); rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Errorf("a PIN in the query string must be ignored: %d", rec.Code)
	}
	rec = f.post(t, dxView, url.Values{"override_pin": {"445566"}}, nil, u)
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" || !strings.Contains(rec.Body.String(), "Zeitraum") {
		t.Errorf("manager PIN: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.post(t, dxView, url.Values{}, jsonHdr, u)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"manager_pin_required"`) || strings.Contains(rec.Body.String(), "report") {
		t.Errorf("JSON gate: %d %s", rec.Code, rec.Body.String())
	}
	// auth off: no PIN asked
	t.Setenv("UT_AUTH", "off")
	if rec := f.post(t, dxView, url.Values{}, nil, nil); rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" {
		t.Errorf("auth off: %d", rec.Code)
	}
}

func TestDayX_JSONShape(t *testing.T) {
	f := dxFixture(t)
	rec := f.post(t, dxView, url.Values{}, jsonHdr, nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Kind   string         `json:"kind"`
			From   string         `json:"from"`
			To     string         `json:"to"`
			Report map[string]any `json:"report"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil || env.Data.Kind != "day_x" || env.Data.To == "" || env.Data.Report["sales_count"] == nil || env.Data.Report["tax_bands"] == nil {
		t.Errorf("shape: %s", rec.Body.String())
	}
}

func TestDayX_BadForm400(t *testing.T) {
	f := dxFixture(t)
	big := url.Values{"x": {strings.Repeat("a", 5000)}}
	for _, route := range []string{dxView, dxPrint} {
		rec := f.post(t, route, big, jsonHdr, nil)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_request"`) {
			t.Errorf("%s oversize: %d %s", route, rec.Code, rec.Body.String())
		}
		if rec := f.post(t, route, big, nil, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "could not be read") {
			t.Errorf("%s oversize html: %d %s", route, rec.Code, rec.Body.String())
		}
	}
}

func TestDayX_ExportsUnaffected(t *testing.T) {
	f := dxFixture(t)
	before := dxSnapshot(t, f)
	if rec := f.post(t, dxView, url.Values{}, nil, nil); rec.Code != 200 {
		t.Fatalf("view: %d %s", rec.Code, rec.Body.String())
	}
	if got := dxSnapshot(t, f); got["report_archive"] != before["report_archive"] {
		t.Error("the X must not reach the archive that every export reads")
	}
}

// A window holding only voids is not "nothing sold": the paper lists them, so
// the empty note must not show above it (review F6).
func TestDayX_VoidOnlyWindowIsNotEmptyNote(t *testing.T) {
	f := dxFixture(t)
	f.exec(t, `UPDATE sales SET status='voided'`)
	rec := f.post(t, dxView, url.Values{}, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("view: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Nothing sold since the last day close.") {
		t.Errorf("void-only window must not show the empty note: %s", rec.Body.String())
	}
}

// After a PIN-approved view the on-screen "generated by" names the approver,
// like the paper (review F4).
func TestDayX_ElevatedViewNamesApprover(t *testing.T) {
	f := dxFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-v", "cash-v", "445566")
	u := &auth.User{ID: cashierID, Role: "cashier"}
	rec := f.post(t, dxView, url.Values{"override_pin": {"445566"}}, nil, u)
	if rec.Code != 200 {
		t.Fatalf("elevated view: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Erstellt von: EOD Manager") || strings.Contains(body, "Blocked Cashier") {
		t.Errorf("screen must name the approver, not the blocked user: %s", body)
	}
}
