package pages

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5841: POST /api/shifts/{id}/close-slip/print. Instants derive from
// time.Now() (the fixed-date guard).

const slipPrintPath = "/api/shifts/sh1/close-slip/print"

// shiftSlipClosedFixture is the xpFixture shift sh1, a mid-shift payout, then
// closed through pos.CloseShift with a 10000 skim.
func shiftSlipClosedFixture(t *testing.T) *xpFx {
	t.Helper()
	f := xpFixture(t)
	f.exec(t, `INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES('adj1','u1','shift','sh1','cash_adjustment','{"type":"payout","amount":-300,"reason":"Milk"}',?)`, xpAt(f.t0, 5*time.Minute))
	err := pos.CloseShift(t.Context(), f.dp.Db, pos.ShiftCloseInput{
		ShiftID: "sh1", ClosingCash: money.FromMinor(108000), Skim: money.FromMinor(10000), SkimApproverID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func shiftSlipAuditRows(t *testing.T, f *xpFx) [][3]sql.NullString {
	t.Helper()
	rows, err := f.dp.Db.Query(`SELECT actor_id, blocked_actor_id, data_json FROM audit_log WHERE action='shift_slip_printed' ORDER BY rowid`)
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

func shiftSlipFx(t *testing.T) (*xpFx, *fakeKitchenPrinter) {
	f := shiftSlipClosedFixture(t)
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	return f, p
}

func TestShiftCloseSlipPrint_PrintsOneJobAndAudits(t *testing.T) {
	f, p := shiftSlipFx(t)
	rec := f.post(t, slipPrintPath, url.Values{}, nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Shift slip sent to the printer") || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %q hdr=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-UT-Response"))
	}
	jobs := p.Settle(1)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d", len(jobs))
	}
	job := jobs[0]
	// stored: expected 98765-500(return)+10000-300 = 107965? read from the row.
	var counted, expected, newFloat int64
	if err := f.dp.Db.QueryRow(`SELECT closing_cash, expected_cash, new_float FROM shifts WHERE id='sh1'`).Scan(&counted, &expected, &newFloat); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Corner Shop", "SHIFT CLOSE", "Front Till", "Alex", "Closed", "Expected", "Counted", "Variance", "Skim to safe", "New float",
		"Payout (cash out): Milk", "R-0001"} {
		if !strings.Contains(job, want) {
			t.Errorf("job lacks %q:\n%s", want, job)
		}
	}
	if strings.Count(job, "Skim to safe") != 1 {
		t.Errorf("the at-close skim must print once (close block), not in the cash block:\n%s", job)
	}
	for _, bad := range []string{"X REPORT", "Not a day close", "END OF DAY", "Zeitraum", "\x1dk", string(print.DrawerKickBytes(2)), string(print.DrawerKickBytes(5))} {
		if strings.Contains(job, bad) {
			t.Errorf("job must not contain %q", bad)
		}
	}
	if counted != 108000 || newFloat != 98000 || expected == 0 {
		t.Errorf("row = %d %d %d", counted, expected, newFloat)
	}
	rows := shiftSlipAuditRows(t, f)
	if len(rows) != 1 || !strings.Contains(rows[0][2].String, `"register_id":"reg1"`) || rows[0][1].Valid {
		t.Fatalf("audit rows = %+v", rows)
	}
	var et, eid string
	if err := f.dp.Db.QueryRow(`SELECT entity_type, entity_id FROM audit_log WHERE action='shift_slip_printed'`).Scan(&et, &eid); err != nil || et != "shift" || eid != "sh1" {
		t.Errorf("entity = %q %q %v", et, eid, err)
	}
}

