package cloudsync

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Custom role directives (ADR-0128 §1/§3): save_role and delete_role are
// main-till only, decode strictly and hand the hook the directive id, type
// and the cloud's created_by for the audit provenance.

var roleTypes = []string{"save_role", "delete_role"}

const testRoleKey = "c_01j9z3k4m5n6p7q8r9s0t1v2w3"

func TestRoleDirectiveTypesAreMainTillOnly(t *testing.T) {
	for _, typ := range roleTypes {
		if !mainTillOnlyTypes[typ] {
			t.Errorf("%s not in mainTillOnlyTypes", typ)
		}
		if catalogTypes[typ] {
			t.Errorf("%s must not be a catalog type", typ)
		}
	}
}

func TestApplyRoleTypes_NilHookUnsupported(t *testing.T) {
	for _, typ := range roleTypes {
		status, msg := apply(context.Background(), directive{ID: "d", Type: typ, Payload: map[string]any{"role": testRoleKey}}, Hooks{})
		if status != "failed" || msg != typ+" is not supported on this till" {
			t.Errorf("%s nil hook: %q %q", typ, status, msg)
		}
	}
}

func roleHooks(got *RoleDirective, calls *int) Hooks {
	h := func(_ context.Context, r RoleDirective) (string, error) { *calls++; *got = r; return "ok", nil }
	return Hooks{SaveRole: h, DeleteRole: h}
}

func TestApplySaveRole_Decode(t *testing.T) {
	var got RoleDirective
	calls := 0
	hooks := roleHooks(&got, &calls)
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"label": "Shift lead", "grants": `[]`}, "missing role"},
		{map[string]any{"role": " ", "label": "Shift lead", "grants": `[]`}, "missing role"},
		{map[string]any{"role": 3.0, "label": "Shift lead", "grants": `[]`}, "missing role"},
		{map[string]any{"role": testRoleKey, "grants": `[]`}, "missing label"},
		{map[string]any{"role": testRoleKey, "label": 4.0, "grants": `[]`}, "bad label"},
		{map[string]any{"role": testRoleKey, "label": "Shift lead"}, "missing grants"},
		{map[string]any{"role": testRoleKey, "label": "Shift lead", "grants": []any{"sales"}}, "bad grants"},
		{map[string]any{"role": testRoleKey, "label": "Shift lead", "grants": `{"a":1}`}, "bad grants"},
		{map[string]any{"role": testRoleKey, "label": "Shift lead", "grants": `null`}, "bad grants"},
		{map[string]any{"role": testRoleKey, "label": "Shift lead", "grants": `["sales", ""]`}, "bad grants"},
		{map[string]any{"role": testRoleKey, "label": "Shift lead", "grants": `[]`, "create": "maybe"}, "bad create"},
	} {
		status, msg := apply(context.Background(), directive{ID: "d1", Type: "save_role", Payload: c.payload}, hooks)
		if status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want failed %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for bad payloads", calls)
	}
	status, _ := apply(context.Background(), directive{ID: "d1", Type: "save_role", CreatedBy: "owner@example.test", Payload: map[string]any{
		"role": " " + testRoleKey + " ", "label": "  Shift lead ", "grants": `[" sales ", "refunds", "sales"]`, "create": true,
	}}, hooks)
	if status != "applied" || calls != 1 {
		t.Fatalf("status %q calls %d", status, calls)
	}
	if got.DirectiveID != "d1" || got.Type != "save_role" || got.CreatedBy != "owner@example.test" || got.Role != testRoleKey ||
		got.Label != "Shift lead" || !got.Create || !reflect.DeepEqual(got.Grants, []string{"sales", "refunds"}) {
		t.Fatalf("decoded %+v", got)
	}
	// create absent → false; an empty grant set is a valid (no-access) role.
	if status, _ := apply(context.Background(), directive{ID: "d2", Type: "save_role", Payload: map[string]any{"role": testRoleKey, "label": "Lead", "grants": `[]`}}, hooks); status != "applied" {
		t.Fatal("update failed")
	}
	if got.Create || got.Grants == nil || len(got.Grants) != 0 {
		t.Fatalf("update decode %+v", got)
	}
}

func TestApplyDeleteRole_Decode(t *testing.T) {
	var got RoleDirective
	calls := 0
	hooks := roleHooks(&got, &calls)
	if status, msg := apply(context.Background(), directive{ID: "x", Type: "delete_role", Payload: map[string]any{}}, hooks); status != "failed" || msg != "missing role" {
		t.Fatalf("%q %q", status, msg)
	}
	if status, _ := apply(context.Background(), directive{ID: "x1", Type: "delete_role", CreatedBy: "o", Payload: map[string]any{"role": testRoleKey}}, hooks); status != "applied" ||
		got.Type != "delete_role" || got.Role != testRoleKey || got.CreatedBy != "o" || got.DirectiveID != "x1" {
		t.Fatalf("delete_role %+v", got)
	}
}

// A satellite till skips both: no apply, no result post.
func TestTickSatelliteSkipsRoleDirectives(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "r1", "type": "save_role", "payload": map[string]any{"role": testRoleKey, "label": "Lead", "grants": `[]`}},
		{"id": "r2", "type": "delete_role", "payload": map[string]any{"role": testRoleKey}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	var got RoleDirective
	calls := 0
	if err := Tick(context.Background(), testCfg(srv.URL), db, roleHooks(&got, &calls)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if calls != 0 || len(cloud.results) != 0 {
		t.Fatalf("satellite applied %d, posted %v", calls, cloud.results)
	}
}
