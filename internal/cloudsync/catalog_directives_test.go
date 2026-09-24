package cloudsync

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// Manage-shop catalog contract §3 (ut-docs
// reference/manage-shop-catalog-api.md): the five new directive types'
// dispatch and payload decoding. Arrays travel as a JSON-encoded STRING
// field, numbers decode from float64 or string, an absent field is nil
// (keep), and a present field of the wrong shape FAILS the directive
// instead of reading as absent.

var newCatalogTypes = []string{"save_item", "save_category", "delete_category", "save_modifier_group", "delete_modifier_group"}

func TestApplyNewCatalogTypes_NilHookUnsupported(t *testing.T) {
	for _, typ := range newCatalogTypes {
		status, msg := apply(context.Background(), directive{Type: typ, Payload: map[string]any{"id": "x"}}, Hooks{})
		if status != "failed" || msg != typ+" is not supported on this till" {
			t.Errorf("%s nil hook: %q %q", typ, status, msg)
		}
	}
}

func TestApplySaveItem(t *testing.T) {
	var calls int
	var got data.ItemPatch
	hooks := Hooks{SaveItem: func(ctx context.Context, p data.ItemPatch) (string, error) {
		calls++
		got = p
		return "updated Latte", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"name": "x"}, "missing id"},
		{map[string]any{"id": " "}, "missing id"},
		{map[string]any{"id": "i1", "name": 3.0}, "bad name"},
		{map[string]any{"id": "i1", "price_minor": "abc"}, "bad price_minor"},
		{map[string]any{"id": "i1", "price_minor": 1.5}, "bad price_minor"},
		{map[string]any{"id": "i1", "active": "maybe"}, "bad active"},
		{map[string]any{"id": "i1", "create": 7.0}, "bad create"},
		{map[string]any{"id": "i1", "barcodes": "not json"}, "bad barcodes"},
		{map[string]any{"id": "i1", "barcodes": []any{"a"}}, "bad barcodes"},
		{map[string]any{"id": "i1", "modifier_group_ids": `{"a":1}`}, "bad modifier_group_ids"},
		{map[string]any{"id": "i1", "modifier_opt_out_ids": 5.0}, "bad modifier_opt_out_ids"},
		{map[string]any{"id": "i1", "color": false}, "bad color"},
	} {
		status, msg := apply(context.Background(), directive{Type: "save_item", Payload: c.payload}, hooks)
		if status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want failed %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for refused payloads", calls)
	}

	// Every field, numbers as strings/floats, unknown fields ignored.
	status, msg := apply(context.Background(), directive{Type: "save_item", Payload: map[string]any{
		"id": " i1 ", "create": true, "name": "Latte", "price_minor": "360", "sku": "HD-3",
		"category_id": "", "color": "#b45309", "barcodes": `["111","222"]`, "active": "true",
		"is_weighed": false, "stock_untracked": true, "modifier_group_ids": `["g1"]`,
		"modifier_opt_out_ids": `[]`, "a_field_from_a_newer_cloud": "ignored",
	}}, hooks)
	if status != "applied" || msg != "updated Latte" {
		t.Fatalf("full: %q %q", status, msg)
	}
	if got.ID != "i1" || !got.Create || *got.Name != "Latte" || *got.PriceMinor != 360 || *got.SKU != "HD-3" ||
		*got.CategoryID != "" || *got.Color != "#b45309" || !reflect.DeepEqual(*got.Barcodes, []string{"111", "222"}) ||
		!*got.Active || *got.IsWeighed || !*got.StockUntracked || !reflect.DeepEqual(*got.ModifierGroupIDs, []string{"g1"}) ||
		got.ModifierOptOutIDs == nil || len(*got.ModifierOptOutIDs) != 0 {
		t.Fatalf("decoded = %+v", got)
	}
	// Absent fields are nil (keep), never zero values.
	status, _ = apply(context.Background(), directive{Type: "save_item", Payload: map[string]any{"id": "i1", "price_minor": 380.0}}, hooks)
	if status != "applied" || got.Create || got.Name != nil || got.Barcodes != nil || got.Active != nil || *got.PriceMinor != 380 {
		t.Fatalf("partial: %+v", got)
	}
}

