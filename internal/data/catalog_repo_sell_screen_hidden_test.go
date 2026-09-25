package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2541: items.sell_screen_hidden (migration 040) — an item hidden
// from the sell screen still sells via barcode scan/live search, but is
// left out of the quick-button grid and All tab. These tests cover the
// CatalogRepo layer directly (SetSellScreenHidden/ListSellScreenHidden/
// SellScreenStates); internal/ui's own tests cover ButtonStore.Load's
// consumption of the flag.
func newCatalogHiddenTestDB(t *testing.T) (*data.CatalogRepo, *db.DB) {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "catalog-hidden.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-a','SKU-A','Latte',320,1,0,'each')`); err != nil {
		t.Fatalf("seed item-a: %v", err)
	}
	return data.NewCatalogRepo(d.DB), d
}

func TestSetSellScreenHidden_RoundTrip(t *testing.T) {
	repo, _ := newCatalogHiddenTestDB(t)
	ctx := context.Background()

	hidden, _, err := repo.SellScreenStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hidden["item-a"] {
		t.Fatalf("expected item-a not hidden by default, got %+v", hidden)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", true); err != nil {
		t.Fatalf("hide: %v", err)
	}
	hidden, _, err = repo.SellScreenStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hidden["item-a"] {
		t.Fatalf("expected item-a hidden after SetSellScreenHidden(true), got %+v", hidden)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", false); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	hidden, _, err = repo.SellScreenStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hidden["item-a"] {
		t.Fatalf("expected item-a unhidden after SetSellScreenHidden(false), got %+v", hidden)
	}
}

func TestSetSellScreenHidden_RequiresItemID(t *testing.T) {
	repo, _ := newCatalogHiddenTestDB(t)
	if err := repo.SetSellScreenHidden(context.Background(), "  ", true); err == nil {
		t.Fatal("expected an error for a blank itemID")
	}
}

// TestSetSellScreenHidden_UnknownOrInactiveItemReturnsErrItemNotFound
// (ut-docs#2541 review finding 4): hide/unhide/delete-item used to accept
// an unknown or already-inactive item id silently -- the UPDATE just
// touched zero rows and returned no error, so the handler answered 200 and
// wrote an audit row for an action that never actually did anything.
func TestSetSellScreenHidden_UnknownOrInactiveItemReturnsErrItemNotFound(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()

	if err := repo.SetSellScreenHidden(ctx, "does-not-exist", true); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("unknown item: err = %v, want ErrItemNotFound", err)
	}

	if _, err := d.DB.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = 'item-a'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSellScreenHidden(ctx, "item-a", true); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("inactive item: err = %v, want ErrItemNotFound", err)
	}
}

// TestSetSellScreenHidden_KeepsShortcutRow (ut-docs#2698, reversing
// #2541's delete): hiding keeps the item's shortcut_buttons row, so the tile
// keeps its position -- it shows greyed in the same spot in edit mode and
// comes back there when unhidden. LoadButtons (the at-rest/cloud view) still
// leaves the hidden row out; LoadGridButtons returns it, marked Hidden.
func TestSetSellScreenHidden_KeepsShortcutRow(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('BTN-A','Latte','item-a',3)`); err != nil {
		t.Fatalf("seed shortcut row: %v", err)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", true); err != nil {
		t.Fatalf("hide: %v", err)
	}

	var n, sortOrder int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*), MAX(sort_order) FROM shortcut_buttons WHERE item_id = 'item-a'`).Scan(&n, &sortOrder); err != nil {
		t.Fatal(err)
	}
	if n != 1 || sortOrder != 3 {
		t.Fatalf("expected hiding to keep the item's shortcut_buttons row at sort_order 3, got count=%d sort_order=%d", n, sortOrder)
	}

	shortcuts := data.NewShortcutsRepo(d.DB)
	rest, err := shortcuts.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("LoadButtons (at rest) must leave a hidden item's row out, got %+v", rest)
	}
	grid, err := shortcuts.LoadGridButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(grid) != 1 || grid[0].ItemID != "item-a" || !grid[0].Hidden {
		t.Fatalf("LoadGridButtons must return the hidden row marked Hidden, got %+v", grid)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", false); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	grid, err = shortcuts.LoadGridButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(grid) != 1 || grid[0].Hidden {
		t.Fatalf("after unhide the same row must come back, not hidden, got %+v", grid)
	}
}

