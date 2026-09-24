package data_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// Manage-shop catalog contract §3.1–§3.3 (ut-docs
// reference/manage-shop-catalog-api.md): the repository write paths behind
// the save_item, save_category and delete_category directives. Each one
// is a single transaction (all or nothing) and idempotent.

func boolp(b bool) *bool        { return &b }
func i64p(n int64) *int64       { return &n }
func intp(n int) *int           { return &n }
func sl(ids ...string) []string { return ids }

type saveFixture struct {
	d       *db.DB
	catalog *data.CatalogRepo
	mods    *data.ModifierRepo
	pos     *data.POSRepo
}

func newSaveFixture(t *testing.T) saveFixture {
	t.Helper()
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d) // cat1 Drinks; itm1 (cat1), itm-nocat, itm-anchor
	f := saveFixture{d: d, catalog: data.NewCatalogRepo(d.DB), mods: data.NewModifierRepo(d.DB), pos: data.NewPOSRepo(d.DB)}
	createAnchoredGroup(t, f.mods, "grp-milk", "Milk", 0)
	createAnchoredGroup(t, f.mods, "grp-size", "Size", 1)
	return f
}

func (f saveFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.d.DB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (f saveFixture) str(t *testing.T, q string, args ...any) string {
	t.Helper()
	var s string
	if err := f.d.DB.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (f saveFixture) col(t *testing.T, q string, args ...any) []string {
	t.Helper()
	rows, err := f.d.DB.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// ---- save_item ----

func TestSaveItem_CreateWithIDEveryField(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	res, err := f.catalog.SaveItem(ctx, data.ItemPatch{
		ID: "11111111-1111-4111-8111-111111111111", Create: true,
		Name: strp(" Oat latte "), PriceMinor: i64p(390), SKU: strp("HD-0007"),
		CategoryID: strp("cat1"), Color: strp("#b45309"),
		Barcodes: idsp("4006381333931", "ABC-1"), Active: boolp(true),
		IsWeighed: boolp(false), StockUntracked: boolp(true),
		ModifierGroupIDs: idsp("grp-size"), ModifierOptOutIDs: idsp("grp-milk"),
	})
	if err != nil || !res.Created || res.Name != "Oat latte" {
		t.Fatalf("create: res=%+v err=%v", res, err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	got := f.str(t, `SELECT name||'|'||sku||'|'||base_price||'|'||category_id||'|'||color||'|'||is_active||'|'||stock_untracked FROM items WHERE id = ?`, id)
	if got != "Oat latte|HD-0007|390|cat1|#b45309|1|1" {
		t.Fatalf("row = %q", got)
	}
	if bc := f.col(t, `SELECT barcode FROM item_barcodes WHERE item_id = ? ORDER BY is_primary DESC, barcode`, id); !reflect.DeepEqual(bc, sl("4006381333931", "ABC-1")) {
		t.Fatalf("barcodes = %v", bc)
	}
	if p := f.str(t, `SELECT barcode FROM item_barcodes WHERE item_id = ? AND is_primary = 1`, id); p != "4006381333931" {
		t.Fatalf("primary = %q, want the first barcode", p)
	}
	if g := f.col(t, `SELECT group_id FROM item_modifier_group_links WHERE item_id = ? ORDER BY sort_order`, id); !reflect.DeepEqual(g, sl("grp-size")) {
		t.Fatalf("links = %v", g)
	}
	if o := f.col(t, `SELECT group_id FROM item_modifier_group_opt_outs WHERE item_id = ?`, id); !reflect.DeepEqual(o, sl("grp-milk")) {
		t.Fatalf("opt-outs = %v", o)
	}

	// Replay (the result post was lost): same payload → same state, reported as an update.
	res, err = f.catalog.SaveItem(ctx, data.ItemPatch{
		ID: id, Create: true, Name: strp("Oat latte"), PriceMinor: i64p(390), SKU: strp("HD-0007"),
		Barcodes: idsp("4006381333931", "ABC-1"), ModifierGroupIDs: idsp("grp-size"),
	})
	if err != nil || res.Created {
		t.Fatalf("replay: res=%+v err=%v", res, err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM items WHERE id = ?`, id); n != "1" {
		t.Fatalf("replay duplicated the item: %s rows", n)
	}
	if bc := f.col(t, `SELECT barcode FROM item_barcodes WHERE item_id = ? ORDER BY is_primary DESC, barcode`, id); !reflect.DeepEqual(bc, sl("4006381333931", "ABC-1")) {
		t.Fatalf("replay barcodes = %v", bc)
	}
}

func TestSaveItem_CreateBlankSKUGeneratesOne(t *testing.T) {
	f := newSaveFixture(t)
	if _, err := f.catalog.SaveItem(context.Background(), data.ItemPatch{ID: "it-new", Create: true, Name: strp("Scone"), PriceMinor: i64p(250), SKU: strp("")}); err != nil {
		t.Fatal(err)
	}
	if sku := f.str(t, `SELECT COALESCE(sku, '') FROM items WHERE id = 'it-new'`); !strings.HasPrefix(sku, "ITEM-") || len(sku) != 13 {
		t.Fatalf("sku = %q, want a generated ITEM-XXXXXXXX (never an item without a SKU)", sku)
	}
}

// Review finding 1: a re-served create (its result post was lost) that
// carried sku:"" must apply as an update and keep the generated SKU, not
// fail "the sku must not be blank" (contract §3.1).
func TestSaveItem_ReplayCreateBlankSKUKeepsGenerated(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	p := data.ItemPatch{ID: "it-new", Create: true, Name: strp("Scone"), PriceMinor: i64p(250), SKU: strp("")}
	if _, err := f.catalog.SaveItem(ctx, p); err != nil {
		t.Fatal(err)
	}
	first := f.str(t, `SELECT COALESCE(sku, '') FROM items WHERE id = 'it-new'`)
	res, err := f.catalog.SaveItem(ctx, p)
	if err != nil {
		t.Fatalf("replayed create failed: %v", err)
	}
	if res.Created {
		t.Fatal("a replayed create must report an update")
	}
	if sku := f.str(t, `SELECT COALESCE(sku, '') FROM items WHERE id = 'it-new'`); sku != first {
		t.Fatalf("sku changed on replay: %q -> %q", first, sku)
	}
}

// Review finding 4: price_minor has the same upper bound as a modifier
// option's price delta.
func TestSaveItem_PriceUpperBound(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", PriceMinor: i64p(1_000_000_000)}); err == nil || !strings.Contains(err.Error(), "price") {
		t.Fatalf("err = %v, want a price bound error", err)
	}
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", PriceMinor: i64p(999_999_999)}); err != nil {
		t.Fatalf("the maximum price must be accepted: %v", err)
	}
}

func TestSaveItem_Validation(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `INSERT INTO categories (id, name, is_active) VALUES ('cat-off', 'Retired', 0)`)
	f.exec(t, `INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary) VALUES ('5000000000011', 'itm-nocat', 'EAN13', 1)`)
	for _, c := range []struct {
		name string
		p    data.ItemPatch
		want string
	}{
		{"missing, no create", data.ItemPatch{ID: "nope", Name: strp("x")}, "item not found"},
		{"create without name", data.ItemPatch{ID: "n1", Create: true, PriceMinor: i64p(1)}, "name"},
		{"create without price", data.ItemPatch{ID: "n1", Create: true, Name: strp("x")}, "price"},
		{"negative price", data.ItemPatch{ID: "itm1", PriceMinor: i64p(-1)}, "price"},
		{"blank name", data.ItemPatch{ID: "itm1", Name: strp("  ")}, "name"},
		{"blank sku on update", data.ItemPatch{ID: "itm1", SKU: strp("")}, "sku"},
		{"taken sku", data.ItemPatch{ID: "itm1", SKU: strp("SKU2")}, "SKU2"},
		{"bad colour", data.ItemPatch{ID: "itm1", Color: strp("#123456")}, "colour"},
		{"unknown category", data.ItemPatch{ID: "itm1", CategoryID: strp("cat-x")}, "category"},
		{"inactive category", data.ItemPatch{ID: "itm1", CategoryID: strp("cat-off")}, "category"},
		{"barcode used elsewhere", data.ItemPatch{ID: "itm1", Barcodes: idsp("5000000000011")}, "5000000000011"},
		{"duplicate barcode", data.ItemPatch{ID: "itm1", Barcodes: idsp("A1", "A1")}, "barcode"},
		{"non-printable barcode", data.ItemPatch{ID: "itm1", Barcodes: idsp("a b")}, "barcode"},
		{"unknown group", data.ItemPatch{ID: "itm1", ModifierGroupIDs: idsp("grp-x")}, "modifier group"},
		{"unknown opt-out group", data.ItemPatch{ID: "itm1", ModifierOptOutIDs: idsp("grp-x")}, "modifier group"},
	} {
		// Every refused save must leave the whole row as it was: the name
		// change riding along with the bad field is rolled back too.
		if !c.p.Create {
			c.p.Name = orKeep(c.p.Name, "Changed")
		}
		_, err := f.catalog.SaveItem(ctx, c.p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	if n := f.str(t, `SELECT name FROM items WHERE id = 'itm1'`); n != "Flat White" {
		t.Fatalf("a refused save changed the row: name = %q", n)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM items WHERE id = 'n1'`); n != "0" {
		t.Fatal("a refused create left a row behind")
	}
	var conflict *data.BarcodeConflictError
	_, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Barcodes: idsp("5000000000011")})
	if !errors.As(err, &conflict) || conflict.TargetID != "itm-nocat" {
		t.Fatalf("barcode conflict must stay a *BarcodeConflictError naming the owner, got %v", err)
	}
}

func orKeep(p *string, v string) *string {
	if p != nil {
		return p
	}
	return &v
}

func TestSaveItem_BarcodeSetReplaceAndActiveToggle(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary) VALUES ('OLD-1', 'itm1', 'CODE128', 1)`)
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Barcodes: idsp("NEW-2", "NEW-1")}); err != nil {
		t.Fatal(err)
	}
	if bc := f.col(t, `SELECT barcode FROM item_barcodes WHERE item_id = 'itm1' ORDER BY is_primary DESC, barcode`); !reflect.DeepEqual(bc, sl("NEW-2", "NEW-1")) {
		t.Fatalf("barcodes = %v, want the full new set with NEW-2 primary", bc)
	}
	// Deactivate, then reactivate (a path the till never had before).
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if a := f.str(t, `SELECT is_active FROM items WHERE id = 'itm1'`); a != "0" {
		t.Fatalf("deactivate: is_active = %s", a)
	}
	// Editing an inactive item works (the Inactive filter's inspector).
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Barcodes: idsp("NEW-3")}); err != nil {
		t.Fatalf("edit inactive item: %v", err)
	}
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if a := f.str(t, `SELECT is_active FROM items WHERE id = 'itm1'`); a != "1" {
		t.Fatalf("reactivate: is_active = %s", a)
	}
	// An empty list clears every item-level barcode.
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Barcodes: idsp()}); err != nil {
		t.Fatal(err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM item_barcodes WHERE item_id = 'itm1'`); n != "0" {
		t.Fatalf("clear barcodes left %s rows", n)
	}
}

func TestSaveItem_PriceChangeRecordsHistoryOnce(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // the replay must not append a second history row
		if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", PriceMinor: i64p(350)}); err != nil {
			t.Fatal(err)
		}
	}
	if p := f.str(t, `SELECT base_price FROM items WHERE id = 'itm1'`); p != "350" {
		t.Fatalf("base_price = %s", p)
	}
	prices, err := f.catalog.ItemCurrentPrices(ctx, []string{"itm1"})
	if err != nil || prices["itm1"] != 350 {
		t.Fatalf("resolved price = %v err=%v, want 350", prices, err)
	}
}

