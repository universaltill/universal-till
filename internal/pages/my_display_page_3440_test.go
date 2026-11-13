package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3440 (personal display, part 3/3): the /my-display page and its
// two POST endpoints. Migration 102 grants personal_display to
// admin/manager/super_admin; cashier stays sale-only.

const md3440Till = "slate"

type md3440Rig struct {
	mux *http.ServeMux
	d   *common.Deps
}

func newMD3440Rig(t *testing.T) md3440Rig {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	initPagesI18n(t)
	d := newDesignerTestDeps(t)
	st := d.CurrentState()
	st.Theme = md3440Till
	d.SetState(st)
	wireThemeAvailable(d)
	for _, u := range []struct{ id, role string }{{"u-mgr", "manager"}, {"u-cash", "cashier"}} {
		if _, err := d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES (?,?,?,'x',?)`, u.id, u.id, u.id, u.role); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
	}
	mux := http.NewServeMux()
	registerMyDisplayPage(mux, d)
	registerThemeSync(mux, d)
	return md3440Rig{mux: mux, d: d}
}

func (r md3440Rig) do(method, path, user, role string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req = auth.WithUser(req, auth.User{ID: user, Role: role})
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func (r md3440Rig) row(t *testing.T, user, key string) (string, bool) {
	t.Helper()
	v, ok, err := data.NewUserDisplayRepo(r.d.Db).Get(context.Background(), user, key)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

func (r md3440Rig) seed(t *testing.T, user, key, val string) {
	t.Helper()
	if err := data.NewUserDisplayRepo(r.d.Db).Set(context.Background(), user, key, val); err != nil {
		t.Fatal(err)
	}
}

func TestMyDisplay_GetManager200CashierForbidden(t *testing.T) {
	r := newMD3440Rig(t)
	if rec := r.do("GET", "/my-display", "u-cash", "cashier", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET = %d, want 403", rec.Code)
	}
	rec := r.do("GET", "/my-display", "u-mgr", "manager", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d: %.300s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`name="theme"`, `name="mode"`, "Use the till&#39;s choice (Slate)", "My display"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%.1500s", want, body)
		}
	}
}

func TestMyDisplay_SetAndClearTheme(t *testing.T) {
	r := newMD3440Rig(t)
	if rec := r.do("POST", "/api/my-display/theme", "u-mgr", "manager", url.Values{"theme": {"amber"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("set = %d: %s", rec.Code, rec.Body.String())
	}
	if v, ok := r.row(t, "u-mgr", common.KeyTheme); !ok || v != "amber" {
		t.Fatalf("row = %q,%v want amber", v, ok)
	}
	if rec := r.do("POST", "/api/my-display/theme", "u-mgr", "manager", url.Values{"theme": {""}}); rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := r.row(t, "u-mgr", common.KeyTheme); ok {
		t.Fatal("row still present after clear")
	}
}

func TestMyDisplay_UnknownThemeAndGarbage400(t *testing.T) {
	r := newMD3440Rig(t)
	for _, v := range []string{"nope", strings.Repeat("x", 200), "../etc"} {
		rec := r.do("POST", "/api/my-display/theme", "u-mgr", "manager", url.Values{"theme": {v}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("theme %.20q = %d, want 400", v, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "my_display.") {
			t.Errorf("body shows a raw key: %q", rec.Body.String())
		}
	}
	if _, ok := r.row(t, "u-mgr", common.KeyTheme); ok {
		t.Fatal("a rejected value was written")
	}
}

func TestMyDisplay_BrowsingModeStrictEnum(t *testing.T) {
	r := newMD3440Rig(t)
	if rec := r.do("POST", "/api/my-display/browsing-mode", "u-mgr", "manager", url.Values{"mode": {common.BrowsingModeStripOverflow}}); rec.Code != http.StatusNoContent {
		t.Fatalf("set = %d: %s", rec.Code, rec.Body.String())
	}
	if v, _ := r.row(t, "u-mgr", common.KeyBrowsingMode); v != common.BrowsingModeStripOverflow {
		t.Fatalf("row = %q", v)
	}
	if rec := r.do("POST", "/api/my-display/browsing-mode", "u-mgr", "manager", url.Values{"mode": {"bogus"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus = %d, want 400", rec.Code)
	}
	if v, _ := r.row(t, "u-mgr", common.KeyBrowsingMode); v != common.BrowsingModeStripOverflow {
		t.Fatalf("bogus overwrote the row: %q", v)
	}
	if rec := r.do("POST", "/api/my-display/browsing-mode", "u-mgr", "manager", url.Values{"mode": {""}}); rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d", rec.Code)
	}
	if _, ok := r.row(t, "u-mgr", common.KeyBrowsingMode); ok {
		t.Fatal("row still present after clear")
	}
}

func TestMyDisplay_CashierDirectPost403RowUnchanged(t *testing.T) {
	r := newMD3440Rig(t)
	r.seed(t, "u-cash", common.KeyTheme, "dark")
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/api/my-display/theme", url.Values{"theme": {"amber"}}},
		{"/api/my-display/theme", url.Values{"theme": {""}}},
		{"/api/my-display/browsing-mode", url.Values{"mode": {common.BrowsingModeStripOverflow}}},
	} {
		if rec := r.do("POST", c.path, "u-cash", "cashier", c.form); rec.Code != http.StatusForbidden {
			t.Errorf("%s %v = %d, want 403", c.path, c.form, rec.Code)
		}
	}
	if v, ok := r.row(t, "u-cash", common.KeyTheme); !ok || v != "dark" {
		t.Fatalf("cashier row changed: %q,%v", v, ok)
	}
	if _, ok := r.row(t, "u-cash", common.KeyBrowsingMode); ok {
		t.Fatal("cashier browsing-mode row was written")
	}
}

func TestMyDisplay_IgnoresUserIDField(t *testing.T) {
	r := newMD3440Rig(t)
	rec := r.do("POST", "/api/my-display/theme", "u-mgr", "manager", url.Values{"theme": {"amber"}, "user_id": {"u-cash"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("= %d", rec.Code)
	}
	if _, ok := r.row(t, "u-cash", common.KeyTheme); ok {
		t.Fatal("user_id form field was honoured")
	}
	if v, _ := r.row(t, "u-mgr", common.KeyTheme); v != "amber" {
		t.Fatalf("manager row = %q", v)
	}
}

func TestMyDisplay_NoOperatorSession403(t *testing.T) {
	r := newMD3440Rig(t)
	t.Setenv("UT_AUTH", "off") // canPerform passes, but there is no operator to own a row
	req := httptest.NewRequest("POST", "/api/my-display/theme", strings.NewReader("theme=amber"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("= %d, want 403", rec.Code)
	}
}

func TestMyDisplay_FollowerTill409GetStillRenders(t *testing.T) {
	r := newMD3440Rig(t)
	setReplica(t, r.d)
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/api/my-display/theme", url.Values{"theme": {"amber"}}},
		{"/api/my-display/browsing-mode", url.Values{"mode": {common.BrowsingModeStripOverflow}}},
	} {
		rec := r.do("POST", c.path, "u-mgr", "manager", c.form)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s = %d, want 409", c.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "main till") {
			t.Errorf("%s body %q does not point at the main till", c.path, rec.Body.String())
		}
	}
	if _, ok := r.row(t, "u-mgr", common.KeyTheme); ok {
		t.Fatal("follower wrote a theme row")
	}
	if _, ok := r.row(t, "u-mgr", common.KeyBrowsingMode); ok {
		t.Fatal("follower wrote a mode row")
	}
	rec := r.do("GET", "/my-display", "u-mgr", "manager", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET on follower = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "follows a main till") || !strings.Contains(body, "disabled") {
		t.Errorf("follower page lacks the notice / disabled controls:\n%.1500s", body)
	}
}

func TestMyDisplay_RemovedThemeFallsBackToTillTheme(t *testing.T) {
	r := newMD3440Rig(t)
	r.seed(t, "u-mgr", common.KeyTheme, "gone-plugin")
	req := auth.WithUser(httptest.NewRequest("GET", "/", nil), auth.User{ID: "u-mgr", Role: "manager"})
	if got := r.d.ResolvedTheme(req); got != md3440Till {
		t.Fatalf("ResolvedTheme = %q, want till theme %q", got, md3440Till)
	}
	rec := r.do("GET", "/my-display", "u-mgr", "manager", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "gone-plugin") || !strings.Contains(body, "no longer installed") {
		t.Errorf("page lacks the unavailable-theme line:\n%.2000s", body)
	}
	if !strings.Contains(body, `data-theme="`+md3440Till+`"`) {
		t.Errorf("page not rendered in the till theme")
	}
}

func TestMyDisplay_EndToEnd_ManagerThemeCashierTill(t *testing.T) {
	r := newMD3440Rig(t)
	if rec := r.do("POST", "/api/my-display/theme", "u-mgr", "manager", url.Values{"theme": {"amber"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	r.seed(t, "u-cash", common.KeyTheme, "amber") // hand-seeded: cashier is ungranted
	if body := r.do("GET", "/my-display", "u-mgr", "manager", nil).Body.String(); !strings.Contains(body, `data-theme="amber"`) {
		t.Errorf("manager's page not in amber:\n%.600s", body)
	}
	if body := r.do("GET", "/ui/theme-sync", "u-mgr", "manager", nil).Body.String(); !strings.Contains(body, "amber") {
		t.Errorf("manager theme-sync = %q, want amber", body)
	}
	body := r.do("GET", "/ui/theme-sync", "u-cash", "cashier", nil).Body.String()
	if !strings.Contains(body, md3440Till) || strings.Contains(body, "amber") {
		t.Errorf("cashier theme-sync = %q, want the till theme only", body)
	}
}

func TestMyDisplay_CustomRoleGrantEnablesPage(t *testing.T) {
	r := newMD3440Rig(t)
	if _, err := r.d.Db.Exec(`INSERT INTO roles(role) VALUES ('supervisor')`); err != nil {
		t.Fatalf("create custom role: %v", err)
	}
	if _, err := r.d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('u-sup','u-sup','u-sup','x','supervisor')`); err != nil {
		t.Fatalf("seed custom-role user: %v", err)
	}
	if rec := r.do("GET", "/my-display", "u-sup", "supervisor", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("ungranted custom role = %d, want 403", rec.Code)
	}
	if _, err := r.d.Db.Exec(`INSERT INTO role_permissions(role,action,granted) VALUES ('supervisor','personal_display',1)`); err != nil {
		t.Fatal(err)
	}
	if rec := r.do("GET", "/my-display", "u-sup", "supervisor", nil); rec.Code != http.StatusOK {
		t.Fatalf("granted custom role = %d: %.300s", rec.Code, rec.Body.String())
	}
}

func TestMyDisplay_ActionConstMatchesCommon(t *testing.T) {
	if personalDisplayAction != common.PersonalDisplayAction {
		t.Fatalf("pages gate %q != common resolver action %q", personalDisplayAction, common.PersonalDisplayAction)
	}
}
