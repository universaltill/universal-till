package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func newShortcutsTestDB(t *testing.T) *data.ShortcutsRepo {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-a','SKU-A','Latte',320,1,0,'each')`); err != nil {
		t.Fatalf("seed item-a: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Cappuccino',350,0,0,'each')`); err != nil {
		t.Fatalf("seed item-b (inactive): %v", err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-c','SKU-C','Mocha',340,1,0,'each')`); err != nil {
		t.Fatalf("seed item-c: %v", err)
	}
	// db.Open runs the real migrations, which seed demo shortcut buttons for
	// the sample café/shop catalog — clear them so each test starts from a
	// deterministic, empty button list rather than asserting against
	// whatever the current seed data happens to contain.
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM shortcut_buttons`); err != nil {
		t.Fatalf("clear seeded shortcut buttons: %v", err)
	}
	return data.NewShortcutsRepo(d.DB)
}

func TestSaveButtons_ReplacesWholeSetInOrder(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B1", Label: "First", ItemID: "item-a"},
		{Barcode: "B2", Label: "Second", ItemID: "item-a", ImageURL: "/custom.png"},
	}); err != nil {
		t.Fatal(err)
	}

	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 {
		t.Fatalf("expected 2 buttons, got %d: %+v", len(btns), btns)
	}
	if btns[0].Label != "First" || btns[1].Label != "Second" {
		t.Fatalf("expected sort_order to follow list order, got %+v", btns)
	}
	if btns[1].ImageURL != "/custom.png" {
		t.Fatalf("expected explicit image preserved, got %q", btns[1].ImageURL)
	}

	// A second call must REPLACE the set, not append to it.
	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B3", Label: "Only", ItemID: "item-a"},
	}); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].Label != "Only" {
		t.Fatalf("expected SaveButtons to replace the whole set, got %+v", btns)
	}

	// Saving an empty list clears every button.
	if err := repo.SaveButtons(ctx, nil); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 0 {
		t.Fatalf("expected an empty button list, got %+v", btns)
	}
}

func TestUpdateOrder(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B1", Label: "Alpha", ItemID: "item-a"},
		{Barcode: "B2", Label: "Beta", ItemID: "item-a"},
		{Barcode: "B3", Label: "Gamma", ItemID: "item-a"},
	}); err != nil {
		t.Fatal(err)
	}

	// Reverse the order via drag&drop.
	if err := repo.UpdateOrder(ctx, []string{"B3", "B2", "B1"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 3 || btns[0].Label != "Gamma" || btns[1].Label != "Beta" || btns[2].Label != "Alpha" {
		t.Fatalf("expected reversed order Gamma,Beta,Alpha — got %+v", btns)
	}
}

func TestAddButton(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].Label != "Latte" {
		t.Fatalf("expected the new button, got %+v", btns)
	}

	// Re-adding the same barcode updates in place (ON CONFLICT), not a
	// duplicate row.
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte Renamed", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].Label != "Latte Renamed" {
		t.Fatalf("expected the existing button updated in place, got %+v", btns)
	}

	// Validation: blank fields rejected.
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "", Barcode: "B2", ItemID: "item-a"}); err == nil {
		t.Fatal("expected an error for a blank label")
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "", ItemID: "item-a"}); err == nil {
		t.Fatal("expected an error for a blank barcode")
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "B2", ItemID: ""}); err == nil {
		t.Fatal("expected an error for a blank item id")
	}

	// Unknown or inactive item rejected — a shortcut must point at something
	// actually sellable.
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "B2", ItemID: "does-not-exist"}); err == nil {
		t.Fatal("expected an error for an unknown item")
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "B2", ItemID: "item-b"}); err == nil {
		t.Fatal("expected an error for an inactive item")
	}
}

func TestAddButton_AppendsAtEndOfSortOrder(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "First", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Second", Barcode: "B2", ItemID: "item-c"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 || btns[0].Label != "First" || btns[1].Label != "Second" {
		t.Fatalf("expected new buttons appended after existing ones, got %+v", btns)
	}
}

// TestAddButton_ReusesTheItemsExistingRow (ut-docs#2698 review F1): an item
// that already has a row keeps that row -- its code and its position -- when
// added again under a different code (its resolvable tile code changed since
// the row was written); only the label/image are refreshed. Both
// sell-screen flags are cleared in the same transaction.
func TestAddButton_ReusesTheItemsExistingRow(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts-reuse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM shortcut_buttons`,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit, sell_screen_hidden, sell_screen_removed) VALUES ('item-a','SKU-A','Latte',320,1,0,'each',1,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-c','SKU-C','Mocha',340,1,0,'each')`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('SKU-C','Mocha','item-c',0)`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('SKU-A','Latte','item-a',1)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	repo := data.NewShortcutsRepo(d.DB)

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte Large", Barcode: "5012345678900", ItemID: "item-a", ImageURL: "/public/images/latte.png"}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.DB.QueryContext(ctx, `SELECT barcode, label, sort_order, COALESCE(image_path,'') FROM shortcut_buttons WHERE item_id='item-a'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		code, label string
		sort        int
		img         string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.code, &r.label, &r.sort, &r.img); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := row{"SKU-A", "Latte Large", 1, "/public/images/latte.png"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("want exactly one kept row %+v, got %+v", want, got)
	}
	var hidden, removed int
	if err := d.DB.QueryRowContext(ctx, `SELECT sell_screen_hidden, sell_screen_removed FROM items WHERE id='item-a'`).Scan(&hidden, &removed); err != nil {
		t.Fatal(err)
	}
	if hidden != 0 || removed != 0 {
		t.Fatalf("AddButton must clear both sell-screen flags, hidden=%d removed=%d", hidden, removed)
	}
}