// ---- save_category ----

func TestSaveCategory_CreateMoveIconHidden(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	res, err := f.catalog.SaveCategory(ctx, data.CategorySave{
		ID: "cat-hot", Create: true, Name: strp("Hot drinks"), ParentID: strp("cat1"),
		Color: strp("#0f766e"), Icon: strp("lucide:coffee"), ShowOnSaleScreen: boolp(false),
		GroupIDs: idsp("grp-milk"),
	})
	if err != nil || !res.Created || res.Name != "Hot drinks" {
		t.Fatalf("create: %+v %v", res, err)
	}
	got := f.str(t, `SELECT name||'|'||parent_id||'|'||color||'|'||icon||'|'||sell_screen_hidden||'|'||is_active FROM categories WHERE id = 'cat-hot'`)
	if got != "Hot drinks|cat1|#0f766e|lucide:coffee|1|1" {
		t.Fatalf("row = %q", got)
	}
	if g := f.col(t, `SELECT group_id FROM category_modifier_group_links WHERE category_id = 'cat-hot'`); !reflect.DeepEqual(g, sl("grp-milk")) {
		t.Fatalf("links = %v", g)
	}
	// Replay: an update, same state.
	if res, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat-hot", Create: true, Name: strp("Hot drinks"), ParentID: strp("cat1")}); err != nil || res.Created {
		t.Fatalf("replay: %+v %v", res, err)
	}
	// Move to top level, clear icon, show again.
	if _, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat-hot", ParentID: strp(""), Icon: strp(""), ShowOnSaleScreen: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	got = f.str(t, `SELECT COALESCE(parent_id,'-')||'|'||COALESCE(icon,'-')||'|'||sell_screen_hidden FROM categories WHERE id = 'cat-hot'`)
	if got != "-|-|0" {
		t.Fatalf("after move/clear: %q", got)
	}
}

