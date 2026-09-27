package cloudsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Till user directives (ut-docs reference/till-user-directives.md §4):
// save_user, set_user_pin and deactivate_user are main-till only, decode
// strictly (a present field of the wrong shape fails) and hand the hook the
// directive id, type and the cloud's created_by for the seal's AAD and the
// audit provenance.

var userTypes = []string{"save_user", "set_user_pin", "deactivate_user"}

func TestUserDirectiveTypesAreMainTillOnly(t *testing.T) {
	for _, typ := range userTypes {
		if !mainTillOnlyTypes[typ] {
			t.Errorf("%s not in mainTillOnlyTypes", typ)
		}
		if catalogTypes[typ] {
			t.Errorf("%s must not be a catalog type", typ)
		}
	}
}

func TestApplyUserTypes_NilHookUnsupported(t *testing.T) {
	for _, typ := range userTypes {
		status, msg := apply(context.Background(), directive{ID: "d", Type: typ, Payload: map[string]any{"user_id": "u", "pin_sealed": "x"}}, Hooks{})
		if status != "failed" || msg != typ+" is not supported on this till" {
			t.Errorf("%s nil hook: %q %q", typ, status, msg)
		}
	}
}

func userHooks(got *UserDirective, calls *int) Hooks {
	h := func(_ context.Context, u UserDirective) (string, error) { *calls++; *got = u; return "ok", nil }
	return Hooks{SaveUser: h, SetUserPIN: h, DeactivateUser: h}
}

func TestApplySaveUser_Decode(t *testing.T) {
	var got UserDirective
	calls := 0
	hooks := userHooks(&got, &calls)
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"username": "x"}, "missing user_id"},
		{map[string]any{"user_id": " "}, "missing user_id"},
		{map[string]any{"user_id": 3.0}, "missing user_id"},
		{map[string]any{"user_id": "u1", "create": 7.0}, "bad create"},
		{map[string]any{"user_id": "u1", "username": 3.0}, "bad username"},
		{map[string]any{"user_id": "u1", "display_name": false}, "bad display_name"},
		{map[string]any{"user_id": "u1", "role": []any{"admin"}}, "bad role"},
		{map[string]any{"user_id": "u1", "pin_sealed": 12.0}, "bad pin_sealed"},
		{map[string]any{"user_id": "u1", "pin_sealed": ""}, "bad pin_sealed"},
		{map[string]any{"user_id": "u1", "active": "maybe"}, "bad active"},
		{map[string]any{"user_id": "u1", "active": false}, "bad active"},
		{map[string]any{"user_id": "u1"}, "nothing to update"},
	} {
		status, msg := apply(context.Background(), directive{ID: "d1", Type: "save_user", Payload: c.payload}, hooks)
		if status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want failed %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for bad payloads", calls)
	}
	status, _ := apply(context.Background(), directive{ID: "d1", Type: "save_user", CreatedBy: "owner@example.test", Payload: map[string]any{
		"user_id": " u1 ", "create": true, "username": " anna ", "display_name": "Anna", "role": "cashier", "pin_sealed": "v1.x.y.z",
	}}, hooks)
	if status != "applied" || calls != 1 {
		t.Fatalf("status %q calls %d", status, calls)
	}
	if got.DirectiveID != "d1" || got.Type != "save_user" || got.CreatedBy != "owner@example.test" || got.UserID != "u1" || !got.Create ||
		*got.Username != "anna" || *got.DisplayName != "Anna" || *got.Role != "cashier" || *got.PINSealed != "v1.x.y.z" || got.Active != nil {
		t.Fatalf("decoded %+v", got)
	}
	// Absent means keep: only role on an update.
	if status, _ := apply(context.Background(), directive{ID: "d2", Type: "save_user", Payload: map[string]any{"user_id": "u1", "role": "manager"}}, hooks); status != "applied" {
		t.Fatal("role-only update failed")
	}
	if got.Username != nil || got.DisplayName != nil || got.PINSealed != nil || got.Create || *got.Role != "manager" {
		t.Fatalf("partial decode %+v", got)
	}
	// active:true is accepted (the hook requires the PIN with it).
	if status, _ := apply(context.Background(), directive{ID: "d3", Type: "save_user", Payload: map[string]any{"user_id": "u1", "active": true}}, hooks); status != "applied" || got.Active == nil || !*got.Active {
		t.Fatalf("active:true %+v", got)
	}
}

