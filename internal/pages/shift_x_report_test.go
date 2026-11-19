package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5797: the X report on /shifts. Every instant derives from
// time.Now() (the fixed-date guard).

type xpFx struct {
	mux *http.ServeMux
	dp  *common.Deps
	t0  time.Time
}

func xpAt(t0 time.Time, d time.Duration) string {
	return t0.Add(d).UTC().Format("2006-01-02T15:04:05Z")
}

func (f *xpFx) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.dp.Db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// xpFixture: user u1 "Alex", register reg1 "Front Till" with an open shift
// (opening 10000) and one cash sale of 98765 with 1000 tax, plus a return.
func xpFixture(t *testing.T) *xpFx {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsPageTestDeps(t)
	f := &xpFx{mux: mux, dp: dp, t0: time.Now().UTC().Add(-2 * time.Hour)}
	f.exec(t, `INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('u1','alex','Alex','','cashier',1)`)
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`)
	f.openShift(t, "sh1", "reg1", "u1", 10000)
	f.sale(t, "s1", "R-0001", "sale", "reg1", 98765, 1000, time.Minute)
	f.pay(t, "p1", "s1", "cash", 98765)
	f.sale(t, "r1", "R-0002", "return", "reg1", 500, 50, 2*time.Minute)
	f.pay(t, "pr1", "r1", "cash", 500)
	return f
}

func (f *xpFx) openShift(t *testing.T, id, reg, cashier string, opening int64) {
	t.Helper()
	f.exec(t, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES(?,?,?,?,?)`, id, reg, cashier, xpAt(f.t0, 0), opening)
}

func (f *xpFx) sale(t *testing.T, id, receipt, typ, reg string, total, tax int64, after time.Duration) {
	t.Helper()
	at := xpAt(f.t0, after)
	f.exec(t, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, cashier_id, created_at, completed_at)
VALUES(?, ?, 'completed', ?, 'GBP', ?, 0, ?, ?, ?, 'u1', ?, ?)`, id, receipt, typ, total, tax, total, reg, at, at)
}

func (f *xpFx) pay(t *testing.T, id, sale, method string, amount int64) {
	t.Helper()
	f.exec(t, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES(?, ?, ?, ?, 'GBP', 0, 0, ?)`, id, sale, method, amount, xpAt(f.t0, 90*time.Second))
}

func (f *xpFx) post(t *testing.T, target string, form url.Values, hdr map[string]string, user *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *xpFx) get(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shifts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shifts = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func (f *xpFx) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.dp.Db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var jsonHdr = map[string]string{"Accept": "application/json"}

func TestPickXShift(t *testing.T) {
	open := []data.OpenShift{
		{ShiftID: "a", RegisterID: "ra", OpenedAt: "2026-01-01T10:00:00Z"},
		{ShiftID: "b", RegisterID: "rb", OpenedAt: "2026-01-01T12:00:00Z"},
		{ShiftID: "c", RegisterID: "rc", OpenedAt: "2026-01-01T12:00:00Z"},
	}
	cases := []struct {
		name      string
		open      []data.OpenShift
		reg, till string
		wantID    string
		wantOK    bool
	}{
		{"explicit", open, "ra", "rb", "a", true},
		{"explicit unknown", open, "zz", "rb", "", false},
		{"empty uses till register", open, "", "ra", "a", true},
		{"empty uses till register, not the first or newest", open, "", "rb", "b", true}, // Tester: "ra" alone matched open[0]
		{"empty, till register not open -> newest, tie larger id", open, "", "rx", "c", true},
		{"empty, no till register -> newest", open[:2], "", "", "b", true},
		{"empty list", nil, "", "ra", "", false},
	}
	for _, c := range cases {
		got, ok := pickXShift(c.open, c.reg, c.till)
		if ok != c.wantOK || got.ShiftID != c.wantID {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got.ShiftID, ok, c.wantID, c.wantOK)
		}
	}
}