func TestRemoveButton(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveButton(ctx, "B1"); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 0 {
		t.Fatalf("expected the button removed, got %+v", btns)
	}

	// Removing a code that was never a button is a no-op, not an error.
	if err := repo.RemoveButton(ctx, "never-existed"); err != nil {
		t.Fatalf("expected no error removing an unknown barcode, got %v", err)
	}
}

// TestLoadButtons_ExcludesHiddenItems (ut-docs#2541): an explicit
// shortcut_buttons row must never resolve as a tile once its item is
// hidden from the sell screen -- defense in depth alongside
// CatalogRepo.SetSellScreenHidden's own row delete (a row could in
// principle still exist if something set the flag directly, e.g. a future
// caller that bypasses that method). Uses CatalogRepo.SetSellScreenHidden
// itself would also delete the row, defeating the point of this test, so a
// direct UPDATE against the DB (not newShortcutsTestDB's repo-only handle)
// simulates that "flag set, row somehow still there" state instead.
func TestLoadButtons_ExcludesHiddenItems(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts-hidden.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-a','SKU-A','Latte',320,1,0,'each')`); err != nil {
		t.Fatalf("seed item-a: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM shortcut_buttons`); err != nil {
		t.Fatalf("clear seeded shortcut buttons: %v", err)
	}
	repo := data.NewShortcutsRepo(d.DB)

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 {
		t.Fatalf("expected the button before hiding, got %+v", btns)
	}

	if _, err := d.DB.ExecContext(ctx, `UPDATE items SET sell_screen_hidden = 1 WHERE id = 'item-a'`); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 0 {
		t.Fatalf("expected the hidden item's row excluded, got %+v", btns)
	}
}

// TestExistingBarcodes_ReportsWhichCodesHaveARow (ut-docs#2541):
// ButtonStore.UpdateOrder's own materialization step relies on this to
// tell an implicit tile (no row) apart from an explicit one.
func TestExistingBarcodes_ReportsWhichCodesHaveARow(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ExistingBarcodes(ctx, []string{"B1", "item:item-a", "does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["B1"] || got["item:item-a"] || got["does-not-exist"] {
		t.Fatalf("unexpected result: %+v", got)
	}

	empty, err := repo.ExistingBarcodes(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected an empty map for no codes, got %+v", empty)
	}
}

// TestMaterializeAndReorder_InsertsAndOrdersInOneTransaction (ut-docs#2541
// review finding 1): ButtonStore.UpdateOrder's repo-layer counterpart --
// batched existence check happens above this call (ExistingBarcodes), and
// this single call does every materializing INSERT plus every sort_order
// UPDATE in ONE transaction, rather than the pre-fix shape of one
// transaction per AddButton call (one per implicit tile) followed by a
// SEPARATE UpdateOrder transaction.
func TestMaterializeAndReorder_InsertsAndOrdersInOneTransaction(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Existing", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}

	err := repo.MaterializeAndReorder(ctx, []data.ShortcutButton{
		{Label: "", Barcode: "NEW1", ItemID: "item-a"},
	}, []string{"NEW1", "B1"})
	if err != nil {
		t.Fatalf("MaterializeAndReorder: %v", err)
	}

	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 {
		t.Fatalf("expected 2 buttons (1 existing + 1 newly materialized), got %d: %+v", len(btns), btns)
	}
	// sort_order follows the posted codes order: NEW1 first, B1 second.
	if btns[0].Barcode != "NEW1" || btns[1].Barcode != "B1" {
		t.Fatalf("expected order NEW1,B1 — got %+v", btns)
	}
}

// TestMaterializeAndReorder_EmptyMaterializeListOnlyReorders: a reorder with
// no implicit tiles to materialize (every code already has a row) still
// updates sort_order for all of them -- the materialize half being a no-op
// must not skip the reorder half.
func TestMaterializeAndReorder_EmptyMaterializeListOnlyReorders(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B1", Label: "Alpha", ItemID: "item-a"},
		{Barcode: "B2", Label: "Beta", ItemID: "item-a"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.MaterializeAndReorder(ctx, nil, []string{"B2", "B1"}); err != nil {
		t.Fatalf("MaterializeAndReorder: %v", err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 || btns[0].Label != "Beta" || btns[1].Label != "Alpha" {
		t.Fatalf("expected reversed order Beta,Alpha — got %+v", btns)
	}
}

// TestItemIDForBarcode_ResolvesOrReportsMiss (ut-docs#2541):
// ButtonStore.Remove's fallback path when only a code, not an itemId, is
// available (buttons_admin.html's legacy search flow).
func TestItemIDForBarcode_ResolvesOrReportsMiss(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}

	if id, ok := repo.ItemIDForBarcode(ctx, "B1"); !ok || id != "item-a" {
		t.Fatalf("ItemIDForBarcode(B1) = %q, %v, want item-a, true", id, ok)
	}
	if _, ok := repo.ItemIDForBarcode(ctx, "never-existed"); ok {
		t.Fatal("expected ok=false for an unknown code")
	}
}
