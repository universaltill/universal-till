package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// Custom roles on the till's own pages (ADR-0128 §3 "Assigning a custom
// role to a user", §6 "Permissions page", ut-docs#3324): a role with
// origin='cloud' shows as a read-only column under its label on the
// Permissions page, is listed by label on the Users page, and can be
// assigned to a user (by an admin) on every path that sets a role: the
// users page, the main till's write-through endpoint and the cloud
// save_user directive. An unknown c_ key is refused everywhere.

// insertCloudRole adds an origin='cloud' role with one granted action.
func insertCloudRole(t *testing.T, db *sql.DB, key, label string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO roles (role, label, origin) VALUES (?, ?, 'cloud')`, key, label); err != nil {
		t.Fatalf("insert cloud role: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, 'refund', 1)`, key); err != nil {
		t.Fatalf("grant cloud role: %v", err)
	}
}

func TestPermissionSettingsPage_GET_CustomRoleColumnReadOnly(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	insertCloudRole(t, dp.Db, roleKeyA, "Shift <lead>")

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The label, escaped, heads the column; the key itself and an
	// untranslated users.role.c_… fallback never show as text.
	if !strings.Contains(body, "Shift &lt;lead&gt;") {
		t.Fatalf("custom role label missing from the header: %s", body)
	}
	if strings.Contains(body, "users.role."+roleKeyA) {
		t.Fatalf("custom role rendered through the built-in label key: %s", body)
	}
	managed := httpx.T("en", "permissions.cloud_role_managed")
	if managed == "permissions.cloud_role_managed" || !strings.Contains(body, managed) {
		t.Fatalf("missing the translated managed-in-my. note %q", managed)
	}

	// Built-in columns first, in today's order; the custom one after them.
	head := body[strings.Index(body, "<thead>"):strings.Index(body, "</thead>")]
	order := []string{httpx.T("en", "users.role.admin"), httpx.T("en", "users.role.cashier"), httpx.T("en", "users.role.manager"), httpx.T("en", "users.role.super_admin"), "Shift &lt;lead&gt;"}
	at := 0
	for _, label := range order {
		i := strings.Index(head[at:], label)
		if i < 0 {
			t.Fatalf("column %q missing or out of order in %s", label, head)
		}
		at += i + len(label)
	}

	// Its cells are disabled and never post; built-in cells still do.
	if strings.Contains(body, `&#34;role&#34;:&#34;`+roleKeyA) || strings.Contains(body, `"role":"`+roleKeyA) {
		t.Fatalf("a custom role cell is still wired to hx-post: %s", body)
	}
	cells := regexp.MustCompile(`<input type="checkbox"[^>]*data-cloud-role[^>]*>`).FindAllString(body, -1)
	if len(cells) == 0 {
		t.Fatalf("no read-only custom role cells rendered")
	}
	checked := 0
	for _, c := range cells {
		if !strings.Contains(c, "disabled") {
			t.Fatalf("custom role cell not disabled: %s", c)
		}
		if strings.Contains(c, "checked") {
			checked++
		}
	}
	if checked != 1 {
		t.Fatalf("custom role shows %d granted cells, want 1 (refund)", checked)
	}
	if !strings.Contains(body, `&#34;role&#34;:&#34;cashier&#34;`) && !strings.Contains(body, `"role":"cashier"`) {
		t.Fatalf("built-in cashier cells lost their hx-post wiring: %s", body)
	}
}

