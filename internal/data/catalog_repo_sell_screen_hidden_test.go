package data_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#2541: items.sell_screen_hidden (migration 039) — an item hidden
// from the sell screen still sells via barcode scan/live search, but is
// left out of the quick-button grid and All tab. These tests cover the
// CatalogRepo layer directly (SetSellScreenHidden/ListSellScreenHidden/
// SellScreenHiddenItemIDs); internal/ui's own tests cover ButtonStore.Load's
// consumption of the flag.
func newCatalogHiddenTestDB(t *testing.T) (*data.CatalogRepo, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "catalog-hidden.db"))
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

	hidden, err := repo.SellScreenHiddenItemIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hidden["item-a"] {
		t.Fatalf("expected item-a not hidden by default, got %+v", hidden)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", true); err != nil {
		t.Fatalf("hide: %v", err)
	}
	hidden, err = repo.SellScreenHiddenItemIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hidden["item-a"] {
		t.Fatalf("expected item-a hidden after SetSellScreenHidden(true), got %+v", hidden)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", false); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	hidden, err = repo.SellScreenHiddenItemIDs(ctx)
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

// TestSetSellScreenHidden_DeletesShortcutRows pins the design's explicit
// contract: hiding an item also deletes any shortcut_buttons row for it, so
// a Designer-configured explicit tile doesn't keep resolving even though
// its item is meant to be off the grid.
func TestSetSellScreenHidden_DeletesShortcutRows(t *testing.T) {
	repo, d := newCatalogHiddenTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('BTN-A','Latte','item-a',0)`); err != nil {
		t.Fatalf("seed shortcut row: %v", err)
	}

	if err := repo.SetSellScreenHidden(ctx, "item-a", true); err != nil {
		t.Fatalf("hide: %v", err)
	}

	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM shortcut_buttons WHERE item_id = 'item-a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected hiding to delete the item's shortcut_buttons row(s), got %d left", n)
	}

	// Unhiding does NOT resurrect the deleted row — the item just goes
	// back to being an IMPLICIT tile (ButtonStore.Load), not a
	// re-materialized explicit one.
	if err := repo.SetSellScreenHidden(ctx, "item-a", false); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM shortcut_buttons WHERE item_id = 'item-a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected unhide not to recreate a shortcut_buttons row, got %d", n)
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