func TestApplySaveCategory(t *testing.T) {
	var got data.CategorySave
	hooks := Hooks{SaveCategory: func(ctx context.Context, p data.CategorySave) (string, error) {
		got = p
		return "saved", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{}, "missing id"},
		{map[string]any{"id": "c1", "parent_id": 1.0}, "bad parent_id"},
		{map[string]any{"id": "c1", "icon": true}, "bad icon"},
		{map[string]any{"id": "c1", "show_on_sale_screen": "nope"}, "bad show_on_sale_screen"},
		{map[string]any{"id": "c1", "station_ids": "[1,2]"}, "bad station_ids"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "save_category", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want %q", c.payload, status, msg, c.want)
		}
	}
	status, _ := apply(context.Background(), directive{Type: "save_category", Payload: map[string]any{
		"id": "c1", "create": "true", "name": "Hot", "parent_id": "c0", "color": "", "icon": "lucide:coffee",
		"show_on_sale_screen": false, "modifier_group_ids": `["g1"]`, "station_ids": `["s1"]`,
	}}, hooks)
	if status != "applied" || got.ID != "c1" || !got.Create || *got.Name != "Hot" || *got.ParentID != "c0" || *got.Color != "" ||
		*got.Icon != "lucide:coffee" || *got.ShowOnSaleScreen || len(*got.GroupIDs) != 1 || len(*got.StationIDs) != 1 {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestApplyDeleteCategory(t *testing.T) {
	var gotID, gotMove string
	hooks := Hooks{DeleteCategory: func(ctx context.Context, id, moveItemsTo string) (string, error) {
		gotID, gotMove = id, moveItemsTo
		return "deleted", nil
	}}
	if status, msg := apply(context.Background(), directive{Type: "delete_category", Payload: map[string]any{"move_items_to": "c2"}}, hooks); status != "failed" || msg != "missing id" {
		t.Fatalf("missing id: %q %q", status, msg)
	}
	if status, msg := apply(context.Background(), directive{Type: "delete_category", Payload: map[string]any{"id": "c1", "move_items_to": 3.0}}, hooks); status != "failed" || msg != "bad move_items_to" {
		t.Fatalf("bad target: %q %q", status, msg)
	}
	if status, _ := apply(context.Background(), directive{Type: "delete_category", Payload: map[string]any{"id": "c1"}}, hooks); status != "applied" || gotID != "c1" || gotMove != "" {
		t.Fatalf("default target: %q %q", gotID, gotMove)
	}
}

func TestApplySaveModifierGroup(t *testing.T) {
	var got data.ModifierGroupSave
	hooks := Hooks{SaveModifierGroup: func(ctx context.Context, p data.ModifierGroupSave) (string, error) {
		got = p
		return "saved", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"name": "x"}, "missing id"},
		{map[string]any{"id": "g1", "min_select": "x"}, "bad min_select"},
		{map[string]any{"id": "g1", "required": 2.0}, "bad required"},
		{map[string]any{"id": "g1", "options": "[{]"}, "bad options"},
		{map[string]any{"id": "g1", "options": `[{"id":"o1","name":"A","price_delta_minor":"x"}]`}, "bad options"},
		{map[string]any{"id": "g1", "attach_item_ids": 1.0}, "bad attach_item_ids"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "save_modifier_group", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want %q", c.payload, status, msg, c.want)
		}
	}
	status, _ := apply(context.Background(), directive{Type: "save_modifier_group", Payload: map[string]any{
		"id": "g1", "create": true, "name": "Size", "required": true, "min_select": 1.0, "max_select": "2",
		"options":             `[{"id":"o1","name":"Small","price_delta_minor":0,"active":true},{"id":"o2","name":"Large","price_delta_minor":50}]`,
		"attach_category_ids": `["c1"]`, "detach_category_ids": `[]`, "attach_item_ids": `["i1"]`, "detach_item_ids": `["i2"]`,
	}}, hooks)
	if status != "applied" || got.ID != "g1" || !got.Create || *got.Name != "Size" || !*got.Required || *got.MinSelect != 1 || *got.MaxSelect != 2 {
		t.Fatalf("decoded = %+v", got)
	}
	opts := *got.Options
	if len(opts) != 2 || opts[0].ID != "o1" || !opts[0].IsActive || opts[1].PriceDeltaMinor != 50 || !opts[1].IsActive {
		t.Fatalf("options = %+v (an absent active means active)", opts)
	}
	if !reflect.DeepEqual(got.AttachCategoryIDs, []string{"c1"}) || !reflect.DeepEqual(got.AttachItemIDs, []string{"i1"}) || !reflect.DeepEqual(got.DetachItemIDs, []string{"i2"}) {
		t.Fatalf("attachments = %+v", got)
	}
	// options absent → nil (keep every option).
	apply(context.Background(), directive{Type: "save_modifier_group", Payload: map[string]any{"id": "g1", "name": "Cup"}}, hooks)
	if got.Options != nil || got.MinSelect != nil {
		t.Fatalf("absent options/min must be nil: %+v", got)
	}
}

func TestApplyDeleteModifierGroup(t *testing.T) {
	var gotID string
	hooks := Hooks{DeleteModifierGroup: func(ctx context.Context, id string) (string, error) {
		gotID = id
		return "deleted", nil
	}}
	if status, msg := apply(context.Background(), directive{Type: "delete_modifier_group", Payload: map[string]any{}}, hooks); status != "failed" || msg != "missing id" {
		t.Fatalf("missing id: %q %q", status, msg)
	}
	if status, _ := apply(context.Background(), directive{Type: "delete_modifier_group", Payload: map[string]any{"id": "g1"}}, hooks); status != "applied" || gotID != "g1" {
		t.Fatalf("delete: %q", gotID)
	}
}

// §3: on a satellite till (sync.primary_url set) Tick SKIPS the five
// main-till-only types — no apply and no result post — so the directive
// stays pending for the main till. Other types still apply as before.
func TestTickSatelliteSkipsMainTillOnlyTypes(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "d1", "type": "save_item", "payload": map[string]any{"id": "i1", "name": "x"}},
		{"id": "d2", "type": "save_category", "payload": map[string]any{"id": "c1"}},
		{"id": "d3", "type": "delete_category", "payload": map[string]any{"id": "c1"}},
		{"id": "d4", "type": "save_modifier_group", "payload": map[string]any{"id": "g1"}},
		{"id": "d5", "type": "delete_modifier_group", "payload": map[string]any{"id": "g1"}},
		{"id": "d6", "type": "set_setting", "payload": map[string]any{"key": "k", "value": "v"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	ran := 0
	hooks := Hooks{
		SaveItem:            func(context.Context, data.ItemPatch) (string, error) { ran++; return "", nil },
		SaveCategory:        func(context.Context, data.CategorySave) (string, error) { ran++; return "", nil },
		DeleteCategory:      func(context.Context, string, string) (string, error) { ran++; return "", nil },
		SaveModifierGroup:   func(context.Context, data.ModifierGroupSave) (string, error) { ran++; return "", nil },
		DeleteModifierGroup: func(context.Context, string) (string, error) { ran++; return "", nil },
		SetSetting:          func(context.Context, string, string) (string, error) { return "set", nil },
	}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if ran != 0 {
		t.Fatalf("a satellite applied %d main-till-only directives", ran)
	}
	if len(cloud.results) != 1 || cloud.results[0]["directive_id"] != "d6" {
		t.Fatalf("results = %+v, want only d6 (skipped types post nothing)", cloud.results)
	}
}

// §3.6 + §3.7: after at least one catalog directive is APPLIED, the same
// tick pushes the snapshot again (schema 2), so the cloud grid converges
// without waiting a tick.
func TestTickPushesSnapshotAgainAfterCatalogDirective(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "d1", "type": "save_item", "payload": map[string]any{"id": "it-1", "price_minor": 150.0}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "Coke", BasePrice: 120, IsActive: true})
	hooks := Hooks{SaveItem: func(ctx context.Context, p data.ItemPatch) (string, error) {
		_, err := db.Exec(`UPDATE items SET base_price = ? WHERE id = ?`, *p.PriceMinor, p.ID)
		return "updated Coke", err
	}}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2 (before and after the applied directive)", len(cloud.snapshots))
	}
	row := cloud.snapshots[1]["items"].([]any)[0].(map[string]any)
	if row["price_minor"] != 150.0 {
		t.Fatalf("second push carries price %v, want 150", row["price_minor"])
	}
}