// TestRemoveFromSellScreen (ut-docs#2698): the trash badge's repo call sets
// sell_screen_removed, clears sell_screen_hidden (removed wins -- a removed
// item must not linger in the Designer's Hidden list) and deletes the
// item's shortcut_buttons row, in one transaction -- and NEVER deactivates
// the item: it keeps selling by scan/search.
func TestRemoveFromSellScreen(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('BTN-A','Latte','item-a',0)`,
		`UPDATE items SET sell_screen_hidden = 1 WHERE id = 'item-a'`,
	} {
		if _, err := d.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	if err := repo.RemoveFromSellScreen(ctx, "item-a"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var active, hidden, removed, rows int
	if err := d.DB.QueryRowContext(ctx, `SELECT is_active, sell_screen_hidden, sell_screen_removed FROM items WHERE id = 'item-a'`).Scan(&active, &hidden, &removed); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("remove must never deactivate the item, is_active=%d", active)
	}
	if removed != 1 || hidden != 0 {
		t.Fatalf("expected removed=1 hidden=0, got removed=%d hidden=%d", removed, hidden)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM shortcut_buttons WHERE item_id = 'item-a'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("expected the shortcut_buttons row deleted, %d left", rows)
	}
	hiddenIDs, removedIDs, err := repo.SellScreenStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hiddenIDs["item-a"] || !removedIDs["item-a"] {
		t.Fatalf("SellScreenStates: hidden=%v removed=%v", hiddenIDs, removedIDs)
	}
	listed, err := repo.ListSellScreenHidden(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("a removed item must not be listed as hidden, got %+v", listed)
	}
}

func TestRemoveFromSellScreen_UnknownOrInactiveItem(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-off','SKU-OFF','Old',100,0,0,'each')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"never-existed", "item-off"} {
		if err := repo.RemoveFromSellScreen(ctx, id); !errors.Is(err, data.ErrItemNotFound) {
			t.Fatalf("RemoveFromSellScreen(%q): want ErrItemNotFound, got %v", id, err)
		}
	}
	if err := repo.RemoveFromSellScreen(ctx, " "); err == nil {
		t.Fatal("expected an error for a blank itemID")
	}
}

func TestListSellScreenHidden_ActiveOnlyOrderedByName(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Bagel',280,1,0,'each')`); err != nil {
		t.Fatalf("seed item-b: %v", err)
	}
	// An inactive item hidden before deactivation must never surface in
	// the Designer's "Hidden from sell screen" list — that's the catalog
	// page's own concern, not this section's.
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit, sell_screen_hidden) VALUES ('item-c','SKU-C','Cake',500,0,0,'each',1)`); err != nil {
		t.Fatalf("seed item-c (inactive, hidden): %v", err)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-b", true); err != nil {
		t.Fatalf("hide item-b: %v", err)
	}
	if err := repo.SetSellScreenHidden(ctx, "item-a", true); err != nil {
		t.Fatalf("hide item-a: %v", err)
	}

	hidden, err := repo.ListSellScreenHidden(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 2 {
		t.Fatalf("expected 2 hidden active items (not the inactive one), got %+v", hidden)
	}
	if hidden[0].Name != "Bagel" || hidden[1].Name != "Latte" {
		t.Fatalf("expected alphabetical order (Bagel, Latte), got %+v", hidden)
	}
	if hidden[0].ItemID != "item-b" || hidden[1].ItemID != "item-a" {
		t.Fatalf("unexpected item ids: %+v", hidden)
	}
}

// TestUnhideAllSellScreen (ut-docs#2614): the Designer's one-click "Show
// all N on the sell screen" clears sell_screen_hidden for every ACTIVE
// hidden item in one UPDATE and reports how many it touched. An inactive
// hidden item is left alone (it isn't listed in the Designer section
// either), and no shortcut_buttons rows are created -- an unhidden item
// comes back as an implicit tile (ut-docs#2541), never as a duplicate or
// moved explicit button.
func TestUnhideAllSellScreen(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()

	if n, err := repo.UnhideAllSellScreen(ctx); err != nil || n != 0 {
		t.Fatalf("nothing hidden: got (%d, %v), want (0, nil)", n, err)
	}

	for _, stmt := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Mocha',350,1,0,'each')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-c','SKU-C','Retired',100,0,0,'each')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-d','SKU-D','Visible',100,1,0,'each')`,
		`UPDATE items SET sell_screen_hidden = 1 WHERE id IN ('item-a','item-b','item-c')`,
		// ut-docs#2698: hidden AND removed (a direct write; RemoveFromSellScreen
		// itself clears hidden) -- removed wins, so it is neither listed as
		// hidden nor counted by unhide-all.
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit, sell_screen_hidden, sell_screen_removed) VALUES ('item-e','SKU-E','Gone',100,1,0,'each',1,1)`,
	} {
		if _, err := d.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	var before int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM shortcut_buttons`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	n, err := repo.UnhideAllSellScreen(ctx)
	if err != nil {
		t.Fatalf("unhide all: %v", err)
	}
	if n != 2 {
		t.Fatalf("unhide all: n = %d, want 2 (the two active hidden items)", n)
	}
	hidden, _, err := repo.SellScreenStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hidden["item-a"] || hidden["item-b"] {
		t.Fatalf("expected active items unhidden, still hidden: %+v", hidden)
	}
	var inactiveHidden int
	if err := d.DB.QueryRowContext(ctx, `SELECT sell_screen_hidden FROM items WHERE id = 'item-c'`).Scan(&inactiveHidden); err != nil {
		t.Fatal(err)
	}
	if inactiveHidden != 1 {
		t.Fatalf("inactive hidden item must stay hidden, got sell_screen_hidden = %d", inactiveHidden)
	}
	var after int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM shortcut_buttons`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unhide all must not create shortcut_buttons rows: before %d, after %d", before, after)
	}

	if n, err := repo.UnhideAllSellScreen(ctx); err != nil || n != 0 {
		t.Fatalf("second call: got (%d, %v), want (0, nil)", n, err)
	}
}
