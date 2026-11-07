package pages

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5798: POST /api/shifts/{id}/x-report/print. Every instant derives
// from time.Now() (the fixed-date guard).

const xpPrintPath = "/api/shifts/sh1/x-report/print"

// xpAuditRows returns (actor_id, blocked_actor_id, data_json) of every
// x_report_printed row.
func xpAuditRows(t *testing.T, f *xpFx) [][3]sql.NullString {
	t.Helper()
	rows, err := f.dp.Db.Query(`SELECT actor_id, blocked_actor_id, data_json FROM audit_log WHERE action='x_report_printed' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]sql.NullString
	for rows.Next() {
		var r [3]sql.NullString
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestXReportPrint_PrintsOneJobAndAudits(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	rec := f.post(t, xpPrintPath, url.Values{}, nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "X report sent to the printer") || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %q hdr=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-UT-Response"))
	}
	jobs := p.Settle(1)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	rep, err := data.NewPOSRepo(f.dp.Db).ShiftXReport(t.Context(), "sh1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	job := jobs[0]
	for _, want := range []string{"Corner Shop", "X REPORT", "Not a day close. Nothing is closed.", "Front Till", "Alex", "R-0001",
		httpx.FormatMoneyPrint(rep.Cash.Expected.Minor(), "en", "utf8")} {
		if !strings.Contains(job, want) {
			t.Errorf("job lacks %q:\n%s", want, job)
		}
	}
	endOfDay := strings.TrimSpace(strings.ReplaceAll(httpx.T("en", "reports.zpaper.end_of_day"), "%s", ""))
	for _, bad := range []string{"END OF DAY", "Zeitraum", endOfDay, "\x1dk", string(print.DrawerKickBytes(2)), string(print.DrawerKickBytes(5))} {
		if bad != "" && strings.Contains(job, bad) {
			t.Errorf("job must not contain %q", bad)
		}
	}
	rows := xpAuditRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0][0].String != "system" || rows[0][1].Valid || !strings.Contains(rows[0][2].String, `"register_id":"reg1"`) {
		t.Errorf("audit row = %+v", rows[0])
	}
	var et, eid string
	if err := f.dp.Db.QueryRow(`SELECT entity_type, entity_id FROM audit_log WHERE action='x_report_printed'`).Scan(&et, &eid); err != nil || et != "shift" || eid != "sh1" {
		t.Errorf("entity = %q %q %v", et, eid, err)
	}
}

func TestXReportPrint_ReadsFreshFigures(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	f.sale(t, "s2", "R-0003", "sale", "reg1", 1234, 0, 3*time.Minute)
	f.pay(t, "p2", "s2", "cash", 1234)
	if rec := f.post(t, xpPrintPath, url.Values{}, nil, nil); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	job := p.Settle(1)[0]
	if !strings.Contains(job, "Sales (2)") || !strings.Contains(job, "R-0003") {
		t.Fatalf("job not fresh:\n%s", job)
	}
}

func TestXReportPrint_PrinterOffIs502NoAudit(t *testing.T) {
	f := xpFixture(t)
	if err := f.dp.Settings.Set(t.Context(), keyPrinterMode, "off"); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, xpPrintPath, url.Values{}, nil, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "Print failed") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(xpAuditRows(t, f)); n != 0 {
		t.Errorf("audit rows = %d", n)
	}
	f.get(t)
}

func TestXReportPrint_PrinterUnreachableIs502NoAudit(t *testing.T) {
	f := xpFixture(t)
	setNetworkPrinter(t, f.dp, unreachableAddr())
	rec := f.post(t, xpPrintPath, url.Values{}, nil, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "Print failed") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(xpAuditRows(t, f)); n != 0 {
		t.Errorf("audit rows = %d", n)
	}
}

func TestXReportPrint_JSON(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	rec := f.post(t, xpPrintPath, url.Values{}, jsonHdr, nil)
	if rec.Code != 200 || strings.ReplaceAll(strings.TrimSpace(rec.Body.String()), " ", "") != `{"data":{"printed":true,"shift_id":"sh1"},"error":null}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if err := f.dp.Settings.Set(t.Context(), keyPrinterMode, "off"); err != nil {
		t.Fatal(err)
	}
	rec = f.post(t, xpPrintPath, url.Values{}, jsonHdr, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), `"print_failed"`) {
		t.Fatalf("off: got %d %s", rec.Code, rec.Body.String())
	}
}

