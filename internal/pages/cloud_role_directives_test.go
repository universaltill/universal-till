package pages

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Custom role directives, main-till apply (ADR-0128 §2/§3, ut-docs#3165),
// driven through buildCloudHooks exactly as Tick does.

const (
	roleKeyA = "c_01j9z3k4m5n6p7q8r9s0t1v2w3"
	roleKeyB = "c_01j9z3k4m5n6p7q8r9s0t1v2w4"
)

type roleDirectiveEnv struct {
	dp    *common.Deps
	hooks cloudsync.Hooks
}

func newRoleDirectiveEnv(t *testing.T) *roleDirectiveEnv {
	t.Helper()
	dp := newCloudSyncTestDeps(t) // chdirs to the repo root
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	return &roleDirectiveEnv{dp: dp, hooks: buildCloudHooks(dp, nil)}
}

func (e *roleDirectiveEnv) save(t *testing.T, did, role, label string, grants []string, create bool) (string, error) {
	t.Helper()
	return e.hooks.SaveRole(t.Context(), cloudsync.RoleDirective{DirectiveID: did, Type: "save_role", CreatedBy: "owner-1", Role: role, Label: label, Grants: grants, Create: create})
}

func (e *roleDirectiveEnv) del(t *testing.T, did, role string) (string, error) {
	t.Helper()
	return e.hooks.DeleteRole(t.Context(), cloudsync.RoleDirective{DirectiveID: did, Type: "delete_role", CreatedBy: "owner-1", Role: role})
}

// state returns (exists, label, origin, granted actions) for role.
func (e *roleDirectiveEnv) state(t *testing.T, role string) (bool, string, string, []string) {
	t.Helper()
	ctx := t.Context()
	repo := data.NewAuthRepo(e.dp.Db)
	tx, err := e.dp.Db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ri, ok, err := repo.GetRoleTx(ctx, tx, role)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := repo.RoleGrantsTx(ctx, tx, role)
	if err != nil {
		t.Fatal(err)
	}
	return ok, ri.Label, ri.Origin, grants
}

type roleAudit struct {
	Action  string
	Payload map[string]any
}

func (e *roleDirectiveEnv) audits(t *testing.T, role string) []roleAudit {
	t.Helper()
	rows, err := e.dp.Db.Query(`SELECT action, COALESCE(data_json,'') FROM audit_log WHERE entity_type = 'role' AND entity_id = ? AND actor_id = 'system' ORDER BY rowid`, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []roleAudit
	for rows.Next() {
		var a roleAudit
		var raw string
		if err := rows.Scan(&a.Action, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &a.Payload); err != nil {
			t.Fatalf("audit payload %q: %v", raw, err)
		}
		out = append(out, a)
	}
	return out
}

func strs(v any) []string {
	arr, _ := v.([]any)
	out := []string{}
	for _, x := range arr {
		out = append(out, x.(string))
	}
	return out
}

func TestCloudRoleDirective_CreateReapplyReplace(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	if _, err := e.save(t, "d1", roleKeyA, "Shift lead", []string{"refund", "audit", "refund"}, true); err != nil {
		t.Fatalf("create: %v", err)
	}
	ok, label, origin, grants := e.state(t, roleKeyA)
	if !ok || label != "Shift lead" || origin != "cloud" || !reflect.DeepEqual(grants, []string{"audit", "refund"}) {
		t.Fatalf("after create: %v %q %q %v", ok, label, origin, grants)
	}
	if can, _ := data.NewAuthRepo(e.dp.Db).HasPermission(t.Context(), roleKeyA, "refund"); !can {
		t.Fatal("custom role grant not enforced")
	}
	// Tick repeats the same directive after a lost result post: applied,
	// same state.
	if _, err := e.save(t, "d1", roleKeyA, "Shift lead", []string{"refund", "audit"}, true); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if _, _, _, g := e.state(t, roleKeyA); !reflect.DeepEqual(g, []string{"audit", "refund"}) {
		t.Fatalf("re-apply changed grants: %v", g)
	}
	// Replace: rename and a complete new set.
	if _, err := e.save(t, "d2", roleKeyA, "Floor lead", []string{"void"}, false); err != nil {
		t.Fatalf("replace: %v", err)
	}
	ok, label, _, grants = e.state(t, roleKeyA)
	if !ok || label != "Floor lead" || !reflect.DeepEqual(grants, []string{"void"}) {
		t.Fatalf("after replace: %q %v", label, grants)
	}
	a := e.audits(t, roleKeyA)
	if len(a) < 2 {
		t.Fatalf("audit rows %+v", a)
	}
	first, last := a[0], a[len(a)-1]
	if first.Action != "cloud_role_created" || first.Payload["via"] != "cloud" || first.Payload["actor"] != "owner-1" ||
		first.Payload["directive_id"] != "d1" || first.Payload["label"] != "Shift lead" ||
		len(strs(first.Payload["grants_before"])) != 0 || !reflect.DeepEqual(strs(first.Payload["grants_after"]), []string{"audit", "refund"}) {
		t.Fatalf("create audit %+v", first)
	}
	if last.Action != "cloud_role_saved" || last.Payload["directive_id"] != "d2" || last.Payload["label"] != "Floor lead" ||
		!reflect.DeepEqual(strs(last.Payload["grants_before"]), []string{"audit", "refund"}) || !reflect.DeepEqual(strs(last.Payload["grants_after"]), []string{"void"}) {
		t.Fatalf("replace audit %+v", last)
	}
}

func TestCloudRoleDirective_Refusals(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	if _, err := e.save(t, "seed", roleKeyB, "Barista", []string{"refund"}, true); err != nil {
		t.Fatal(err)
	}
	const builtIn = "Built-in roles are changed on the till."
	for name, c := range map[string]struct {
		role, label string
		grants      []string
		create      bool
		want        string
	}{
		"builtin key":                {"manager", "Boss", nil, true, builtIn},
		"bad key upper":              {"c_01J9Z3K4M5N6P7Q8R9S0T1V2W3", "X", nil, true, "The role key c_01J9Z3K4M5N6P7Q8R9S0T1V2W3 is not a custom role key."},
		"bad key short":              {"c_123", "X", nil, true, "The role key c_123 is not a custom role key."},
		"bad key letter u":           {"c_01j9z3k4m5n6p7q8r9s0t1v2wu", "X", nil, true, "The role key c_01j9z3k4m5n6p7q8r9s0t1v2wu is not a custom role key."},
		"no key prefix":              {"shift_lead", "X", nil, true, "The role key shift_lead is not a custom role key."},
		"empty label":                {roleKeyA, "  ", nil, true, "The role name must be 1 to 40 characters."},
		"long label":                 {roleKeyA, strings.Repeat("é", 41), nil, true, "The role name must be 1 to 40 characters."},
		"control char":               {roleKeyA, "Lead\u0007", nil, true, "The role name can't contain control characters."},
		"zero-width space":           {roleKeyA, "Admin\u200b", nil, true, "The role name can't contain control characters."},
		"rtl override":               {roleKeyA, "\u202enimdA", nil, true, "The role name can't contain control characters."},
		"bom":                        {roleKeyA, "\ufeffAdmin", nil, true, "The role name can't contain control characters."},
		"builtin key label":          {roleKeyA, "ADMIN", nil, true, "A role called ADMIN already exists."},
		"builtin key label super":    {roleKeyA, "Super_Admin", nil, true, "A role called Super_Admin already exists."},
		"builtin english label":      {roleKeyA, "Super Admin", nil, true, "A role called Super Admin already exists."},
		"builtin turkish label":      {roleKeyA, httpx.T("tr", "users.role.manager"), nil, true, "A role called " + httpx.T("tr", "users.role.manager") + " already exists."},
		"other custom label ci":      {roleKeyA, "BARISTA", nil, true, "A role called BARISTA already exists."},
		"unknown action":             {roleKeyA, "Lead", []string{"refund", "teleport"}, true, "Action teleport isn't on this till. Reload the roles page and save again."},
		"permission management":      {roleKeyA, "Lead", []string{"refund", "permission_management"}, true, "Permission management can't be given to a custom role."},
		"update missing":             {roleKeyA, "Lead", []string{"refund"}, false, "Role " + roleKeyA + " does not exist on the main till."},
		"permission management only": {roleKeyB, "Barista", []string{"permission_management"}, false, "Permission management can't be given to a custom role."},
	} {
		_, err := e.save(t, "x-"+name, c.role, c.label, c.grants, c.create)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if ok, _, _, _ := e.state(t, roleKeyA); ok {
		t.Fatal("a refused save_role created the role")
	}
	if _, label, _, g := e.state(t, roleKeyB); label != "Barista" || !reflect.DeepEqual(g, []string{"refund"}) {
		t.Fatalf("a refused save_role changed the other role: %q %v", label, g)
	}
	if _, _, origin, _ := e.state(t, "manager"); origin != "builtin" {
		t.Fatalf("manager origin %q", origin)
	}
	// Re-saving a role under its OWN label (any case) is not a collision.
	if _, err := e.save(t, "own", roleKeyB, "barista", []string{"refund"}, false); err != nil {
		t.Fatalf("own label: %v", err)
	}
}

func TestCloudRoleDirective_Delete(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	ctx := t.Context()
	if _, err := e.save(t, "d1", roleKeyA, "Shift lead", []string{"refund", "audit"}, true); err != nil {
		t.Fatal(err)
	}
	// Built-in key refused.
	if _, err := e.del(t, "b", "cashier"); err == nil || err.Error() != "Built-in roles are changed on the till." {
		t.Fatalf("builtin delete: %v", err)
	}
	// Held by one active and one deactivated user: refused, no names.
	ue := &userDirectiveEnv{dp: e.dp}
	ue.addUser(t, "u-1", "anna", roleKeyA, "", true)
	ue.addUser(t, "u-2", "ben", roleKeyA, "", false)
	_, err := e.del(t, "h", roleKeyA)
	if err == nil || err.Error() != "Shift lead is still assigned to 2 people. Give them another role first." {
		t.Fatalf("held delete: %v", err)
	}
	if ok, _, _, g := e.state(t, roleKeyA); !ok || len(g) != 2 {
		t.Fatal("a refused delete changed the role")
	}
	if _, err := e.dp.Db.ExecContext(ctx, `UPDATE users SET role = 'cashier' WHERE id IN ('u-1','u-2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.del(t, "ok", roleKeyA); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _, _, _ := e.state(t, roleKeyA); ok {
		t.Fatal("role not deleted")
	}
	var n int
	if err := e.dp.Db.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE role = ?`, roleKeyA).Scan(&n); err != nil || n != 0 {
		t.Fatalf("grant rows left: %d %v", n, err)
	}
	a := e.audits(t, roleKeyA)
	last := a[len(a)-1]
	if last.Action != "cloud_role_deleted" || last.Payload["label"] != "Shift lead" || last.Payload["directive_id"] != "ok" ||
		!reflect.DeepEqual(strs(last.Payload["grants_before"]), []string{"audit", "refund"}) || len(strs(last.Payload["grants_after"])) != 0 {
		t.Fatalf("delete audit %+v", last)
	}
	// Already gone: applied (idempotent), no new audit row.
	msg, err := e.del(t, "ok", roleKeyA)
	if err != nil || !strings.Contains(msg, "already deleted") {
		t.Fatalf("re-delete: %q %v", msg, err)
	}
	if len(e.audits(t, roleKeyA)) != len(a) {
		t.Fatal("an idempotent re-delete wrote an audit row")
	}
	// A malformed key is refused.
	if _, err := e.del(t, "bad", "admin2"); err == nil {
		t.Fatal("bad key delete accepted")
	}
}

// A satellite till refuses both hooks (Tick already skips the types; this
// is the second line).
func TestCloudRoleDirective_SatelliteRefuses(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	setReplica(t, e.dp)
	hooks := buildCloudHooks(e.dp, nil)
	if _, err := hooks.SaveRole(context.Background(), cloudsync.RoleDirective{DirectiveID: "s", Type: "save_role", Role: roleKeyA, Label: "Lead", Grants: []string{}, Create: true}); err == nil {
		t.Fatal("a satellite applied save_role")
	}
	if ok, _, _, _ := e.state(t, roleKeyA); ok {
		t.Fatal("a satellite created a role")
	}
	if _, err := hooks.DeleteRole(context.Background(), cloudsync.RoleDirective{DirectiveID: "s2", Type: "delete_role", Role: roleKeyA}); err == nil {
		t.Fatal("a satellite applied delete_role")
	}
}
