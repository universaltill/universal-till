package data

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// openDemoSeedTestDB opens a real migrated DB (post-036: no demo rows).
func openDemoSeedTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "demo-seed.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// ut-docs#539: the setup wizard's opt-in checkbox (and any later re-seed)
// inserts the full demo catalogue, every item flagged is_sample_data = 1.
func TestSeedDemoCatalogue(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)

	// Post-036 fresh install: nothing there yet.
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 0 {
		t.Fatalf("SampleItemCount before seed = %d, %v; want 0, nil", n, err)
	}

	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue: %v", err)
	}

	for table, want := range map[string]int{
		"items": 50, "categories": 10, "brands": 8, "item_variants": 12,
		"item_barcodes": 50, "variant_barcodes": 12, "inventory": 62,
		"price_history": 62, "shortcut_buttons": 10,
	} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("after seed: %s = %d rows, want %d", table, n, want)
		}
	}

	var unflagged int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE is_sample_data != 1`).Scan(&unflagged); err != nil {
		t.Fatal(err)
	}
	if unflagged != 0 {
		t.Errorf("%d seeded items are not flagged is_sample_data = 1", unflagged)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 50 {
		t.Fatalf("SampleItemCount after seed = %d, %v; want 50, nil", n, err)
	}

	// Idempotent: seeding again must neither fail nor duplicate.
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("second SeedDemoCatalogue: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Errorf("after re-seed: items = %d, want 50 (INSERT OR IGNORE must not duplicate)", n)
	}
}

// The Settings "Remove sample data" action: removes every untouched demo
// item and reports how many it could and couldn't remove — couldn't means
// already sold (directly or via a variant) or stock-adjusted, the same
// safety rule migration 036 applies on upgrade.
func TestRemoveDemoCatalogue(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Touch two items: itm001 sold, itm002 stock-adjusted.
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s-1', 'R-1', 120, 120)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sl-1', 's-1', 1, 'itm001', 'Coca-Cola Can 330ml', 1, 120, 2000, 20, 100, 120)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity)
		VALUES ('sm-1', 'itm002', NULL, 'loc_main', 'adjust', 3)`); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 48 || kept != 2 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 48, 2", removed, kept)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("items after removal = %d, want 2 (itm001 sold, itm002 adjusted)", n)
	}
	for _, id := range []string{"itm001", "itm002"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("touched item %s was removed", id)
		}
	}
	// The touched items' category and brands survive with them; everything
	// else demo is gone.
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("categories after removal = %d, want 1 (cat_drink)", n)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 2 {
		t.Fatalf("SampleItemCount after removal = %d, %v; want 2, nil", n, err)
	}

	// Running removal again is a no-op that reports the same kept count.
	removed, kept, err = repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("second RemoveDemoCatalogue: %v", err)
	}
	if removed != 0 || kept != 2 {
		t.Fatalf("second RemoveDemoCatalogue = removed %d, kept %d; want 0, 2", removed, kept)
	}
}

// Removing when nothing was ever seeded reports zeros, not an error.
func TestRemoveDemoCatalogueEmpty(t *testing.T) {
	d := openDemoSeedTestDB(t)
	removed, kept, err := NewDemoSeedRepo(d.DB).RemoveDemoCatalogue(context.Background())
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue on empty DB: %v", err)
	}
	if removed != 0 || kept != 0 {
		t.Fatalf("RemoveDemoCatalogue on empty DB = removed %d, kept %d; want 0, 0", removed, kept)
	}
}

// An operator's own item is never counted or touched by the sample-data
// paths, even if it clashes with nothing — only is_sample_data = 1 rows are.
func TestRemoveDemoCatalogueLeavesOwnItemsAlone(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if _, err := d.DB.Exec(`INSERT INTO items (id, name, base_price) VALUES ('own-1', 'My Own Item', 250)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatal(err)
	}
	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 50 || kept != 0 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 50, 0", removed, kept)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'own-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("operator's own item was removed by the sample-data cleanup")
	}
}