func xpCashier(t *testing.T, f *xpFx) (mgrID string, u *auth.User) {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	mgr, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	return mgr, &auth.User{ID: cashierID, Role: "cashier"}
}

func TestXReportPrint_ElevationPromptPrintsNothing(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	_, u := xpCashier(t, f)
	rec := f.post(t, xpPrintPath, url.Values{}, nil, u)
	body := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	for _, want := range []string{xpPrintPath, "#x-report-print-msg", "Front Till"} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "987.65") || strings.Contains(body, "R-0001") {
		t.Error("prompt must carry no figures")
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(xpAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestXReportPrint_WrongPINReprompts(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	_, u := xpCashier(t, f)
	rec := f.post(t, xpPrintPath, url.Values{"override_pin": {"000000"}}, nil, u)
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(xpAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestXReportPrint_ManagerPINPrintsAndRecordsApprover(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	mgr, u := xpCashier(t, f)
	rec := f.post(t, xpPrintPath, url.Values{"override_pin": {"445566"}}, nil, u)
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if n := len(p.Settle(1)); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	rows := xpAuditRows(t, f)
	if len(rows) != 1 || rows[0][0].String != mgr || rows[0][1].String != u.ID {
		t.Fatalf("rows = %+v (mgr %s cashier %s)", rows, mgr, u.ID)
	}
}

func TestXReportPrint_PermittedRoleNeedsNoPIN(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	t.Setenv("UT_AUTH", "on")
	mgr, _ := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	rec := f.post(t, xpPrintPath, url.Values{}, nil, &auth.User{ID: mgr, Role: "manager"})
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(1)); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	rows := xpAuditRows(t, f)
	if len(rows) != 1 || rows[0][0].String != mgr || rows[0][1].Valid {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestXReportPrint_PINInQueryIgnored(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	_, u := xpCashier(t, f)
	rec := f.post(t, xpPrintPath+"?override_pin=445566", url.Values{}, nil, u)
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("a PIN in the query must be ignored: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(xpAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestXReportPrint_JSONElevationRequired(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	_, u := xpCashier(t, f)
	rec := f.post(t, xpPrintPath, url.Values{}, jsonHdr, u)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"manager_pin_required"`) || strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
}

func TestXReportPrint_NotFoundBeforePIN(t *testing.T) {
	f := xpFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	_, u := xpCashier(t, f)
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg3','Closed Till',1)`)
	f.exec(t, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash) VALUES('shc','reg3','u1',?,?,0)`, xpAt(f.t0, -time.Hour), xpAt(f.t0, -30*time.Minute))
	cases := []struct {
		name, id string
		want     int
	}{
		{"unknown", "nope", 404},
		{"closed", "shc", 404},
		{"too long", strings.Repeat("a", 129), 400},
	}
	for _, c := range cases {
		rec := f.post(t, "/api/shifts/"+c.id+"/x-report/print", url.Values{}, nil, u)
		if rec.Code != c.want || rec.Header().Get("X-UT-Response") == "elevation-prompt" || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Errorf("%s: got %d %q %s", c.name, rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
		}
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(xpAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestShiftXReport_FragmentHasPrintButton(t *testing.T) {
	f := xpFixture(t)
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil)
	body := rec.Body.String()
	for _, want := range []string{`hx-post="/api/shifts/sh1/x-report/print"`, `hx-target="#x-report-print-msg"`, `id="x-report-print-msg"`, "Print X report"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment lacks %q", want)
		}
	}
	if strings.Contains(body, "showModal") {
		t.Error("no showModal")
	}
}
