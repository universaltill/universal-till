package pages

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
)

// The main till's check-in report of roles and permission actions
// (ADR-0128 §5, ut-docs#3323), driven through the hooks exactly as
// StartCloudSync wires them: DeviceExtra builds the report, AfterTick
// tells the gate whether the check-in carrying it got through.

// rolesOf returns DeviceExtra's roles and permission_actions, and whether
// each key was present.
func rolesOf(extra map[string]any) ([]cloudRoleReport, bool, []string, bool) {
	roles, rok := extra["roles"].([]cloudRoleReport)
	actions, aok := extra["permission_actions"].([]string)
	return roles, rok, actions, aok
}

func TestCloudRolesReport_Shape(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	if _, err := e.save(t, "d1", roleKeyA, "Shift lead", []string{"void", "refund"}, true); err != nil {
		t.Fatal(err)
	}
	wireCloudLinkHooks(e.dp, &e.hooks)
	extra := e.hooks.DeviceExtra(t.Context())
	roles, rok, actions, aok := rolesOf(extra)
	if !rok || !aok {
		t.Fatalf("roles/permission_actions missing: %#v %#v", extra["roles"], extra["permission_actions"])
	}
	if !sort.StringsAreSorted(actions) || len(actions) < 5 {
		t.Fatalf("permission_actions = %v", actions)
	}
	byRole := map[string]cloudRoleReport{}
	for _, r := range roles {
		byRole[r.Role] = r
		if r.Grants == nil || !sort.StringsAreSorted(r.Grants) {
			t.Fatalf("grants of %s = %#v", r.Role, r.Grants)
		}
	}
	// Every roles row is included, built-in ones too.
	for _, k := range []string{"cashier", "manager", "admin", "super_admin", roleKeyA} {
		if _, ok := byRole[k]; !ok {
			t.Fatalf("role %s not reported: %+v", k, roles)
		}
	}
	// Built-in: the label in the till's UI language, never the raw key.
	sa := byRole["super_admin"]
	if sa.Origin != "builtin" || sa.Label != httpx.T(httpx.DefaultLocale(), "users.role.super_admin") || sa.Label != "super admin" {
		t.Fatalf("super_admin = %+v", sa)
	}
	// Cloud: the label as stored, with its grants.
	if c := byRole[roleKeyA]; c.Origin != "cloud" || c.Label != "Shift lead" || !reflect.DeepEqual(c.Grants, []string{"refund", "void"}) {
		t.Fatalf("cloud role = %+v", c)
	}
	raw, err := json.Marshal(map[string]any{"roles": extra["roles"], "permission_actions": extra["permission_actions"]})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"role":`, `"label":`, `"origin":`, `"grants":`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("report lacks %s: %s", key, raw)
		}
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("report carries null: %s", raw)
	}
}

// A satellite never sends either key.
func TestCloudRolesReport_SatelliteSendsNothing(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	setReplica(t, e.dp)
	wireCloudLinkHooks(e.dp, &e.hooks)
	extra := e.hooks.DeviceExtra(t.Context())
	if _, ok := extra["roles"]; ok {
		t.Fatal("a satellite reported roles")
	}
	if _, ok := extra["permission_actions"]; ok {
		t.Fatal("a satellite reported permission_actions")
	}
}

// After a successful check-in, an unchanged admin generation sends
// nothing; an admin change sends again. Wired through wireCloudLinkHooks,
// so the cloud link's AfterTick must not replace the gate's.
func TestCloudRolesReport_SentOnlyWhenGenerationMoves(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	wireCloudLinkHooks(e.dp, &e.hooks)
	ctx := t.Context()
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
		t.Fatal("first check-in after start did not send")
	}
	e.hooks.AfterTick(ctx, true, nil)
	if _, rok, _, aok := rolesOf(e.hooks.DeviceExtra(ctx)); rok || aok {
		t.Fatal("unchanged generation re-sent the report")
	}
	e.hooks.AfterTick(ctx, true, nil)
	if err := data.NewAuthRepo(e.dp.Db).SetRolePermission(ctx, nil, "cashier", "refund", true); err != nil {
		t.Fatal(err)
	}
	roles, rok, _, aok := rolesOf(e.hooks.DeviceExtra(ctx))
	if !rok || !aok {
		t.Fatal("an admin change did not re-send the report")
	}
	for _, r := range roles {
		if r.Role == "cashier" && !slices.Contains(r.Grants, "refund") {
			t.Fatalf("cashier grants = %v, want refund", r.Grants)
		}
	}
	e.hooks.AfterTick(ctx, true, nil)
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); rok {
		t.Fatal("re-sent after the change was reported")
	}
}

// A check-in that did not get through re-sends the report next time; only
// contacted && err == nil commits it.
func TestCloudRolesReport_FailedCheckinResends(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	wireCloudLinkHooks(e.dp, &e.hooks)
	ctx := t.Context()
	for i, outcome := range []struct {
		contacted bool
		err       error
	}{{false, errors.New("sync POST: 503")}, {false, nil}, {true, errors.New("odd")}} {
		if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
			t.Fatalf("attempt %d: report not sent after a failed check-in", i)
		}
		e.hooks.AfterTick(ctx, outcome.contacted, outcome.err)
	}
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
		t.Fatal("report not sent after a failed check-in")
	}
	e.hooks.AfterTick(ctx, true, nil)
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); rok {
		t.Fatal("re-sent after a successful check-in")
	}
}

// A check-in that never built a report (an unregistered till's tick skips
// DeviceExtra) commits nothing, even when it reports success.
func TestCloudRolesReport_TickWithoutReportCommitsNothing(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	wireCloudLinkHooks(e.dp, &e.hooks)
	ctx := t.Context()
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
		t.Fatal("first check-in did not send")
	}
	e.hooks.AfterTick(ctx, false, errors.New("down"))
	e.hooks.AfterTick(ctx, true, nil) // no DeviceExtra in between
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
		t.Fatal("a tick that carried no report committed the failed one")
	}
}

// No generation counter row: the report goes on every check-in.
func TestCloudRolesReport_UntrackedGenerationAlwaysSends(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	wireCloudLinkHooks(e.dp, &e.hooks)
	ctx := t.Context()
	if _, err := e.dp.Db.ExecContext(ctx, `DELETE FROM sync_admin_version`); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
			t.Fatalf("check-in %d: untracked generation did not send", i)
		}
		e.hooks.AfterTick(ctx, true, nil)
	}
}

// A read error leaves both keys out, logs a warning and never fails the
// rest of the heartbeat; the next readable check-in sends.
func TestCloudRolesReport_ReadErrorLeavesKeysOut(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	wireCloudLinkHooks(e.dp, &e.hooks)
	ctx := t.Context()
	logs := &lockedBuf{}
	t.Cleanup(logging.CaptureForTest(logs))
	if _, err := e.dp.Db.ExecContext(ctx, `ALTER TABLE role_permissions RENAME TO role_permissions_gone`); err != nil {
		t.Fatal(err)
	}
	extra := e.hooks.DeviceExtra(ctx)
	if _, ok := extra["roles"]; ok {
		t.Fatal("roles sent on a read error")
	}
	if _, ok := extra["permission_actions"]; ok {
		t.Fatal("permission_actions sent on a read error")
	}
	if _, ok := extra["theme"]; !ok {
		t.Fatal("the rest of the heartbeat was dropped")
	}
	if !strings.Contains(logs.String(), "roles report") {
		t.Fatalf("no warning logged: %s", logs.String())
	}
	e.hooks.AfterTick(ctx, true, nil)
	if _, err := e.dp.Db.ExecContext(ctx, `ALTER TABLE role_permissions_gone RENAME TO role_permissions`); err != nil {
		t.Fatal(err)
	}
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
		t.Fatal("not sent once the read worked again")
	}
}

// A report over rolesReportByteBudget leaves both keys out, so the cloud's
// 4 MiB sync limit can never refuse every heartbeat (review of #3323), and
// it is not retried until the admin generation moves.
func TestCloudRolesReport_OverBudgetLeavesKeysOut(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	wireCloudLinkHooks(e.dp, &e.hooks)
	old := rolesReportByteBudget
	rolesReportByteBudget = 64
	t.Cleanup(func() { rolesReportByteBudget = old })
	ctx := t.Context()
	extra := e.hooks.DeviceExtra(ctx)
	if _, rok, _, aok := rolesOf(extra); rok || aok {
		t.Fatal("an over-budget report was sent")
	}
	if _, ok := extra["theme"]; !ok {
		t.Fatal("the over-budget report dropped the rest of the heartbeat")
	}
	e.hooks.AfterTick(ctx, true, nil)
	rolesReportByteBudget = old
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); rok {
		t.Fatal("an over-budget report was retried with no admin change")
	}
	e.hooks.AfterTick(ctx, true, nil)
	if err := data.NewAuthRepo(e.dp.Db).SetRolePermission(ctx, nil, "cashier", "refund", true); err != nil {
		t.Fatal(err)
	}
	if _, rok, _, _ := rolesOf(e.hooks.DeviceExtra(ctx)); !rok {
		t.Fatal("an admin change after an over-budget report did not send")
	}
}

// Main → satellite → main: reset makes the till send again at its first
// check-in as main, even at an unchanged admin generation. Driven on the
// gate itself: the sync.primary_url write that demotes or promotes a till
// moves the generation on its own, which would hide a missing reset.
func TestCloudRolesReport_ResetResends(t *testing.T) {
	e := newRoleDirectiveEnv(t)
	ctx := t.Context()
	g := &rolesReportGate{}
	sent := func() bool {
		extra := map[string]any{}
		g.add(ctx, e.dp, extra)
		_, ok := extra["roles"]
		return ok
	}
	if !sent() {
		t.Fatal("first check-in did not send")
	}
	g.commit(ctx, true, nil)
	if sent() {
		t.Fatal("unchanged generation re-sent")
	}
	g.commit(ctx, true, nil)
	g.reset()
	if !sent() {
		t.Fatal("after reset (satellite spell) the report was not re-sent")
	}
}

// A built-in role with no users.role.* key reports its stored label, never
// the raw i18n key.
func TestBuiltinRoleLabel_MissingKeyFallsBack(t *testing.T) {
	if got := builtinRoleLabel(httpx.DefaultLocale(), "super_admin", ""); got != "super admin" {
		t.Fatalf("super_admin label = %q", got)
	}
	if got := builtinRoleLabel(httpx.DefaultLocale(), "no_such_role_3323", "Stored"); got != "Stored" {
		t.Fatalf("missing-key label = %q, want the stored label", got)
	}
}
