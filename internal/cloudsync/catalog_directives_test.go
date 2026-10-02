package cloudsync

import (
	"context"
	"errors"
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

var newCatalogTypes = []string{"save_item", "save_category", "delete_category", "save_modifier_group", "delete_modifier_group", "delete_item", "save_option_set", "delete_option_set"}

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

// ut-docs#3317: delete_item {id} reaches the hook with the id only — the
// hook (the till's own data) decides whether the item may go; a refusal is
// the directive's failure text, verbatim.
func TestApplyDeleteItem(t *testing.T) {
	var gotID string
	hooks := Hooks{DeleteItem: func(ctx context.Context, id string) (string, error) {
		gotID = id
		if id == "sold" {
			return "", errors.New("sold 14 times — deactivate it instead")
		}
		return "deleted item Latte", nil
	}}
	if status, msg := apply(context.Background(), directive{Type: "delete_item", Payload: map[string]any{}}, hooks); status != "failed" || msg != "missing id" {
		t.Fatalf("missing id: %q %q", status, msg)
	}
	if status, msg := apply(context.Background(), directive{Type: "delete_item", Payload: map[string]any{"id": " i1 "}}, hooks); status != "applied" || msg != "deleted item Latte" || gotID != "i1" {
		t.Fatalf("delete: %q %q %q", status, msg, gotID)
	}
	if status, msg := apply(context.Background(), directive{Type: "delete_item", Payload: map[string]any{"id": "sold"}}, hooks); status != "failed" || msg != "sold 14 times — deactivate it instead" {
		t.Fatalf("refused: %q %q", status, msg)
	}
	if !mainTillOnlyTypes["delete_item"] || !catalogTypes["delete_item"] {
		t.Fatal("delete_item must be main-till only and re-push the catalog snapshot")
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

// ut-docs#3319: save_option_set mirrors save_modifier_group's payload
// conventions — id required, create/name/active presence-aware, and
// `values` a JSON-encoded array STRING of {id, value}, the full ordered
// replace (index = sort_order). A malformed field fails the directive.
func TestApplySaveOptionSet(t *testing.T) {
	var got data.OptionSetSave
	calls := 0
	hooks := Hooks{SaveOptionSet: func(ctx context.Context, p data.OptionSetSave) (string, error) {
		calls++
		got = p
		return "saved", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"name": "x"}, "missing id"},
		{map[string]any{"id": "  "}, "missing id"},
		{map[string]any{"id": "s1", "create": "perhaps"}, "bad create"},
		{map[string]any{"id": "s1", "name": 3.0}, "bad name"},
		{map[string]any{"id": "s1", "active": 2.0}, "bad active"},
		{map[string]any{"id": "s1", "values": "[{]"}, "bad values"},
		{map[string]any{"id": "s1", "values": `[{"id":"v1","value":5}]`}, "bad values"},
		{map[string]any{"id": "s1", "values": `null`}, "bad values"},
		{map[string]any{"id": "s1", "values": []any{map[string]any{"id": "v1", "value": "S"}}}, "bad values"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "save_option_set", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("a malformed payload reached the hook %d time(s)", calls)
	}
	status, msg := apply(context.Background(), directive{Type: "save_option_set", Payload: map[string]any{
		"id": " s1 ", "create": true, "name": " Size ", "active": "false",
		"values": `[{"id":" v-s ","value":" S "},{"id":"v-m","value":"M"}]`,
	}}, hooks)
	if status != "applied" || msg != "saved" {
		t.Fatalf("good payload: %q %q", status, msg)
	}
	if got.ID != "s1" || !got.Create || got.Name == nil || *got.Name != "Size" || got.Active == nil || *got.Active {
		t.Fatalf("decoded = %+v", got)
	}
	if got.Values == nil || !reflect.DeepEqual(*got.Values, []data.OptionSetValueInput{{ID: "v-s", Value: "S"}, {ID: "v-m", Value: "M"}}) {
		t.Fatalf("values = %+v", got.Values)
	}
	// Absent fields → nil (keep); an empty array is a real "clear every value".
	apply(context.Background(), directive{Type: "save_option_set", Payload: map[string]any{"id": "s1", "values": `[]`}}, hooks)
	if got.Create || got.Name != nil || got.Active != nil || got.Values == nil || len(*got.Values) != 0 {
		t.Fatalf("absent fields must be nil and [] an empty replace: %+v", got)
	}
	apply(context.Background(), directive{Type: "save_option_set", Payload: map[string]any{"id": "s1", "name": "Sizes"}}, hooks)
	if got.Values != nil {
		t.Fatalf("absent values must be nil (keep every value): %+v", got)
	}
}

func TestApplyDeleteOptionSet(t *testing.T) {
	var gotID string
	hooks := Hooks{DeleteOptionSet: func(ctx context.Context, id string) (string, error) {
		gotID = id
		if id == "used" {
			return "", errors.New("option set Size is used by 1 item: T-shirt")
		}
		return "deleted", nil
	}}
	if status, msg := apply(context.Background(), directive{Type: "delete_option_set", Payload: map[string]any{}}, hooks); status != "failed" || msg != "missing id" {
		t.Fatalf("missing id: %q %q", status, msg)
	}
	if status, _ := apply(context.Background(), directive{Type: "delete_option_set", Payload: map[string]any{"id": "s1"}}, hooks); status != "applied" || gotID != "s1" {
		t.Fatalf("delete: %q", gotID)
	}
	// The hook's error IS the failure text the cloud shows (the in-use refusal).
	if status, msg := apply(context.Background(), directive{Type: "delete_option_set", Payload: map[string]any{"id": "used"}}, hooks); status != "failed" || msg != "option set Size is used by 1 item: T-shirt" {
		t.Fatalf("in-use refusal: %q %q", status, msg)
	}
}

// Both option-set types are main-till only and re-push the catalog
// snapshot after they apply, like the modifier-group pair.
func TestOptionSetDirectiveTypesRegistered(t *testing.T) {
	for _, typ := range []string{"save_option_set", "delete_option_set"} {
		if !mainTillOnlyTypes[typ] {
			t.Errorf("%s missing from mainTillOnlyTypes", typ)
		}
		if !catalogTypes[typ] {
			t.Errorf("%s missing from catalogTypes", typ)
		}
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
		{"id": "d7", "type": "set_category_order", "payload": map[string]any{"category_ids": `["c1"]`}},
		{"id": "d8", "type": "save_option_set", "payload": map[string]any{"id": "s1"}},
		{"id": "d9", "type": "delete_option_set", "payload": map[string]any{"id": "s1"}},
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
		SetCategoryOrder:    func(context.Context, []string) (string, error) { ran++; return "", nil },
		SaveOptionSet:       func(context.Context, data.OptionSetSave) (string, error) { ran++; return "", nil },
		DeleteOptionSet:     func(context.Context, string) (string, error) { ran++; return "", nil },
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

// ut-docs#3039 (ADR-0053): fiscal_tse_ready is main-till only. A satellite
// that applied it would spend the single-use credential handoff on its own
// disk; one that failed it would resolve it before the main till saw it.
// It is skipped with no result post, and still applies on the main till.
func TestTickFiscalTSEReadyIsMainTillOnly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		primary   string
		wantRuns  int
		wantPosts int
	}{
		{"satellite", "http://10.0.0.2:8080", 0, 0},
		{"main till", "", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cloud := &fakeCloud{directives: []map[string]any{
				{"id": "d1", "type": "fiscal_tse_ready", "payload": map[string]any{}},
			}}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			db := testDB(t)
			if tc.primary != "" {
				if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', ?)`, tc.primary); err != nil {
					t.Fatal(err)
				}
			}
			ran := 0
			hooks := Hooks{FiscalTSEReady: func(context.Context) (string, error) { ran++; return "stored", nil }}
			if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
				t.Fatalf("tick: %v", err)
			}
			if ran != tc.wantRuns || len(cloud.results) != tc.wantPosts {
				t.Fatalf("hook runs = %d, result posts = %+v; want %d runs, %d posts", ran, cloud.results, tc.wantRuns, tc.wantPosts)
			}
		})
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
	if _, err := db.Exec(`UPDATE items SET age_restricted = 1 WHERE id = 'it-1'`); err != nil {
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
		"age_restricted", "barcode", "barcodes", "modifier_group_ids", "modifier_opt_out_ids", "effective_modifier_group_ids", "variants"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing %q: %v", k, row)
		}
	}
	if row["age_restricted"] != true {
		t.Fatalf("age_restricted = %v, want true", row["age_restricted"])
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

// §3.8 (ut-docs#3075): set_category_order carries the full ordered list as
// a JSON-encoded string field. Unlike set_quick_button_layout's lenient
// strs() decode, a malformed list, a blank id or a duplicate FAILS the
// directive visibly instead of being dropped or guessed at.
func TestApplySetCategoryOrder(t *testing.T) {
	if status, msg := apply(context.Background(), directive{Type: "set_category_order", Payload: map[string]any{"category_ids": `["c1"]`}}, Hooks{}); status != "failed" || msg != "set_category_order is not supported on this till" {
		t.Fatalf("nil hook: %q %q", status, msg)
	}
	var calls int
	var got []string
	hooks := Hooks{SetCategoryOrder: func(ctx context.Context, ids []string) (string, error) {
		calls++
		got = ids
		return "category order applied to 3 categories", nil
	}}
	for _, c := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{}, "missing category_ids"},
		{map[string]any{"category_ids": `[]`}, "missing category_ids"},
		{map[string]any{"category_ids": `null`}, "missing category_ids"},
		{map[string]any{"category_ids": ""}, "bad category_ids"},
		{map[string]any{"category_ids": "c1,c2"}, "bad category_ids"},
		{map[string]any{"category_ids": `[1,2]`}, "bad category_ids"},
		{map[string]any{"category_ids": `{"a":"b"}`}, "bad category_ids"},
		{map[string]any{"category_ids": []any{"c1", "c2"}}, "bad category_ids"},
		{map[string]any{"category_ids": 5.0}, "bad category_ids"},
		{map[string]any{"category_ids": `["c1"," ","c2"]`}, "blank category id"},
		{map[string]any{"category_ids": `["c1","c2"," c1 "]`}, "duplicate category id c1"},
	} {
		if status, msg := apply(context.Background(), directive{Type: "set_category_order", Payload: c.payload}, hooks); status != "failed" || msg != c.want {
			t.Errorf("%v: %q %q, want failed %q", c.payload, status, msg, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for refused payloads", calls)
	}
	status, msg := apply(context.Background(), directive{Type: "set_category_order", Payload: map[string]any{"category_ids": `[" c3 ","c1","c2"]`}}, hooks)
	if status != "applied" || msg != "category order applied to 3 categories" || !reflect.DeepEqual(got, []string{"c3", "c1", "c2"}) {
		t.Fatalf("applied: %q %q %v", status, msg, got)
	}
}

// §3.8: set_category_order is main-till only (a satellite leaves it
// pending) and a catalog type (an applied one re-pushes in the same tick).
func TestSetCategoryOrderIsMainTillOnlyCatalogType(t *testing.T) {
	if !mainTillOnlyTypes["set_category_order"] {
		t.Error("set_category_order not in mainTillOnlyTypes")
	}
	if !catalogTypes["set_category_order"] {
		t.Error("set_category_order not in catalogTypes")
	}
}

// ut-docs#3317: every snapshot row says whether the item was ever sold
// (itself or a variant), so my. can offer Delete only for a never-sold
// item. A first sale changes the snapshot, so it is pushed again.
func TestSnapshotReportsEverSold(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	d := openMigratedDB(t, "cloudsync.db")
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('es-sold','ES1','Sold',100,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('es-new','ES2','New',100,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	everSold := func(snap map[string]any) map[string]any {
		out := map[string]any{}
		for _, r := range snap["items"].([]any) {
			m := r.(map[string]any)
			out[m["id"].(string)] = m["ever_sold"]
		}
		return out
	}
	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), d.DB); err != nil {
		t.Fatal(err)
	}
	if got := everSold(cloud.snapshots[0]); got["es-sold"] != false || got["es-new"] != false {
		t.Fatalf("before any sale: %v / %v", got["es-sold"], got["es-new"])
	}
	for _, q := range []string{
		`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-es','R-es','completed','sale','GBP',100,0,0,100,datetime('now'))`,
		`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('l-es','s-es',1,'es-sold','Sold',1,100,0,0,0,100,100)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), d.DB); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2 (a first sale changes the snapshot)", len(cloud.snapshots))
	}
	if got := everSold(cloud.snapshots[1]); got["es-sold"] != true || got["es-new"] != false {
		t.Fatalf("after a sale: %v / %v", got["es-sold"], got["es-new"])
	}
}
