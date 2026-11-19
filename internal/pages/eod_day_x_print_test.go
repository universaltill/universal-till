package pages

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/print"
)

func dxAuditRows(t *testing.T, f *xpFx) [][5]sql.NullString {
	t.Helper()
	rows, err := f.dp.Db.Query(`SELECT actor_id, blocked_actor_id, entity_type, entity_id, data_json FROM audit_log WHERE action='x_day_report_printed' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][5]sql.NullString
	for rows.Next() {
		var r [5]sql.NullString
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3], &r[4]); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestDayXPrint_PrintsOneJobAndAudits(t *testing.T) {
	f := dxFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	rec := f.post(t, dxPrint, url.Values{}, nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Day-so-far report sent to the printer") || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %q hdr=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-UT-Response"))
	}
	jobs := p.Settle(1)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d", len(jobs))
	}
	for _, want := range []string{"Corner Shop", "X REPORT - DAY SO FAR", "Not a day close. Nothing is closed.", "Zeitraum bis "} {
		if !strings.Contains(jobs[0], want) {
			t.Errorf("job lacks %q:\n%s", want, jobs[0])
		}
	}
	for _, bad := range []string{"END OF DAY", "Z no.", "Anmerkung", string(print.DrawerKickBytes(2)), string(print.DrawerKickBytes(5))} {
		if strings.Contains(jobs[0], bad) {
			t.Errorf("job must not contain %q", bad)
		}
	}
	rows := dxAuditRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r[0].String != "system" || r[1].Valid || r[2].String != "report" || r[3].String == "" {
		t.Errorf("audit row = %+v", r)
	}
	for _, k := range []string{`"from"`, `"to"`, `"sales"`, `"net"`} {
		if !strings.Contains(r[4].String, k) {
			t.Errorf("payload lacks %s: %s", k, r[4].String)
		}
	}
	if f.count(t, "report_archive") != 0 {
		t.Error("printing must not archive")
	}
	var n int
	if err := f.dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='eod_generated'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("eod_generated rows = %d", n)
	}
}

func TestDayXPrint_ViewWritesNoAudit(t *testing.T) {
	f := dxFixture(t)
	if rec := f.post(t, dxView, url.Values{}, nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "X REPORT - DAY SO FAR") {
		t.Fatalf("view: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(dxAuditRows(t, f)); n != 0 {
		t.Errorf("viewing wrote %d audit rows", n)
	}
}

func TestDayXPrint_PrinterOffOrDownIs502NoAudit(t *testing.T) {
	f := dxFixture(t)
	if err := f.dp.Settings.Set(t.Context(), keyPrinterMode, "off"); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, dxPrint, url.Values{}, nil, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "Check the receipt printer") {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	setNetworkPrinter(t, f.dp, unreachableAddr())
	rec = f.post(t, dxPrint, url.Values{}, jsonHdr, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), `"print_failed"`) {
		t.Fatalf("down: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(dxAuditRows(t, f)); n != 0 {
		t.Errorf("audit rows = %d", n)
	}
	if rec := f.post(t, dxView, url.Values{}, nil, nil); rec.Code != 200 {
		t.Errorf("tab must keep rendering: %d", rec.Code)
	}
}

func TestDayXPrint_JSON(t *testing.T) {
	f := dxFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	rec := f.post(t, dxPrint, url.Values{}, jsonHdr, nil)
	b := strings.ReplaceAll(rec.Body.String(), " ", "")
	if rec.Code != 200 || !strings.Contains(b, `"printed":true`) || !strings.Contains(b, `"to":"`) || !strings.Contains(b, `"error":null`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestDayXPrint_ElevationPromptAndDualAttribution(t *testing.T) {
	f := dxFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	t.Setenv("UT_AUTH", "on")
	mgr, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	u := &auth.User{ID: cashierID, Role: "cashier"}

	rec := f.post(t, dxPrint, url.Values{}, nil, u)
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" || len(p.Tickets()) != 0 || len(dxAuditRows(t, f)) != 0 {
		t.Fatalf("prompt must print and audit nothing: %d", rec.Code)
	}
	if rec := f.post(t, dxPrint, url.Values{}, jsonHdr, u); rec.Code != 403 {
		t.Errorf("JSON gate: %d", rec.Code)
	}
	rec = f.post(t, dxPrint, url.Values{"override_pin": {"445566"}}, nil, u)
	if rec.Code != 200 {
		t.Fatalf("elevated print: %d %s", rec.Code, rec.Body.String())
	}
	p.Settle(1)
	rows := dxAuditRows(t, f)
	if len(rows) != 1 || rows[0][0].String != mgr || rows[0][1].String != cashierID {
		t.Errorf("want approver %q blocked %q, got %+v", mgr, cashierID, rows)
	}
}