func TestShiftCloseSlipPrint_OpenUnknownEmptyIs404BeforePIN(t *testing.T) {
	f, p := shiftSlipFx(t)
	_, u := xpCashier(t, f)
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg3','Other',1)`)
	f.openShift(t, "shopen", "reg3", "u1", 0)
	cases := []struct {
		name, path string
		want       int
	}{
		{"open", "/api/shifts/shopen/close-slip/print", 404},
		{"unknown", "/api/shifts/nope/close-slip/print", 404},
		{"empty", "/api/shifts/%20/close-slip/print", 404},
		{"too long", "/api/shifts/" + strings.Repeat("a", 129) + "/close-slip/print", 400},
	}
	for _, c := range cases {
		rec := f.post(t, c.path, url.Values{"override_pin": {"000000"}}, nil, u)
		if rec.Code != c.want || rec.Header().Get("X-UT-Response") == "elevation-prompt" || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Errorf("%s: got %d %q %s", c.name, rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
		}
		if c.want == 404 && !strings.Contains(rec.Body.String(), "not closed yet") {
			t.Errorf("%s: body %s", c.name, rec.Body.String())
		}
	}
	rec := f.post(t, "/api/shifts/shopen/close-slip/print", url.Values{}, jsonHdr, nil)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"shift_not_closed"`) {
		t.Errorf("json: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestShiftCloseSlipPrint_PrinterOffIs502NoAudit(t *testing.T) {
	f := shiftSlipClosedFixture(t)
	if err := f.dp.Settings.Set(t.Context(), keyPrinterMode, "off"); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, slipPrintPath, url.Values{}, nil, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "Check the receipt printer") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 0 {
		t.Errorf("audit rows = %d", n)
	}
	f.get(t)
}

func TestShiftCloseSlipPrint_PrinterUnreachableIs502NoAudit(t *testing.T) {
	f := shiftSlipClosedFixture(t)
	setNetworkPrinter(t, f.dp, unreachableAddr())
	rec := f.post(t, slipPrintPath, url.Values{}, nil, nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "Check the receipt printer") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 0 {
		t.Errorf("audit rows = %d", n)
	}
}

func TestShiftCloseSlipPrint_ElevationPromptPrintsNothing(t *testing.T) {
	f, p := shiftSlipFx(t)
	_, u := xpCashier(t, f)
	rec := f.post(t, slipPrintPath, url.Values{}, nil, u)
	body := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	for _, want := range []string{slipPrintPath, "#shift-slip-print-msg", "Front Till"} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt lacks %q: %s", want, body)
		}
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestShiftCloseSlipPrint_WrongPINReprompts(t *testing.T) {
	f, p := shiftSlipFx(t)
	_, u := xpCashier(t, f)
	rec := f.post(t, slipPrintPath, url.Values{"override_pin": {"000000"}}, nil, u)
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestShiftCloseSlipPrint_ManagerPINPrintsAndRecordsApprover(t *testing.T) {
	f, p := shiftSlipFx(t)
	mgr, u := xpCashier(t, f)
	rec := f.post(t, slipPrintPath, url.Values{"override_pin": {"445566"}}, nil, u)
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if n := len(p.Settle(1)); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	rows := shiftSlipAuditRows(t, f)
	if len(rows) != 1 || rows[0][0].String != mgr || rows[0][1].String != u.ID {
		t.Fatalf("rows = %+v (mgr %s cashier %s)", rows, mgr, u.ID)
	}
}

func TestShiftCloseSlipPrint_PermittedRoleNeedsNoPIN(t *testing.T) {
	f, p := shiftSlipFx(t)
	t.Setenv("UT_AUTH", "on")
	mgr, _ := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	rec := f.post(t, slipPrintPath, url.Values{}, nil, &auth.User{ID: mgr, Role: "manager"})
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(1)); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	rows := shiftSlipAuditRows(t, f)
	if len(rows) != 1 || rows[0][0].String != mgr || rows[0][1].Valid {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestShiftCloseSlipPrint_AuthOffNoPIN(t *testing.T) {
	f, p := shiftSlipFx(t) // xpFixture sets UT_AUTH=off
	rec := f.post(t, slipPrintPath, url.Values{}, nil, nil)
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(1)); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
}

func TestShiftCloseSlipPrint_PINInQueryIgnored(t *testing.T) {
	f, p := shiftSlipFx(t)
	_, u := xpCashier(t, f)
	rec := f.post(t, slipPrintPath+"?override_pin=445566", url.Values{}, nil, u)
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("a PIN in the query must be ignored: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
	if n := len(shiftSlipAuditRows(t, f)); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestShiftCloseSlipPrint_JSONElevationRequired(t *testing.T) {
	f, p := shiftSlipFx(t)
	_, u := xpCashier(t, f)
	rec := f.post(t, slipPrintPath, url.Values{}, jsonHdr, u)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"manager_pin_required"`) || strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := len(p.Settle(0)); n != 0 {
		t.Errorf("jobs = %d", n)
	}
}

func TestShiftCloseSlipPrint_JSON(t *testing.T) {
	f, _ := shiftSlipFx(t)
	rec := f.post(t, slipPrintPath, url.Values{}, jsonHdr, nil)
	if rec.Code != 200 || strings.ReplaceAll(strings.TrimSpace(rec.Body.String()), " ", "") != `{"data":{"printed":true,"shift_id":"sh1"},"error":null}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