func TestSaveCategory_Validation(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	// cat1 > cat2 > cat3 (three levels, the maximum); cat-b is top level.
	f.exec(t, `INSERT INTO categories (id, name, parent_id) VALUES ('cat2', 'Hot', 'cat1'), ('cat3', 'Tea', 'cat2'), ('cat-b', 'Food', NULL)`)
	f.exec(t, `INSERT INTO categories (id, name, is_active) VALUES ('cat-off', 'Retired', 0)`)
	for _, c := range []struct {
		name string
		p    data.CategorySave
		want string
	}{
		{"missing, no create", data.CategorySave{ID: "nope", Name: strp("x")}, "category not found"},
		{"create without name", data.CategorySave{ID: "n1", Create: true}, "name"},
		{"duplicate name", data.CategorySave{ID: "cat-b", Name: strp("drinks")}, "a category named drinks already exists"},
		{"self parent", data.CategorySave{ID: "cat1", ParentID: strp("cat1")}, "inside itself"},
		{"cycle", data.CategorySave{ID: "cat1", ParentID: strp("cat3")}, "inside itself"},
		{"too deep", data.CategorySave{ID: "cat-b", ParentID: strp("cat3")}, "deeper than 3"},
		{"subtree too deep", data.CategorySave{ID: "cat1", ParentID: strp("cat-b")}, "deeper than 3"},
		{"unknown parent", data.CategorySave{ID: "cat-b", ParentID: strp("cat-x")}, "category"},
		{"inactive parent", data.CategorySave{ID: "cat-b", ParentID: strp("cat-off")}, "category"},
		{"bad colour", data.CategorySave{ID: "cat-b", Color: strp("red")}, "colour"},
		{"bad icon", data.CategorySave{ID: "cat-b", Icon: strp("<svg/>")}, "icon"},
		{"unknown group", data.CategorySave{ID: "cat-b", GroupIDs: idsp("grp-x")}, "modifier group"},
	} {
		if _, err := f.catalog.SaveCategory(ctx, c.p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	if n := f.str(t, `SELECT COUNT(*) FROM categories WHERE id = 'n1'`); n != "0" {
		t.Fatal("a refused create left a row behind")
	}
	// A move that keeps the depth at 3 is fine: cat-b (no children) under cat2.
	if _, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat-b", ParentID: strp("cat2")}); err != nil {
		t.Fatalf("depth-3 move refused: %v", err)
	}
}

func TestListCategoriesCarriesIconAndHidden(t *testing.T) {
	f := newSaveFixture(t)
	f.exec(t, `UPDATE categories SET icon = 'lucide:coffee', sell_screen_hidden = 1 WHERE id = 'cat1'`)
	for _, list := range []func(context.Context) ([]data.CategoryNode, error){f.catalog.ListCategories, f.catalog.ListActiveCategories} {
		cats, err := list(context.Background())
		if err != nil || len(cats) != 1 || cats[0].Icon != "lucide:coffee" || !cats[0].SellScreenHidden {
			t.Fatalf("cats = %+v err=%v", cats, err)
		}
	}
	rows, err := f.catalog.ListCategoriesForAdmin(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Icon != "lucide:coffee" || !rows[0].SellScreenHidden {
		t.Fatalf("admin rows = %+v err=%v", rows, err)
	}
}

// ---- delete_category ----

func TestDeleteCategoryMoving(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	// cat0 > cat1 > cat-kid; cat-food elsewhere. itm1 (active) and itm-off
	// (inactive) sit in cat1.
	f.exec(t, `INSERT INTO categories (id, name) VALUES ('cat0', 'Menu'), ('cat-food', 'Food')`)
	f.exec(t, `UPDATE categories SET parent_id = 'cat0' WHERE id = 'cat1'`)
	f.exec(t, `INSERT INTO categories (id, name, parent_id) VALUES ('cat-kid', 'Hot', 'cat1')`)
	f.exec(t, `INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm-off','SKU-OFF','Old',100,0,'cat1')`)
	if err := f.mods.SetCategoryModifierGroups(ctx, "cat1", []string{"grp-milk"}); err != nil {
		t.Fatal(err)
	}
	st, err := f.pos.CreateKitchenStation(ctx, "Bar", "printer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pos.SetCategoryStationRoutes(ctx, "cat1", []string{st}); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"cat1", "cat-kid", "cat-missing"} {
		if _, err := f.catalog.DeleteCategoryMoving(ctx, "cat1", bad); err == nil {
			t.Errorf("move_items_to=%s must fail", bad)
		}
	}
	if a := f.str(t, `SELECT is_active FROM categories WHERE id = 'cat1'`); a != "1" {
		t.Fatal("a refused delete changed the category")
	}

	res, err := f.catalog.DeleteCategoryMoving(ctx, "cat1", "cat-food")
	if err != nil || res.AlreadyDeleted || res.MovedItems != 2 || res.MovedChildren != 1 {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if got := f.col(t, `SELECT category_id FROM items WHERE id IN ('itm1','itm-off') ORDER BY id`); !reflect.DeepEqual(got, sl("cat-food", "cat-food")) {
		t.Fatalf("items moved to %v", got)
	}
	if p := f.str(t, `SELECT parent_id FROM categories WHERE id = 'cat-kid'`); p != "cat0" {
		t.Fatalf("child re-parented to %q, want the deleted category's own parent cat0", p)
	}
	if n := f.str(t, `SELECT (SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id='cat1') + (SELECT COUNT(*) FROM category_station_routes WHERE category_id='cat1')`); n != "0" {
		t.Fatalf("links left behind: %s", n)
	}
	if a := f.str(t, `SELECT is_active FROM categories WHERE id = 'cat1'`); a != "0" {
		t.Fatal("category not soft-deleted")
	}
	// Replay: already deleted, applied, nothing moves.
	res, err = f.catalog.DeleteCategoryMoving(ctx, "cat1", "")
	if err != nil || !res.AlreadyDeleted {
		t.Fatalf("replay: %+v %v", res, err)
	}
	if got := f.str(t, `SELECT category_id FROM items WHERE id = 'itm1'`); got != "cat-food" {
		t.Fatalf("replay moved items again: %q", got)
	}
	if _, err := f.catalog.DeleteCategoryMoving(ctx, "cat-nope", ""); !errors.Is(err, data.ErrCategoryNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	// Default target: uncategorised.
	if _, err := f.catalog.DeleteCategoryMoving(ctx, "cat-food", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT COALESCE(category_id, '-') FROM items WHERE id = 'itm1'`); got != "-" {
		t.Fatalf("item not moved to uncategorised: %q", got)
	}
}

// ---- save_modifier_group / delete_modifier_group ----

func TestSaveModifierGroup_CreateOptionsAttach(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	opts := []data.ModifierOption{
		{ID: "o-small", Name: "Small", PriceDeltaMinor: 0, IsActive: true},
		{ID: "o-large", Name: "Large", PriceDeltaMinor: 50, IsActive: true},
	}
	res, err := f.mods.SaveGroup(ctx, data.ModifierGroupSave{
		ID: "grp-new", Create: true, Name: strp("Cup"), Required: boolp(true), MinSelect: intp(1), MaxSelect: intp(1),
		Options: &opts, AttachCategoryIDs: sl("cat1"), AttachItemIDs: sl("itm-nocat"),
	})
	if err != nil || !res.Created || res.Name != "Cup" {
		t.Fatalf("create: %+v %v", res, err)
	}
	if got := f.col(t, `SELECT id||':'||name||':'||price_delta_minor||':'||sort_order FROM item_modifier_options WHERE group_id='grp-new' ORDER BY sort_order`); !reflect.DeepEqual(got, sl("o-small:Small:0:0", "o-large:Large:50:1")) {
		t.Fatalf("options = %v", got)
	}
	if n := f.str(t, `SELECT (SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id='grp-new' AND category_id='cat1') || (SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id='grp-new' AND item_id='itm-nocat')`); n != "11" {
		t.Fatalf("attachments = %s", n)
	}
	// Replay is idempotent.
	if res, err := f.mods.SaveGroup(ctx, data.ModifierGroupSave{ID: "grp-new", Create: true, Name: strp("Cup"), Options: &opts, AttachCategoryIDs: sl("cat1")}); err != nil || res.Created {
		t.Fatalf("replay: %+v %v", res, err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM item_modifier_options WHERE group_id='grp-new'`); n != "2" {
		t.Fatalf("replay duplicated options: %s", n)
	}

	// Full ordered set: reorder, rename one, drop one, add one, deactivate one.
	opts2 := []data.ModifierOption{
		{ID: "o-large", Name: "Large cup", PriceDeltaMinor: 60, IsActive: false},
		{ID: "o-xl", Name: "XL", PriceDeltaMinor: 90, IsActive: true},
	}
	if _, err := f.mods.SaveGroup(ctx, data.ModifierGroupSave{ID: "grp-new", Options: &opts2, DetachCategoryIDs: sl("cat1"), DetachItemIDs: sl("itm-nocat")}); err != nil {
		t.Fatal(err)
	}
	if got := f.col(t, `SELECT id||':'||name||':'||price_delta_minor||':'||sort_order||':'||is_active FROM item_modifier_options WHERE group_id='grp-new' ORDER BY sort_order`); !reflect.DeepEqual(got, sl("o-large:Large cup:60:0:0", "o-xl:XL:90:1:1")) {
		t.Fatalf("options after full-set save = %v", got)
	}
	if n := f.str(t, `SELECT (SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id='grp-new') + (SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id='grp-new')`); n != "0" {
		t.Fatalf("detach left %s links", n)
	}
}

func TestSaveModifierGroup_Validation(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	opt := func(id, name string, delta int64) *[]data.ModifierOption {
		o := []data.ModifierOption{{ID: id, Name: name, PriceDeltaMinor: delta, IsActive: true}}
		return &o
	}
	for _, c := range []struct {
		name string
		p    data.ModifierGroupSave
		want string
	}{
		{"missing, no create", data.ModifierGroupSave{ID: "nope", Name: strp("x")}, "modifier group not found"},
		{"create without name", data.ModifierGroupSave{ID: "g1", Create: true}, "name"},
		{"duplicate name", data.ModifierGroupSave{ID: "grp-size", Name: strp("MILK")}, "a modifier group named MILK already exists"},
		{"min > max", data.ModifierGroupSave{ID: "grp-size", MinSelect: intp(3), MaxSelect: intp(2)}, "choose"},
		{"max 0", data.ModifierGroupSave{ID: "grp-size", MaxSelect: intp(0)}, "choose"},
		{"max > 50", data.ModifierGroupSave{ID: "grp-size", MaxSelect: intp(51)}, "choose"},
		{"required with min 0", data.ModifierGroupSave{ID: "grp-size", Required: boolp(true), MinSelect: intp(0)}, "required"},
		{"negative delta", data.ModifierGroupSave{ID: "grp-size", Options: opt("o1", "A", -1)}, "option"},
		{"blank option name", data.ModifierGroupSave{ID: "grp-size", Options: opt("o1", " ", 0)}, "option"},
		{"option without id", data.ModifierGroupSave{ID: "grp-size", Options: opt("", "A", 0)}, "option"},
		{"option of another group", data.ModifierGroupSave{ID: "grp-size", Options: opt("opt-grp-milk", "A", 0)}, "option"},
		{"unknown category", data.ModifierGroupSave{ID: "grp-size", AttachCategoryIDs: sl("cat-x")}, "category"},
		{"unknown item", data.ModifierGroupSave{ID: "grp-size", AttachItemIDs: sl("itm-x")}, "item"},
		{"attach and detach", data.ModifierGroupSave{ID: "grp-size", AttachItemIDs: sl("itm1"), DetachItemIDs: sl("itm1")}, "both"},
	} {
		if _, err := f.mods.SaveGroup(ctx, c.p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	if n := f.str(t, `SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g1'`); n != "0" {
		t.Fatal("a refused create left a row behind")
	}
}

func TestDeleteGroupIfExists(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	found, err := f.mods.DeleteGroupIfExists(ctx, "grp-milk")
	if err != nil || !found {
		t.Fatalf("delete: found=%v err=%v", found, err)
	}
	found, err = f.mods.DeleteGroupIfExists(ctx, "grp-milk")
	if err != nil || found {
		t.Fatalf("replay: found=%v err=%v, want not found (already deleted)", found, err)
	}
}

// A stock-untracked item created from the cloud never gets the inventory
// placeholder row a tracked one does (ut-docs#1850's rule for CreateItem).
func TestSaveItem_CreateUntrackedHasNoInventoryRow(t *testing.T) {
	f := newSaveFixture(t)
	if _, err := f.catalog.SaveItem(context.Background(), data.ItemPatch{ID: "it-u", Create: true, Name: strp("Service"), PriceMinor: i64p(500), StockUntracked: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM inventory WHERE item_id = 'it-u'`); n != "0" {
		t.Fatalf("inventory rows = %s, want 0 for a stock-untracked item", n)
	}
}