func TestShiftsPage_XSectionWithoutFigures(t *testing.T) {
	f := xpFixture(t)
	body := f.get(t)
	for _, want := range []string{`<section id="x-report"`, `hx-post="/shifts/x-report"`, `id="x-report-body"`, "X report", "Nothing is closed or reset"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /shifts missing %q", want)
		}
	}
	for _, leak := range []string{"987.65", "98,765", "1,087.65", "1087.65"} {
		if strings.Contains(body, leak) {
			t.Errorf("GET /shifts leaks a figure %q", leak)
		}
	}
	if strings.Contains(body, "<details id=\"x-report\"") {
		t.Error("X section must not be a <details>")
	}
}

func TestShiftsPage_NoOpenShiftHasNoXSection(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newShiftsPageTestDeps(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shifts", nil))
	body := rec.Body.String()
	if strings.Contains(body, `id="x-report"`) || strings.Contains(body, `hx-post="/shifts/x-report"`) {
		t.Error("no open shift: the X section must be absent")
	}
	if !strings.Contains(body, "No open shift") {
		t.Error("expected the none-open text")
	}
}

func TestShiftsPage_CurrentCardShowsNames(t *testing.T) {
	f := xpFixture(t)
	body := f.get(t)
	if !strings.Contains(body, "Alex") || !strings.Contains(body, "Front Till") {
		t.Fatalf("expected cashier and register names, got:\n%s", body)
	}
	if strings.Contains(body, "<td>u1</td>") || strings.Contains(body, "<td>reg1</td>") {
		t.Error("raw ids must not show when names exist")
	}
	// username only, then raw id.
	f.exec(t, `UPDATE users SET display_name='' WHERE id='u1'`)
	if b := f.get(t); !strings.Contains(b, "<td>alex</td>") {
		t.Error("expected username fallback")
	}
	f.exec(t, `PRAGMA foreign_keys = OFF`)
	f.exec(t, `UPDATE shifts SET cashier_id='ghost' WHERE id='sh1'`)
	if b := f.get(t); !strings.Contains(b, "<td>ghost</td>") {
		t.Error("expected raw id fallback")
	}
}

func TestShiftsPage_XPickerOnlyWhenSeveralOpen(t *testing.T) {
	f := xpFixture(t)
	b := f.get(t)
	if !strings.Contains(b, `<input type="hidden" name="register" value="reg1">`) || strings.Contains(b, `<select name="register"`) {
		t.Fatalf("one open register: want hidden input and no select")
	}
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`)
	f.openShift(t, "sh2", "reg2", "u1", 0)
	if err := f.dp.Settings.Set(t.Context(), pos.SettingsKeyTillRegisterID, "reg2"); err != nil {
		t.Fatal(err)
	}
	b = f.get(t)
	if !strings.Contains(b, `<select name="register"`) || !strings.Contains(b, `value="reg2" selected`) || !strings.Contains(b, "Back Till") || !strings.Contains(b, "Front Till") {
		t.Fatalf("two open registers: want select with both, till register selected:\n%s", b)
	}
}

func TestShiftXReport_HTMLMatchesRepo(t *testing.T) {
	f := xpFixture(t)
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	rep, err := data.NewPOSRepo(f.dp.Db).ShiftXReport(t.Context(), "sh1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fn := httpx.FuncsFor("en")["money"].(func(any) string)
	for _, m := range []any{rep.SalesGross, rep.ReturnsTotal, rep.Net, rep.Tax, rep.Cash.Expected} {
		if !strings.Contains(body, fn(m)) {
			t.Errorf("fragment missing %s", fn(m))
		}
	}
	for _, want := range []string{"Figures as of", "Cash", "Hide X report", "Front Till", "Alex", "R-0001", "R-0002"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	if rep.Cash.Expected != rep.Cash.Opening.Add(rep.Cash.CashSales).Add(rep.Cash.TipsHeldOut).Add(rep.Cash.AdjustmentsTotal) {
		t.Error("repo cash breakdown does not reconcile")
	}
	if strings.Contains(body, "End of day") || strings.Contains(body, "Z-report") || strings.Contains(body, "Z report") {
		t.Error("no Z wording in the X fragment")
	}
	// ut-docs#5798: the only live region is the print status; the figures
	// themselves must not be announced.
	if strings.Count(body, `aria-live`) != 1 || !strings.Contains(body, `id="x-report-print-msg" class="x-report-print-msg" aria-live="polite"`) {
		t.Error("the print status is the only live region of the report")
	}
}

func TestShiftXReport_ElevationPromptWithoutFigures(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	u := &auth.User{ID: cashierID, Role: "cashier"}
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, u)
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("want 200 elevation prompt, got %d %q", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="register"`) || !strings.Contains(body, `value="reg1"`) || !strings.Contains(body, "/shifts/x-report") || !strings.Contains(body, "Front Till") {
		t.Errorf("prompt must replay register and name it: %s", body)
	}
	if strings.Contains(body, "987.65") || strings.Contains(body, "R-0001") {
		t.Error("prompt must carry no figures")
	}
}