func TestApplySetUserPINAndDeactivate_Decode(t *testing.T) {
	var got UserDirective
	calls := 0
	hooks := userHooks(&got, &calls)
	for _, c := range []struct {
		typ     string
		payload map[string]any
		want    string
	}{
		{"set_user_pin", map[string]any{"pin_sealed": "v1.a.b.c"}, "missing user_id"},
		{"set_user_pin", map[string]any{"user_id": "u1"}, "missing pin_sealed"},
		{"set_user_pin", map[string]any{"user_id": "u1", "pin_sealed": 4.0}, "missing pin_sealed"},
		{"deactivate_user", map[string]any{}, "missing user_id"},
	} {
		status, msg := apply(context.Background(), directive{ID: "d", Type: c.typ, Payload: c.payload}, hooks)
		if status != "failed" || msg != c.want {
			t.Errorf("%s %v: %q %q, want %q", c.typ, c.payload, status, msg, c.want)
		}
	}
	if status, _ := apply(context.Background(), directive{ID: "p1", Type: "set_user_pin", Payload: map[string]any{"user_id": "u1", "pin_sealed": "v1.a.b.c"}}, hooks); status != "applied" ||
		got.Type != "set_user_pin" || got.DirectiveID != "p1" || *got.PINSealed != "v1.a.b.c" {
		t.Fatalf("set_user_pin %+v", got)
	}
	if status, _ := apply(context.Background(), directive{ID: "x1", Type: "deactivate_user", Payload: map[string]any{"user_id": "u1"}}, hooks); status != "applied" ||
		got.Type != "deactivate_user" || got.UserID != "u1" || got.PINSealed != nil {
		t.Fatalf("deactivate_user %+v", got)
	}
}

// created_by rides the wire when the cloud sends it and is "" otherwise.
func TestDirectiveCreatedByDecodes(t *testing.T) {
	var d directive
	if err := json.Unmarshal([]byte(`{"id":"a","type":"save_user","created_by":"owner-1","payload":{}}`), &d); err != nil || d.CreatedBy != "owner-1" {
		t.Fatalf("%+v %v", d, err)
	}
	d = directive{}
	if err := json.Unmarshal([]byte(`{"id":"a","type":"save_user","payload":{}}`), &d); err != nil || d.CreatedBy != "" {
		t.Fatalf("%+v %v", d, err)
	}
}

// A satellite till skips all three: no apply, no result post.
func TestTickSatelliteSkipsUserDirectives(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "u1", "type": "save_user", "payload": map[string]any{"user_id": "x", "role": "cashier"}},
		{"id": "u2", "type": "set_user_pin", "payload": map[string]any{"user_id": "x", "pin_sealed": "v1.a.b.c"}},
		{"id": "u3", "type": "deactivate_user", "payload": map[string]any{"user_id": "x"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	var got UserDirective
	calls := 0
	if err := Tick(context.Background(), testCfg(srv.URL), db, userHooks(&got, &calls)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if calls != 0 || len(cloud.results) != 0 {
		t.Fatalf("satellite applied %d, posted %v", calls, cloud.results)
	}
}

// The main till applies them and posts the result; created_by reaches the
// hook.
func TestTickMainTillAppliesUserDirective(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "u3", "type": "deactivate_user", "created_by": "owner-7", "payload": map[string]any{"user_id": "x"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	var got UserDirective
	calls := 0
	if err := Tick(context.Background(), testCfg(srv.URL), db, userHooks(&got, &calls)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if calls != 1 || got.CreatedBy != "owner-7" || len(cloud.results) != 1 || cloud.results[0]["status"] != "applied" {
		t.Fatalf("calls %d got %+v results %v", calls, got, cloud.results)
	}
}
