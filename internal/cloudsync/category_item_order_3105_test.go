package cloudsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// §3.14 (ut-docs#3105): set_category_item_order carries the category id and
// the category's full item order as a JSON-encoded string field. Same strict
// decode as set_category_order: a malformed list, a blank id or a duplicate
// FAILS the directive visibly instead of being dropped or guessed at.
func TestApplySetCategoryItemOrder(t *testing.T) {
	ok := map[string]any{"category_id": "c1", "item_ids": `["i1"]`}
	if status, msg := apply(context.Background(), directive{Type: "set_category_item_order", Payload: ok}, Hooks{}); status != "failed" || msg != "set_category_item_order is not supported on this till" {
		t.Fatalf("nil hook: %q %q", status, msg)
	}
	var calls int
	var gotCat string
	var gotIDs []string
	hooks := Hooks{SetCategoryItemOrder: func(ctx context.Context, cat string, ids []string) (string, error) {
		calls++
		gotCat, gotIDs = cat, ids
		return "item order applied to 3 items in category Drinks", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"item_ids": `["i1"]`}, "missing category_id"},
		{map[string]any{"category_id": " ", "item_ids": `["i1"]`}, "missing category_id"},
		{map[string]any{"category_id": 5.0, "item_ids": `["i1"]`}, "bad category_id"},
		{map[string]any{"category_id": []any{"c1"}, "item_ids": `["i1"]`}, "bad category_id"},
		{map[string]any{"category_id": "c1"}, "missing item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": `[]`}, "missing item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": `null`}, "missing item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": ""}, "bad item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": "i1,i2"}, "bad item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": `[1,2]`}, "bad item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": `{"a":"b"}`}, "bad item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": []any{"i1"}}, "bad item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": 5.0}, "bad item_ids"},
		{map[string]any{"category_id": "c1", "item_ids": `["i1"," ","i2"]`}, "blank item id"},
		{map[string]any{"category_id": "c1", "item_ids": `["i1","i2"," i1 "]`}, "duplicate item id i1"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "set_category_item_order", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want failed %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for refused payloads", calls)
	}
	status, msg := apply(context.Background(), directive{Type: "set_category_item_order", Payload: map[string]any{"category_id": " c1 ", "item_ids": `[" i3 ","i1","i2"]`}}, hooks)
	if status != "applied" || msg != "item order applied to 3 items in category Drinks" || gotCat != "c1" || !reflect.DeepEqual(gotIDs, []string{"i3", "i1", "i2"}) {
		t.Fatalf("applied: %q %q %q %v", status, msg, gotCat, gotIDs)
	}
}

// §3.14: set_category_item_order is main-till only (a satellite leaves it
// pending, no result post) and a catalog type (an applied one re-pushes in
// the same tick).
func TestSetCategoryItemOrderIsMainTillOnlyCatalogType(t *testing.T) {
	if !mainTillOnlyTypes["set_category_item_order"] {
		t.Error("set_category_item_order not in mainTillOnlyTypes")
	}
	if !catalogTypes["set_category_item_order"] {
		t.Error("set_category_item_order not in catalogTypes")
	}
}

func TestTickSatelliteSkipsSetCategoryItemOrder(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "d1", "type": "set_category_item_order", "payload": map[string]any{"category_id": "c1", "item_ids": `["i1"]`}},
		{"id": "d2", "type": "set_setting", "payload": map[string]any{"key": "k", "value": "v"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	ran := 0
	hooks := Hooks{
		SetCategoryItemOrder: func(context.Context, string, []string) (string, error) { ran++; return "", nil },
		SetSetting:           func(context.Context, string, string) (string, error) { return "set", nil },
	}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if ran != 0 {
		t.Fatalf("a satellite applied %d set_category_item_order directives", ran)
	}
	if len(cloud.results) != 1 || cloud.results[0]["directive_id"] != "d2" {
		t.Fatalf("results = %+v, want only d2 (a skipped type posts nothing)", cloud.results)
	}
}

// §3.7 (ut-docs#3105): the snapshot reports each item's first sale-screen
// tile position as tile_pos, so my. can show the till's effective order. It
// is absent for an item with no tile, and absent everywhere when the hook is
// missing or fails (the cloud then keeps the values it had, §3.7).
func TestSnapshotItemTilePos(t *testing.T) {
	origData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origData) })
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"it-a", "it-b", "it-gone"} {
		testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: id, SKU: id, Name: id, BasePrice: 100, IsActive: true})
	}
	positions := func(hook func(context.Context) (map[string]int, error)) map[string]any {
		cloud := &fakeCloud{}
		srv := httptest.NewServer(cloud.handler())
		defer srv.Close()
		if _, err := db.Exec(`DELETE FROM settings`); err != nil { // defeat the hash gate
			t.Fatal(err)
		}
		if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db, hook); err != nil {
			t.Fatal(err)
		}
		out := map[string]any{}
		for _, it := range cloud.snapshots[0]["items"].([]any) {
			m := it.(map[string]any)
			if v, ok := m["tile_pos"]; ok {
				out[m["id"].(string)] = v
			}
		}
		return out
	}
	got := positions(func(context.Context) (map[string]int, error) { return map[string]int{"it-b": 0, "it-a": 3}, nil })
	if !reflect.DeepEqual(got, map[string]any{"it-b": 0.0, "it-a": 3.0}) {
		t.Fatalf("tile_pos = %v, want it-b 0 (zero must still be sent) and it-a 3, nothing for it-gone", got)
	}
	if got := positions(func(context.Context) (map[string]int, error) { return nil, errors.New("boom") }); len(got) != 0 {
		t.Fatalf("a hook error must omit tile_pos everywhere, got %v", got)
	}
	if got := positions(nil); len(got) != 0 {
		t.Fatalf("a nil hook must omit tile_pos everywhere, got %v", got)
	}
}