func TestShiftXReport_WrongPINReprompts(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}, "override_pin": {"000000"}}, nil, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" || strings.Contains(rec.Body.String(), "R-0001") {
		t.Fatalf("wrong PIN must re-prompt without figures: %d %s", rec.Code, rec.Body.String())
	}
}

func TestShiftXReport_ManagerPINShowsFigures(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	before := f.count(t, "audit_log")
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}, "override_pin": {"445566"}}, nil, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "R-0001") {
		t.Fatalf("valid PIN: want figures, got %d %s", rec.Code, rec.Body.String())
	}
	if after := f.count(t, "audit_log"); after != before {
		t.Errorf("viewing wrote %d audit rows", after-before)
	}
}

func TestShiftXReport_PermittedRoleNeedsNoPIN(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	mgrID, _ := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, &auth.User{ID: mgrID, Role: "manager"})
	if rec.Code != 200 || rec.Header().Get("X-UT-Response") != "" || !strings.Contains(rec.Body.String(), "R-0001") {
		t.Fatalf("manager: want figures, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestShiftXReport_PINInQueryIgnored(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	rec := f.post(t, "/shifts/x-report?override_pin=445566&register=reg1", url.Values{}, nil, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" || strings.Contains(rec.Body.String(), "R-0001") {
		t.Fatalf("a PIN in the query string must be ignored: %d %s", rec.Code, rec.Body.String())
	}
}

func TestShiftXReport_OtherRegister(t *testing.T) {
	f := xpFixture(t)
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`)
	f.openShift(t, "sh2", "reg2", "u1", 0)
	f.sale(t, "s2", "B-0001", "sale", "reg2", 4242, 0, time.Minute)
	f.pay(t, "p2", "s2", "card", 4242)
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg2"}}, nil, nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Back Till") || !strings.Contains(body, "42.42") || strings.Contains(body, "987.65") {
		t.Fatalf("other register: %d %s", rec.Code, body)
	}
}

func TestShiftXReport_NotFound(t *testing.T) {
	f := xpFixture(t)
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg3','Closed Till',1)`)
	f.exec(t, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash) VALUES('shc','reg3','u1',?,?,0)`, xpAt(f.t0, -time.Hour), xpAt(f.t0, -30*time.Minute))
	for _, reg := range []string{"nope", "reg3"} {
		for _, asJSON := range []bool{false, true} {
			hdr := map[string]string(nil)
			if asJSON {
				hdr = jsonHdr
			}
			rec := f.post(t, "/shifts/x-report", url.Values{"register": {reg}}, hdr, nil)
			if rec.Code != 404 {
				t.Fatalf("%s json=%v: status %d", reg, asJSON, rec.Code)
			}
			if asJSON && !strings.Contains(rec.Body.String(), `"no_open_shift"`) {
				t.Errorf("json code missing: %s", rec.Body.String())
			}
			if !asJSON && !strings.Contains(rec.Body.String(), `<div class='error'`) {
				t.Errorf("html error fragment missing: %s", rec.Body.String())
			}
		}
	}
	f.exec(t, `UPDATE shifts SET closed_at=? WHERE id='sh1'`, xpAt(f.t0, time.Hour))
	if rec := f.post(t, "/shifts/x-report", url.Values{}, nil, nil); rec.Code != 404 {
		t.Fatalf("no open shift at all: status %d", rec.Code)
	}
}

func TestShiftXReport_RegisterTooLong(t *testing.T) {
	f := xpFixture(t)
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {strings.Repeat("x", 129)}}, jsonHdr, nil)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_register"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestShiftXReport_JSONShape(t *testing.T) {
	f := xpFixture(t)
	f.exec(t, `UPDATE shifts SET opened_at=? WHERE id='sh1'`, f.t0.Format("2006-01-02 15:04:05")) // legacy format
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, jsonHdr, nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var env struct {
		Data  map[string]any `json:"data"`
		Error any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("error = %v", env.Error)
	}
	for _, k := range []string{"shift_id", "register_id", "register_name", "cashier_id", "cashier_name", "opened_at", "generated_at", "currency", "sales", "returns", "net_minor", "tax_minor", "voids", "cleared_baskets", "tenders", "cash", "no_sale", "first_receipt", "last_receipt"} {
		if _, ok := env.Data[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	rep, _ := data.NewPOSRepo(f.dp.Db).ShiftXReport(t.Context(), "sh1", time.Now())
	sales := env.Data["sales"].(map[string]any)
	cash := env.Data["cash"].(map[string]any)
	if int64(sales["gross_minor"].(float64)) != rep.SalesGross.Minor() || int64(env.Data["net_minor"].(float64)) != rep.Net.Minor() ||
		int64(env.Data["tax_minor"].(float64)) != rep.Tax.Minor() || int64(cash["expected_minor"].(float64)) != rep.Cash.Expected.Minor() {
		t.Errorf("minor figures differ from repo: %v", env.Data)
	}
	for _, k := range []string{"opened_at", "generated_at"} {
		if _, err := time.Parse(time.RFC3339, env.Data[k].(string)); err != nil {
			t.Errorf("%s not RFC 3339: %v", k, env.Data[k])
		}
	}
	// Tester: pin the tender line's keys and values too (a renamed tender tag
	// survived the original assertions).
	tenders := env.Data["tenders"].([]any)
	if len(tenders) != len(rep.Methods) || len(tenders) == 0 {
		t.Fatalf("tenders = %v, repo methods = %+v", tenders, rep.Methods)
	}
	t0 := tenders[0].(map[string]any)
	m0 := rep.Methods[0]
	if t0["method_id"] != m0.MethodID || t0["name"] != m0.Name || t0["type"] != m0.Type ||
		int64(t0["in_minor"].(float64)) != m0.In.Minor() || int64(t0["refunded_minor"].(float64)) != m0.Refunded.Minor() ||
		int64(t0["net_minor"].(float64)) != m0.Net.Minor() || int64(t0["tips_minor"].(float64)) != m0.Tips.Minor() {
		t.Errorf("tender[0] = %v, repo = %+v", t0, m0)
	}
	if env.Data["currency"] != "GBP" {
		t.Errorf("currency = %v", env.Data["currency"])
	}
	// zero shift: slices encode as [], never null.
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg9','Empty Till',1)`)
	f.openShift(t, "sh9", "reg9", "u1", 100)
	raw := f.post(t, "/shifts/x-report", url.Values{"register": {"reg9"}}, jsonHdr, nil).Body.String()
	for _, want := range []string{`"tenders":[]`, `"adjustments":[]`, `"operators":[]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("zero shift body missing %s: %s", want, raw)
		}
	}
}

func TestShiftXReport_JSONElevationRequired(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, jsonHdr, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"manager_pin_required"`) || !strings.Contains(rec.Body.String(), `"data":null`) ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec = f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}, "override_pin": {"445566"}}, jsonHdr, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Code != 200 {
		t.Fatalf("JSON with valid override_pin: %d %s", rec.Code, rec.Body.String())
	}
}

