package cloudsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// apply_item_option_sets (ut-docs#4163, contract §3.13): {item_id,
// option_set_ids: JSON []string (full ordered list, [] clears), generate?}.
// A wrong shape fails before the hook runs; the hook's error is the
// directive's failure text.
func TestApplyApplyItemOptionSets(t *testing.T) {
	var got ItemOptionSetsApply
	calls := 0
	hooks := Hooks{ApplyItemOptionSets: func(ctx context.Context, p ItemOptionSetsApply) (string, error) {
		calls++
		got = p
		return "applied", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"option_set_ids": `[]`}, "missing item_id"},
		{map[string]any{"item_id": " ", "option_set_ids": `[]`}, "missing item_id"},
		{map[string]any{"item_id": "i1"}, "missing option_set_ids"},
		{map[string]any{"item_id": "i1", "option_set_ids": []any{"s1"}}, "bad option_set_ids"},
		{map[string]any{"item_id": "i1", "option_set_ids": `[1]`}, "bad option_set_ids"},
		{map[string]any{"item_id": "i1", "option_set_ids": `null`}, "bad option_set_ids"},
		{map[string]any{"item_id": "i1", "option_set_ids": `[]`, "generate": "perhaps"}, "bad generate"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "apply_item_option_sets", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("a malformed payload reached the hook %d time(s)", calls)
	}

	status, msg := apply(context.Background(), directive{Type: "apply_item_option_sets", Payload: map[string]any{
		"item_id": " i1 ", "option_set_ids": `[" s-size ","s-colour"]`, "generate": true,
	}}, hooks)
	if status != "applied" || msg != "applied" {
		t.Fatalf("apply: %q %q", status, msg)
	}
	want := ItemOptionSetsApply{ItemID: "i1", OptionSetIDs: []string{"s-size", "s-colour"}, Generate: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
	// generate absent = false; [] is a real (clearing) list, not "absent".
	apply(context.Background(), directive{Type: "apply_item_option_sets", Payload: map[string]any{"item_id": "i1", "option_set_ids": `[]`}}, hooks)
	if got.Generate || got.OptionSetIDs == nil || len(got.OptionSetIDs) != 0 {
		t.Fatalf("clear decoded %+v", got)
	}

	hooks.ApplyItemOptionSets = func(context.Context, ItemOptionSetsApply) (string, error) {
		return "", errors.New("option set Retired is inactive")
	}
	if status, msg := apply(context.Background(), directive{Type: "apply_item_option_sets", Payload: map[string]any{"item_id": "i1", "option_set_ids": `["x"]`}}, hooks); status != "failed" || msg != "option set Retired is inactive" {
		t.Fatalf("hook error: %q %q", status, msg)
	}
}

func TestApplyItemOptionSetsIsMainTillOnlyCatalogType(t *testing.T) {
	if !mainTillOnlyTypes["apply_item_option_sets"] {
		t.Error("apply_item_option_sets missing from mainTillOnlyTypes")
	}
	if !catalogTypes["apply_item_option_sets"] {
		t.Error("apply_item_option_sets missing from catalogTypes (the snapshot must be pushed again)")
	}
}

// §3.7: every snapshot item carries option_set_ids, axis order, [] for none.
func TestSnapshotReportsOptionSetIDs(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "T-shirt", BasePrice: 1500, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-2", SKU: "SKU2", Name: "Mug", BasePrice: 800, IsActive: true})
	for _, q := range []string{
		`INSERT INTO option_sets (id, name) VALUES ('s-colour','Colour'), ('s-size','Size')`,
		`INSERT INTO item_option_sets (item_id, option_set_id, axis_order) VALUES ('it-1','s-colour',1), ('it-1','s-size',0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db, nil); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, it := range cloud.snapshots[0]["items"].([]any) {
		row := it.(map[string]any)
		byID[row["id"].(string)] = row
	}
	if got := byID["it-1"]["option_set_ids"]; !reflect.DeepEqual(got, []any{"s-size", "s-colour"}) {
		t.Fatalf("it-1 option_set_ids = %v, want [s-size s-colour]", got)
	}
	if got := byID["it-2"]["option_set_ids"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("it-2 option_set_ids = %#v, want []", got)
	}
}