// fixUsersFixture gives seedForPages' NULL-display_name rows a name, so
// GET /users (AuthRepo.ListUsers) can scan the whole table.
func fixUsersFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE users SET display_name = username WHERE display_name IS NULL`); err != nil {
		t.Fatal(err)
	}
}

func getUsersPage(t *testing.T, mux *http.ServeMux, actor auth.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/users", nil), actor))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /users = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestUsersPage_GET_CustomRoleShownByLabelAndOfferedToAdmins(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	fixUsersFixture(t, dp.Db)
	insertCloudRole(t, dp.Db, roleKeyA, "Shift lead")
	insertTestUser(t, dp.Db, "admin-1", "admin-1", "Admin One", "admin")
	insertTestUser(t, dp.Db, "lead-1", "lead-1", "Lead One", roleKeyA)
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")

	body := getUsersPage(t, mux, auth.User{ID: "admin-1", Role: "admin"})
	if strings.Contains(body, ">"+roleKeyA+"<") {
		t.Fatalf("the users list shows the raw role key: %s", body)
	}
	if !regexp.MustCompile(`<td[^>]*>Shift lead</td>`).MatchString(body) {
		t.Fatalf("the users list doesn't show the custom role's label: %s", body)
	}
	if !regexp.MustCompile(`<td[^>]*>` + regexp.QuoteMeta(httpx.T("en", "users.role.cashier")) + `</td>`).MatchString(body) {
		t.Fatalf("built-in roles lost their translated label in the list: %s", body)
	}
	opt := `<option value="` + roleKeyA + `"`
	if n := strings.Count(body, opt); n < 2 { // new-user form + at least one row picker
		t.Fatalf("admin sees the custom role in %d role pickers, want the new-user form and the row pickers: %s", n, body)
	}
	if !regexp.MustCompile(`<option value="` + roleKeyA + `" selected>Shift lead</option>`).MatchString(body) {
		t.Fatalf("lead-1's row picker doesn't preselect its custom role: %s", body)
	}

	// A manager can only move cashiers to cashier: no custom role offered.
	body = getUsersPage(t, mux, auth.User{ID: "mgr-1", Role: "manager"})
	if strings.Contains(body, opt) {
		t.Fatalf("a manager is offered the custom role: %s", body)
	}
}

func TestUsersPage_ChangeRole_CustomRole(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertCloudRole(t, dp.Db, roleKeyA, "Shift lead")
	insertTestUser(t, dp.Db, "admin-1", "admin-1", "Admin One", "admin")
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")
	admin := &auth.User{ID: "admin-1", Role: "admin"}

	// A manager can't give a cashier a custom role (assignment needs an
	// admin, like manager/admin).
	if rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {roleKeyA}}, &auth.User{ID: "mgr-1", Role: "manager"}); rec.Code != http.StatusForbidden {
		t.Fatalf("manager assigning a custom role = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// An unknown c_ key is refused like any unknown role.
	rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {roleKeyB}}, admin)
	if rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.role")) {
		t.Fatalf("unknown custom role = %d %q: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if role, _ := userRole(t, dp.Db, "cash-1"); role != "cashier" {
		t.Fatalf("refused change wrote role %q", role)
	}

	rec = postForm(mux, "/api/users/cash-1/role", url.Values{"role": {roleKeyA}}, admin)
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "ok" {
		t.Fatalf("admin assigning a custom role = %d %q: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if role, _ := userRole(t, dp.Db, "cash-1"); role != roleKeyA {
		t.Fatalf("role after assign = %q, want %s", role, roleKeyA)
	}

	// A user holding a custom role is managed by admins only: a manager
	// can't set them back to cashier.
	if rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {"cashier"}}, &auth.User{ID: "mgr-1", Role: "manager"}); rec.Code != http.StatusForbidden {
		t.Fatalf("manager changing a custom-role holder = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersPage_CreateUser_CustomRole(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertCloudRole(t, dp.Db, roleKeyA, "Shift lead")
	insertTestUser(t, dp.Db, "admin-1", "admin-1", "Admin One", "admin")

	form := url.Values{"username": {"lena"}, "display_name": {"Lena"}, "role": {roleKeyA}}
	if rec := postForm(mux, "/api/users", form, &auth.User{ID: "mgr-1", Role: "manager"}); rec.Code != http.StatusForbidden {
		t.Fatalf("manager creating a custom-role user = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	bad := url.Values{"username": {"lena"}, "display_name": {"Lena"}, "role": {roleKeyB}}
	if rec := postForm(mux, "/api/users", bad, &auth.User{ID: "admin-1", Role: "admin"}); rec.Header().Get("X-UT-Response") != "refused" {
		t.Fatalf("unknown custom role create = %d %q: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if rec := postForm(mux, "/api/users", form, &auth.User{ID: "admin-1", Role: "admin"}); rec.Header().Get("X-UT-Response") != "ok" {
		t.Fatalf("admin creating a custom-role user = %d %q: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "lena"); !ok || role != roleKeyA {
		t.Fatalf("created role = %q ok=%v", role, ok)
	}
}

func TestSyncUsersApply_CustomRole(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	insertCloudRole(t, dp.Db, roleKeyA, "Shift lead")
	pin, _ := auth.HashPIN("2468")
	insertTestUserWithPIN(t, dp.Db, "m-1", "mia", "Mia", "manager", pin)
	insertTestUserWithPIN(t, dp.Db, "c-1", "cara", "Cara", "cashier", pin)

	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "create", Username: "x", DisplayName: "X", Role: roleKeyB, ActorID: "adm-1"}), syncUsersBearer),
		http.StatusBadRequest, "invalid_role")
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "c-1", Role: roleKeyB, ActorID: "adm-1"}), syncUsersBearer),
		http.StatusBadRequest, "invalid_role")
	if rec := postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "c-1", Role: roleKeyA, ActorID: "m-1"}), syncUsersBearer); rec.Code != http.StatusForbidden {
		t.Fatalf("manager assigning a custom role via the main till = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	rec := postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "create", Username: "noor", DisplayName: "Noor", Role: roleKeyA, ActorID: "adm-1"}), syncUsersBearer)
	if out := decodeSyncUserApply(t, rec); rec.Code != http.StatusOK || out.Data == nil || out.Data.Role != roleKeyA {
		t.Fatalf("admin create with custom role = %d: %s", rec.Code, rec.Body.String())
	}
	rec = postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "c-1", Role: roleKeyA, ActorID: "adm-1"}), syncUsersBearer)
	if out := decodeSyncUserApply(t, rec); rec.Code != http.StatusOK || out.Data == nil || out.Data.Role != roleKeyA {
		t.Fatalf("admin set_role to custom role = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCloudUserDirective_CustomRole(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, "u-admin", "boss", "admin", "1111", true)
	e.addUser(t, "u-cash", "cash", "cashier", "3333", true)
	insertCloudRole(t, e.dp.Db, roleKeyA, "Shift lead")

	if _, err := e.hooks.SaveUser(ctx, cloudsync.UserDirective{DirectiveID: "x1", Type: "save_user", UserID: "u-cash", Role: sp(roleKeyB)}); err == nil || err.Error() != "The role "+roleKeyB+" can't be set from the cloud." {
		t.Fatalf("unknown custom role: err = %v", err)
	}
	if _, err := e.hooks.SaveUser(ctx, cloudsync.UserDirective{DirectiveID: "x2", Type: "save_user", UserID: "u-cash", Role: sp(roleKeyA)}); err != nil {
		t.Fatalf("existing custom role: %v", err)
	}
	if u := e.user(t, "u-cash"); u.Role != roleKeyA {
		t.Fatalf("role after directive = %q", u.Role)
	}
}

// The assignable-role rule itself (ADR-0128 §3): built-in roles by name,
// custom roles only when an origin='cloud' row exists; a built-in-looking
// row never makes an arbitrary name assignable; the cloud never sets
// super_admin.
func TestAssignableUserRole(t *testing.T) {
	_, dp := newPermissionSettingsTestDeps(t)
	insertCloudRole(t, dp.Db, roleKeyA, "Shift lead")
	if _, err := dp.Db.Exec(`INSERT INTO roles (role, label, origin) VALUES ('owner', '', 'builtin')`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewAuthRepo(dp.Db)
	for role, want := range map[string][2]bool{ // {till, cloud}
		"cashier":     {true, true},
		"manager":     {true, true},
		"admin":       {true, true},
		"super_admin": {true, false},
		roleKeyA:      {true, true},
		roleKeyB:      {false, false},
		"owner":       {false, false},
		"":            {false, false},
	} {
		got, err := isAssignableUserRole(t.Context(), repo, role)
		if err != nil || got != want[0] {
			t.Errorf("isAssignableUserRole(%q) = %v, %v; want %v", role, got, err, want[0])
		}
		got, err = cloudAssignableUserRole(t.Context(), repo, role)
		if err != nil || got != want[1] {
			t.Errorf("cloudAssignableUserRole(%q) = %v, %v; want %v", role, got, err, want[1])
		}
	}
}

// ADR-0128 §6: name-based checks keep matching built-in names only. A
// custom-role actor holding user_management manages cashiers only; a
// custom-role target is managed by admin/super_admin only.
func TestCanManageUser_CustomRoles(t *testing.T) {
	cashier := data.UserRow{ID: "c", Role: "cashier"}
	lead := data.UserRow{ID: "l", Role: roleKeyA}
	for _, c := range []struct {
		actor  string
		target data.UserRow
		want   bool
	}{
		{roleKeyA, cashier, true},
		{roleKeyA, lead, false},
		{roleKeyA, data.UserRow{ID: "m", Role: "manager"}, false},
		{"manager", lead, false},
		{"admin", lead, true},
		{"super_admin", lead, true},
	} {
		if got := canManageUser(c.actor, c.target); got != c.want {
			t.Errorf("canManageUser(%s, %s) = %v, want %v", c.actor, c.target.Role, got, c.want)
		}
	}
	if (auth.User{Role: roleKeyA}).IsManager() {
		t.Fatal("a custom role must get nothing by name: IsManager() true")
	}
}

// ut-docs#3190: the users list's role column shows the translated role
// name in a non-English locale, never the raw role id.
func TestUsersPage_GET_RoleColumnTranslated(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	fixUsersFixture(t, dp.Db)
	insertTestUser(t, dp.Db, "admin-1", "admin-1", "Admin One", "admin")
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/users?lang=fa", nil), auth.User{ID: "admin-1", Role: "admin"}))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /users?lang=fa = %d: %s", rec.Code, body)
	}
	fa := httpx.T("fa", "users.role.cashier")
	if fa == "users.role.cashier" || fa == httpx.T("en", "users.role.cashier") {
		t.Fatalf("fa has no own users.role.cashier label: %q", fa)
	}
	if !regexp.MustCompile(`<td[^>]*>` + regexp.QuoteMeta(fa) + `</td>`).MatchString(body) {
		t.Fatalf("role column doesn't show the Persian cashier label %q", fa)
	}
	for _, raw := range []string{"cashier", "admin"} {
		if regexp.MustCompile(`<td[^>]*>` + raw + `</td>`).MatchString(body) {
			t.Fatalf("role column shows the raw role id %q", raw)
		}
	}
}