func TestShiftXReport_ReadOnly(t *testing.T) {
	f := xpFixture(t)
	tables := []string{"report_archive", "audit_log", "settings", "sales", "payments"}
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = f.count(t, tb)
	}
	for i := 0; i < 3; i++ {
		if rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil); rec.Code != 200 {
			t.Fatalf("post %d: %d", i, rec.Code)
		}
	}
	for _, tb := range tables {
		if n := f.count(t, tb); n != before[tb] {
			t.Errorf("%s rows %d -> %d", tb, before[tb], n)
		}
	}
	var closing, expected any
	var closedAt any
	if err := f.dp.Db.QueryRow(`SELECT closed_at, closing_cash, expected_cash FROM shifts WHERE id='sh1'`).Scan(&closedAt, &closing, &expected); err != nil {
		t.Fatal(err)
	}
	if closedAt != nil || closing != nil || expected != nil {
		t.Errorf("shift mutated: %v %v %v", closedAt, closing, expected)
	}
}

func TestShiftXReport_DoesNotPersistTillRegister(t *testing.T) {
	f := xpFixture(t)
	if _, ok, _ := f.dp.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); ok {
		t.Fatal("precondition: till register already set")
	}
	if rec := f.post(t, "/shifts/x-report", url.Values{}, nil, nil); rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if v, ok, _ := f.dp.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); ok && v != "" {
		t.Errorf("the X endpoint persisted the till register: %q", v)
	}
}

