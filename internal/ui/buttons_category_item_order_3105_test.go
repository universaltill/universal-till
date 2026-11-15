package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// ut-docs#3105 (contract §3.14): set_category_item_order deals one
// category's items into the sale-screen slots that category's tiles already
// hold, so no other category's tile moves.

func TestRedealIntoSlots(t *testing.T) {
	btns := func(codes ...string) []Button {
		out := make([]Button, len(codes))
		for i, c := range codes {
			out[i] = Button{Code: c}
		}
		return out
	}
	for _, c := range []struct {
		name    string
		current []Button
		subset  []string
		want    []string
	}{
		{"reverse into own slots", btns("A1", "B1", "A2", "B2", "A3"), []string{"A3", "A2", "A1"}, []string{"A3", "B1", "A2", "B2", "A1"}},
		{"one code keeps its slot", btns("A1", "B1", "A2"), []string{"A2"}, []string{"A1", "B1", "A2"}},
		{"unknown and duplicate codes ignored", btns("A1", "B1", "A2"), []string{"A2", "nope", "A1", "A2"}, []string{"A2", "B1", "A1"}},
		{"nothing known", btns("A1", "B1"), []string{"nope"}, nil},
	} {
		if got := redealIntoSlots(c.current, c.subset); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// categoryCodes is the category's direct tile codes in Load() order, on
// seedInterleavedCategories' catalogue (codes SA1 SB1 SA2 SB2 SA3, all
// implicit): Alpha = Apple(a1) Cherry(a2) Egg(a3).
func categoryCodes(t *testing.T, store *ButtonStore, cat string) []string {
	t.Helper()
	btns, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range btns {
		if b.CategoryID == cat {
			out = append(out, b.Code)
		}
	}
	return out
}

func TestUpdateCategoryOrder_MixedExplicitAndImplicitKeepsOtherSlots(t *testing.T) {
	_, store, order := seedInterleavedCategories(t)
	ctx := context.Background()
	// Make a2 explicit (a real shortcut_buttons row) so the category mixes both kinds.
	if err := store.UpdateOrder(ctx, []string{"SA1", "SB1", "SA2"}); err != nil {
		t.Fatal(err)
	}
	other := func() string {
		btns, _ := store.Load()
		var s []string
		for i, b := range btns {
			if b.CategoryID != "cat_a" {
				s = append(s, b.Code+"@"+string(rune('0'+i)))
			}
		}
		return strings.Join(s, ",")
	}
	beforeOther := other()
	tiles, changed, err := store.UpdateCategoryOrder(ctx, "cat_a", []string{"a3", "a2", "a1"})
	if err != nil || !changed || tiles != 3 {
		t.Fatalf("tiles=%d changed=%v err=%v", tiles, changed, err)
	}
	if got := strings.Join(categoryCodes(t, store, "cat_a"), ","); got != "SA3,SA2,SA1" {
		t.Fatalf("category order = %s, want SA3,SA2,SA1", got)
	}
	if got := other(); got != beforeOther {
		t.Fatalf("other category moved: %s -> %s", beforeOther, got)
	}
	if got := strings.Join(order(), ","); got != "SA3,SB1,SA2,SB2,SA1" {
		t.Fatalf("global order = %s", got)
	}
}

func TestUpdateCategoryOrder_UnlistedTilesFollowInOldOrder(t *testing.T) {
	_, store, order := seedInterleavedCategories(t)
	tiles, changed, err := store.UpdateCategoryOrder(context.Background(), "cat_a", []string{"a3"})
	if err != nil || !changed || tiles != 1 {
		t.Fatalf("tiles=%d changed=%v err=%v", tiles, changed, err)
	}
	// a3 first, then the unlisted a1, a2 in their old order.
	if got := strings.Join(categoryCodes(t, store, "cat_a"), ","); got != "SA3,SA1,SA2" {
		t.Fatalf("category order = %s, want SA3,SA1,SA2", got)
	}
	if got := strings.Join(order(), ","); got != "SA3,SB1,SA1,SB2,SA2" {
		t.Fatalf("global order = %s", got)
	}
}

func TestUpdateCategoryOrder_HiddenItemOrderedInItsSlot(t *testing.T) {
	db, store, _ := seedInterleavedCategories(t)
	mustExec(t, db, `UPDATE items SET sell_screen_hidden = 1 WHERE id = 'a1'`)
	tiles, changed, err := store.UpdateCategoryOrder(context.Background(), "cat_a", []string{"a1", "a3", "a2"})
	if err != nil || !changed || tiles != 3 {
		t.Fatalf("tiles=%d changed=%v err=%v", tiles, changed, err)
	}
	btns, _ := store.Load()
	var got []string
	for _, b := range btns {
		if b.CategoryID == "cat_a" {
			got = append(got, b.ItemID)
		}
	}
	// a1 is hidden but sits in the grid: it was first and stays first.
	if strings.Join(got, ",") != "a1,a3,a2" {
		t.Fatalf("category item order = %v", got)
	}
}

func TestUpdateCategoryOrder_ItemWithoutTileSkipped(t *testing.T) {
	db, store, _ := seedInterleavedCategories(t)
	mustExec(t, db, `UPDATE items SET sell_screen_removed = 1 WHERE id = 'a2'`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES ('a4','SA4','Inactive',100,'cat_a',0)`)
	tiles, changed, err := store.UpdateCategoryOrder(context.Background(), "cat_a", []string{"a4", "a3", "a2", "a1"})
	if err != nil || !changed || tiles != 2 {
		t.Fatalf("tiles=%d changed=%v err=%v, want 2 tiles ordered (removed/inactive skipped)", tiles, changed, err)
	}
	if got := strings.Join(categoryCodes(t, store, "cat_a"), ","); got != "SA3,SA1" {
		t.Fatalf("category order = %s, want SA3,SA1", got)
	}
}

func TestUpdateCategoryOrder_NoTileIsError(t *testing.T) {
	db, store, order := seedInterleavedCategories(t)
	mustExec(t, db, `UPDATE items SET sell_screen_removed = 1 WHERE id = 'a2'`)
	_, changed, err := store.UpdateCategoryOrder(context.Background(), "cat_a", []string{"a2"})
	if err == nil || err.Error() != "none of these items has a sale-screen tile" || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := strings.Join(order(), ","); got != "SA1,SB1,SB2,SA3" {
		t.Fatalf("order changed by a refused reorder: %s", got)
	}
}

func TestUpdateCategoryOrder_IdempotentWritesNothing(t *testing.T) {
	db, store, _ := seedInterleavedCategories(t)
	ctx := context.Background()
	if _, changed, err := store.UpdateCategoryOrder(ctx, "cat_a", []string{"a3", "a1", "a2"}); err != nil || !changed {
		t.Fatalf("first: changed=%v err=%v", changed, err)
	}
	var before, after int
	count := func(dst *int) {
		if err := db.QueryRow(`SELECT COALESCE(SUM(sort_order),0)*1000 + COUNT(*) FROM shortcut_buttons`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	count(&before)
	tiles, changed, err := store.UpdateCategoryOrder(ctx, "cat_a", []string{"a3", "a1", "a2"})
	if err != nil || changed || tiles != 3 {
		t.Fatalf("replay: tiles=%d changed=%v err=%v", tiles, changed, err)
	}
	count(&after)
	if before != after {
		t.Fatalf("a no-op replay wrote shortcut_buttons: %d -> %d", before, after)
	}
}

func TestUpdateCategoryOrder_ImplicitReplayDoesNotMaterialise(t *testing.T) {
	db, store, _ := seedInterleavedCategories(t)
	// Already in order: nothing to change, so no implicit tile becomes a row.
	_, changed, err := store.UpdateCategoryOrder(context.Background(), "cat_a", []string{"a1", "a2", "a3"})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM shortcut_buttons`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("shortcut_buttons rows = %d, err=%v", n, err)
	}
}

// TilePositions backs the snapshot's tile_pos (§3.7): the index of each
// item's first tile in Load()'s grid order.
func TestTilePositions_MatchLoadGridOrder(t *testing.T) {
	db, store, _ := seedInterleavedCategories(t)
	ctx := context.Background()
	mustExec(t, db, `UPDATE items SET sell_screen_hidden = 1 WHERE id = 'b1'`)
	mustExec(t, db, `UPDATE items SET sell_screen_removed = 1 WHERE id = 'b2'`)
	if err := store.UpdateOrder(ctx, []string{"SA3", "SA1"}); err != nil { // two explicit rows lead
		t.Fatal(err)
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for i, b := range btns {
		if _, dup := want[b.ItemID]; !dup {
			want[b.ItemID] = i
		}
	}
	got, err := store.TilePositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TilePositions = %v, want %v", got, want)
	}
	if _, ok := got["b2"]; ok {
		t.Fatal("a removed item has no tile position")
	}
	if _, ok := got["b1"]; !ok {
		t.Fatal("a hidden item keeps its slot")
	}
}

// A tile code shared by two tiles (duplicate SKUs are allowed) can't be
// placed by slot: redealIntoSlots only knows a code's first slot, so it
// would move another category's tile. The order is refused, nothing written.
func TestUpdateCategoryOrder_SharedTileCodeRefused(t *testing.T) {
	db, store, order := seedInterleavedCategories(t)
	mustExec(t, db, `UPDATE items SET sku='SB1' WHERE id='a3'`)
	before := strings.Join(order(), ",")
	_, changed, err := store.UpdateCategoryOrder(context.Background(), "cat_a", []string{"a3", "a2", "a1"})
	if !errors.Is(err, ErrSharedTileCode) || changed {
		t.Fatalf("changed=%v err=%v, want ErrSharedTileCode", changed, err)
	}
	if after := strings.Join(order(), ","); after != before {
		t.Fatalf("grid changed on a refusal: %s -> %s", before, after)
	}
}