// §3.7 schema 2: inactive items included (active first), variants nested,
// the full field set present, and the legacy "barcode" kept for an older
// reader.
func TestSnapshotSchema2Shape(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "Coca-Cola", BasePrice: 120, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-0", SKU: "SKU0", Name: "Aardvark", BasePrice: 90, IsActive: false})
	testsupport.SeedBarcode(t, db, "5000000000011", "it-1", true)
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var-1", ItemID: "it-1", SKU: "SKU1-L", Name: "1.5L", Price: 210, IsActive: true})
	testsupport.SeedVariantBarcode(t, db, "5000000000028", "var-1", true)
	if _, err := db.Exec(`UPDATE items SET stock_untracked = 1 WHERE id = 'it-0'`); err != nil {
		t.Fatal(err)
	}

	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db); err != nil {
		t.Fatal(err)
	}
	snap := cloud.snapshots[0]
	if snap["schema"] != 2.0 || snap["store_id"] != "store-1" {
		t.Fatalf("envelope = %v / %v", snap["schema"], snap["store_id"])
	}
	items := snap["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (variant nested, inactive included)", len(items))
	}
	row := items[0].(map[string]any)
	if row["id"] != "it-1" || items[1].(map[string]any)["active"] != false {
		t.Fatalf("active items must come first: %v", items)
	}
	for _, k := range []string{"id", "name", "sku", "price_minor", "category_id", "color", "active", "is_weighed", "stock_untracked",
		"barcode", "barcodes", "modifier_group_ids", "modifier_opt_out_ids", "effective_modifier_group_ids", "variants"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing %q: %v", k, row)
		}
	}
	if row["barcode"] != "5000000000011" || !reflect.DeepEqual(row["barcodes"], []any{"5000000000011"}) {
		t.Fatalf("barcodes = %v / %v", row["barcode"], row["barcodes"])
	}
	vs := row["variants"].([]any)
	v := vs[0].(map[string]any)
	if len(vs) != 1 || v["id"] != "var-1" || v["name"] != "1.5L" || v["sku"] != "SKU1-L" || v["price_minor"] != 210.0 ||
		v["active"] != true || !reflect.DeepEqual(v["barcodes"], []any{"5000000000028"}) {
		t.Fatalf("variant = %v", v)
	}
	if _, has := v["qty"]; has {
		t.Fatal("a variant carries no qty")
	}
	if _, has := row["qty"]; !has {
		t.Fatal("a tracked item carries qty")
	}
	if _, has := items[1].(map[string]any)["qty"]; has {
		t.Fatal("qty must be omitted for a stock-untracked item")
	}
}