func TestShiftXReport_ZeroDecimalCurrency(t *testing.T) {
	f := xpFixture(t)
	pinCurrency(t, "JPY")
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil)
	if !strings.Contains(rec.Body.String(), "¥500") || strings.Contains(rec.Body.String(), "5.00") {
		t.Errorf("JPY: want ¥500 unscaled: %s", rec.Body.String())
	}
}

func TestShiftXReport_EmptyShiftShowsZeros(t *testing.T) {
	f := xpFixture(t)
	f.exec(t, `INSERT INTO registers(id,name,is_active) VALUES('reg9','Empty Till',1)`)
	f.openShift(t, "sh9", "reg9", "u1", 7700)
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg9"}}, nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No payments on this shift yet.") || !strings.Contains(rec.Body.String(), "£77.00") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// Tester (ut-docs#5797): the JSON lockout branch. Wrong PINs are 403 until
// the device-wide limiter trips; from then on even the RIGHT PIN is a 429
// manager_pin_required with no figures.
func TestShiftXReport_JSONLockoutIs429(t *testing.T) {
	f := xpFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-x", "cash-x", "445566")
	u := &auth.User{ID: cashierID, Role: "cashier"}
	got429 := false
	for i := 0; i < 10 && !got429; i++ {
		rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}, "override_pin": {"000000"}}, jsonHdr, u)
		switch rec.Code {
		case http.StatusForbidden:
		case http.StatusTooManyRequests:
			got429 = true
		default:
			t.Fatalf("attempt %d: status %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if !got429 {
		t.Fatal("repeated wrong PINs never reached the 429 lockout")
	}
	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}, "override_pin": {"445566"}}, jsonHdr, u)
	body := rec.Body.String()
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(body, `"manager_pin_required"`) || !strings.Contains(body, `"data":null`) ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || strings.Contains(body, "R-0001") {
		t.Fatalf("locked out, right PIN: want 429 JSON without figures, got %d %q %s", rec.Code, rec.Header().Get("Content-Type"), body)
	}
}

// Tester (ut-docs#5797): a built-in method's stored name is a locale key
// ("tender.cash", migration 022); the tender table must show the translated
// label, as the sale screen's pay buttons do ({{ T .Name }}), never the key.
func TestShiftXReport_TenderNamesTranslated(t *testing.T) {
	f := xpFixture(t)
	// seedForPages overwrites the name with a literal "Cash"; restore what
	// migration 022 really ships, which is what a live till has.
	f.exec(t, `UPDATE payment_methods SET name='tender.cash' WHERE id='cash'`)
	body := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil).Body.String()
	if strings.Contains(body, "tender.cash") {
		t.Errorf("tender table shows the raw locale key: %s", body)
	}
	if !strings.Contains(body, "<td>Cash</td>") {
		t.Errorf("tender table missing the translated Cash label: %s", body)
	}
}
