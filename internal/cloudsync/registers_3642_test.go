package cloudsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Registers from my. (ut-docs#3642): set_register_location assigns a
// register's stock location; apply validates the payload shape and the hook
// (pages' cloudSetRegisterLocation) does the till-side work.

func TestApplySetRegisterLocation_NilHook(t *testing.T) {
	status, msg := apply(context.Background(), directive{Type: "set_register_location", Payload: map[string]any{
		"register_id": "reg-1", "location_id": "loc-1",
	}}, Hooks{})
	if status != "failed" || msg != "set_register_location is not supported on this till" {
		t.Fatalf("nil hook: status=%q msg=%q", status, msg)
	}
}

func TestApplySetRegisterLocation(t *testing.T) {
	var gotReg string
	var gotLoc *string
	hooks := Hooks{SetRegisterLocation: func(_ context.Context, registerID, locationID string) (string, error) {
		gotReg, gotLoc = registerID, &locationID
		return "ok", nil
	}}
	for _, p := range []map[string]any{{"location_id": "loc-1"}, {"register_id": "  ", "location_id": "loc-1"}} {
		if status, msg := apply(context.Background(), directive{Type: "set_register_location", Payload: p}, hooks); status != "failed" || msg != "missing register_id" {
			t.Fatalf("payload %v: status=%q msg=%q", p, status, msg)
		}
	}
	// location_id must be PRESENT: an absent key is not read as "unassign".
	if status, msg := apply(context.Background(), directive{Type: "set_register_location", Payload: map[string]any{"register_id": "reg-1"}}, hooks); status != "failed" || msg != "missing location_id" {
		t.Fatalf("no location_id: status=%q msg=%q", status, msg)
	}
	if gotLoc != nil {
		t.Fatal("hook ran on an invalid payload")
	}
	if status, msg := apply(context.Background(), directive{Type: "set_register_location", Payload: map[string]any{"register_id": "reg-1", "location_id": 7}}, hooks); status != "failed" || msg != "invalid location_id" {
		t.Fatalf("non-string location_id: status=%q msg=%q", status, msg)
	}
	status, _ := apply(context.Background(), directive{Type: "set_register_location", Payload: map[string]any{"register_id": " reg-1 ", "location_id": " loc-1 "}}, hooks)
	if status != "applied" || gotReg != "reg-1" || gotLoc == nil || *gotLoc != "loc-1" {
		t.Fatalf("assign: status=%q reg=%q loc=%v", status, gotReg, gotLoc)
	}
	// "" (present) means unassign.
	gotLoc = nil
	status, _ = apply(context.Background(), directive{Type: "set_register_location", Payload: map[string]any{"register_id": "reg-1", "location_id": ""}}, hooks)
	if status != "applied" || gotLoc == nil || *gotLoc != "" {
		t.Fatalf("unassign: status=%q loc=%v", status, gotLoc)
	}
	// The hook's refusal is the directive's failure message, verbatim.
	hooks.SetRegisterLocation = func(context.Context, string, string) (string, error) {
		return "", errors.New("stock location Back room is inactive")
	}
	status, msg := apply(context.Background(), directive{Type: "set_register_location", Payload: map[string]any{"register_id": "reg-1", "location_id": "loc-1"}}, hooks)
	if status != "failed" || msg != "stock location Back room is inactive" {
		t.Fatalf("refusal: status=%q msg=%q", status, msg)
	}
}

// Main-till only (registers sync primary-wins) and a catalog type, so the
// snapshot — which now carries the registers — is re-pushed in the same tick.
func TestSetRegisterLocationIsMainTillOnlyCatalogType(t *testing.T) {
	if !mainTillOnlyTypes["set_register_location"] {
		t.Error("set_register_location missing from mainTillOnlyTypes")
	}
	if !catalogTypes["set_register_location"] {
		t.Error("set_register_location missing from catalogTypes")
	}
}

// The catalog snapshot carries every register (inactive included) as
// [{id, name, is_active, location_id}] with "" for an unassigned one, and a
// registers-only change pushes again: the field is part of the hashed
// payload.
func TestSnapshotIncludesRegisters(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	d := openMigratedDB(t, "cloudsync.db")
	ctx := context.Background()
	repo := data.NewPOSRepo(d.DB)
	loc := "loc_main"
	frontID, err := repo.CreateRegister(ctx, "Front till", &loc)
	if err != nil {
		t.Fatal(err)
	}
	spareID, err := repo.CreateRegister(ctx, "Spare till", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRegisterActive(ctx, spareID, false); err != nil {
		t.Fatal(err)
	}

	if err := Tick(ctx, testCfg(srv.URL), d.DB, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(cloud.snapshots))
	}
	if _, ok := cloud.snapshots[0]["items"]; !ok {
		t.Fatal("items field missing: registers must be additive")
	}
	regs, ok := cloud.snapshots[0]["registers"].([]any)
	if !ok {
		t.Fatalf("registers = %#v, want an array", cloud.snapshots[0]["registers"])
	}
	byID := map[string]map[string]any{}
	for _, r := range regs {
		m := r.(map[string]any)
		if len(m) != 4 {
			t.Fatalf("register row %v: want exactly id, name, is_active, location_id", m)
		}
		byID[m["id"].(string)] = m
	}
	if m := byID[frontID]; m == nil || m["name"] != "Front till" || m["is_active"] != true || m["location_id"] != "loc_main" {
		t.Fatalf("assigned register row = %v", m)
	}
	if m := byID[spareID]; m == nil || m["name"] != "Spare till" || m["is_active"] != false || m["location_id"] != "" {
		t.Fatalf("inactive unassigned register row = %v", m)
	}

	// Unchanged: no second push.
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), d.DB, nil); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("unchanged snapshot pushed again: %d", len(cloud.snapshots))
	}
	// A registers-only change pushes.
	if err := repo.SetRegisterLocation(ctx, frontID, nil); err != nil {
		t.Fatal(err)
	}
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), d.DB, nil); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 2 {
		t.Fatalf("a re-assigned register must re-push the snapshot: %d pushes", len(cloud.snapshots))
	}
	regs, _ = cloud.snapshots[1]["registers"].([]any)
	for _, r := range regs {
		if m := r.(map[string]any); m["id"] == frontID && m["location_id"] != "" {
			t.Fatalf("unassigned register row = %v", m)
		}
	}
}
