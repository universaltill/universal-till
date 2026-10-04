package cloudsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Stock locations from my. (ut-docs#3383): create_stock_location,
// rename_stock_location and set_stock_location_active dispatch like
// upsert_category — apply validates the payload shape and the hook (pages'
// cloudCreateStockLocation & co.) does the till-side work.

func TestApplyStockLocationDirectives_NilHooks(t *testing.T) {
	for _, typ := range []string{"create_stock_location", "rename_stock_location", "set_stock_location_active"} {
		status, msg := apply(context.Background(), directive{Type: typ, Payload: map[string]any{
			"name": "Back room", "location_id": "loc-1", "active": false,
		}}, Hooks{})
		if status != "failed" || msg != typ+" is not supported on this till" {
			t.Fatalf("%s nil hook: status=%q msg=%q", typ, status, msg)
		}
	}
}

func TestApplyCreateStockLocation(t *testing.T) {
	var got string
	hooks := Hooks{CreateStockLocation: func(_ context.Context, name string) (string, error) {
		got = name
		return "created stock location " + name, nil
	}}
	status, msg := apply(context.Background(), directive{Type: "create_stock_location", Payload: map[string]any{"name": "  "}}, hooks)
	if status != "failed" || msg != "missing name" {
		t.Fatalf("blank name: status=%q msg=%q", status, msg)
	}
	status, msg = apply(context.Background(), directive{Type: "create_stock_location", Payload: map[string]any{"name": " Back room "}}, hooks)
	if status != "applied" || msg != "created stock location Back room" || got != "Back room" {
		t.Fatalf("create: status=%q msg=%q got=%q", status, msg, got)
	}
}

func TestApplyRenameStockLocation(t *testing.T) {
	var gotID, gotName string
	hooks := Hooks{RenameStockLocation: func(_ context.Context, id, name string) (string, error) {
		gotID, gotName = id, name
		return "renamed", nil
	}}
	for _, p := range []map[string]any{{"name": "x"}, {"location_id": "loc-1"}, {"location_id": " ", "name": "x"}} {
		if status, msg := apply(context.Background(), directive{Type: "rename_stock_location", Payload: p}, hooks); status != "failed" || msg != "missing location_id or name" {
			t.Fatalf("payload %v: status=%q msg=%q", p, status, msg)
		}
	}
	status, _ := apply(context.Background(), directive{Type: "rename_stock_location", Payload: map[string]any{"location_id": " loc-1 ", "name": " Cellar "}}, hooks)
	if status != "applied" || gotID != "loc-1" || gotName != "Cellar" {
		t.Fatalf("rename: status=%q id=%q name=%q", status, gotID, gotName)
	}
}

func TestApplySetStockLocationActive(t *testing.T) {
	var gotID string
	var gotActive *bool
	hooks := Hooks{SetStockLocationActive: func(_ context.Context, id string, active bool) (string, error) {
		gotID, gotActive = id, &active
		return "ok", nil
	}}
	if status, msg := apply(context.Background(), directive{Type: "set_stock_location_active", Payload: map[string]any{"active": false}}, hooks); status != "failed" || msg != "missing location_id" {
		t.Fatalf("no id: status=%q msg=%q", status, msg)
	}
	// active must be present: an absent flag is not read as "deactivate".
	if status, msg := apply(context.Background(), directive{Type: "set_stock_location_active", Payload: map[string]any{"location_id": "loc-1"}}, hooks); status != "failed" || msg != "missing or invalid active" {
		t.Fatalf("no active: status=%q msg=%q", status, msg)
	}
	if status, msg := apply(context.Background(), directive{Type: "set_stock_location_active", Payload: map[string]any{"location_id": "loc-1", "active": "maybe"}}, hooks); status != "failed" || msg != "missing or invalid active" {
		t.Fatalf("bad active: status=%q msg=%q", status, msg)
	}
	// A JSON bool and its string form both decode.
	for _, tc := range []struct {
		raw  any
		want bool
	}{{false, false}, {true, true}, {"false", false}, {"true", true}} {
		gotActive = nil
		status, _ := apply(context.Background(), directive{Type: "set_stock_location_active", Payload: map[string]any{"location_id": "loc-1", "active": tc.raw}}, hooks)
		if status != "applied" || gotID != "loc-1" || gotActive == nil || *gotActive != tc.want {
			t.Fatalf("active=%v: status=%q id=%q active=%v", tc.raw, status, gotID, gotActive)
		}
	}
	// The hook's refusal is the directive's failure message, verbatim.
	hooks.SetStockLocationActive = func(context.Context, string, bool) (string, error) {
		return "", errors.New("Cannot deactivate a location that still holds stock or has an active register")
	}
	status, msg := apply(context.Background(), directive{Type: "set_stock_location_active", Payload: map[string]any{"location_id": "loc-1", "active": false}}, hooks)
	if status != "failed" || msg != "Cannot deactivate a location that still holds stock or has an active register" {
		t.Fatalf("refusal: status=%q msg=%q", status, msg)
	}
}

// Main-till only (stock_locations is primary-wins synced) and a catalog
// type, so the snapshot — which now carries the locations — is re-pushed
// in the same tick.
func TestStockLocationDirectivesAreMainTillOnlyCatalogTypes(t *testing.T) {
	for _, typ := range []string{"create_stock_location", "rename_stock_location", "set_stock_location_active"} {
		if !mainTillOnlyTypes[typ] {
			t.Errorf("%s missing from mainTillOnlyTypes", typ)
		}
		if !catalogTypes[typ] {
			t.Errorf("%s missing from catalogTypes", typ)
		}
	}
}

// The catalog snapshot carries the till's stock locations (inactive ones
// included) as [{id, name, is_active}], and a locations-only change pushes
// again: the field is part of the hashed payload.
func TestSnapshotIncludesStockLocations(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	d := openMigratedDB(t, "cloudsync.db")
	ctx := context.Background()
	repo := data.NewPOSRepo(d.DB)
	backID, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStockLocationActive(ctx, backID, false); err != nil {
		t.Fatal(err)
	}

	if err := Tick(ctx, testCfg(srv.URL), d.DB, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(cloud.snapshots))
	}
	if _, ok := cloud.snapshots[0]["items"]; !ok {
		t.Fatal("items field missing: stock_locations must be additive")
	}
	locs, ok := cloud.snapshots[0]["stock_locations"].([]any)
	if !ok {
		t.Fatalf("stock_locations = %#v, want an array", cloud.snapshots[0]["stock_locations"])
	}
	byID := map[string]map[string]any{}
	for _, l := range locs {
		m := l.(map[string]any)
		if len(m) != 3 {
			t.Fatalf("location row %v: want exactly id, name, is_active", m)
		}
		byID[m["id"].(string)] = m
	}
	if m := byID["loc_main"]; m == nil || m["name"] != "Main Store" || m["is_active"] != true {
		t.Fatalf("loc_main row = %v", m)
	}
	if m := byID[backID]; m == nil || m["name"] != "Back room" || m["is_active"] != false {
		t.Fatalf("inactive location row = %v", m)
	}

	// Unchanged: no second push.
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), d.DB); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("unchanged snapshot pushed again: %d", len(cloud.snapshots))
	}
	// A locations-only change pushes.
	if err := repo.RenameStockLocation(ctx, backID, "Cellar"); err != nil {
		t.Fatal(err)
	}
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), d.DB); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 2 {
		t.Fatalf("a renamed location must re-push the snapshot: %d pushes", len(cloud.snapshots))
	}
}
