package data

// ut-docs#2119 — /inventory's category-filter chip row needs each stock row
// to carry its item's category id (mirroring catalog_row.html's existing
// data-category, ut-docs#1951) so the client-side filter can match a row
// against the selected/expanded category set. ListStockLevels previously
// dropped category_id entirely; this pins it on both the item-scoped and
// the variant-scoped query path (ut-docs#2082's additive-row shape).

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openCat2119DB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "cat2119.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

func TestListStockLevels_CarriesCategoryID(t *testing.T) {
	d, repo := openCat2119DB(t)
	ctx := context.Background()

	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('cat-2119-drinks', 'Drinks')`)
	// Categorized item-level row.
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES
		('i-2119-cat', 'sku-2119-cat', '2119 Categorized', 100, 'cat-2119-drinks', 1)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES
		('inv-2119-cat', 'i-2119-cat', NULL, 'loc_main', 5)`)
	// Uncategorized item-level row — must come back with an EMPTY
	// CategoryID, not NULL/error (COALESCE in the query).
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES
		('i-2119-none', 'sku-2119-none', '2119 Uncategorized', 100, 1)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES
		('inv-2119-none', 'i-2119-none', NULL, 'loc_main', 5)`)
	// Variant-scoped row: category lives on the PARENT item, must still
	// come through on the variant's own row (ut-docs#2082 additive shape).
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES
		('i-2119-parent', 'sku-2119-parent', '2119 Variant Parent', 100, 'cat-2119-drinks', 1)`)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES
		('v-2119', 'i-2119-parent', '330ml', 150)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES
		('inv-2119-variant', NULL, 'v-2119', 'loc_main', 6)`)

	levels, err := repo.ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("list stock levels: %v", err)
	}

	var categorized, uncategorized, variant *LowStockItem
	for i, l := range levels {
		switch {
		case l.ItemID == "i-2119-cat":
			categorized = &levels[i]
		case l.ItemID == "i-2119-none":
			uncategorized = &levels[i]
		case l.VariantID == "v-2119":
			variant = &levels[i]
		}
	}

	if categorized == nil {
		t.Fatalf("categorized item row not found in %+v", levels)
	}
	if categorized.CategoryID != "cat-2119-drinks" {
		t.Fatalf("categorized item CategoryID = %q, want cat-2119-drinks", categorized.CategoryID)
	}

	if uncategorized == nil {
		t.Fatalf("uncategorized item row not found in %+v", levels)
	}
	if uncategorized.CategoryID != "" {
		t.Fatalf("uncategorized item CategoryID = %q, want empty string", uncategorized.CategoryID)
	}

	if variant == nil {
		t.Fatalf("variant-scoped row not found in %+v", levels)
	}
	if variant.CategoryID != "cat-2119-drinks" {
		t.Fatalf("variant row CategoryID = %q, want parent's cat-2119-drinks", variant.CategoryID)
	}
}
