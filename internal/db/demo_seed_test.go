package db

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/universaltill/universal-till/internal/data/seeddata"
)

// ut-docs#539 / ut-docs#567: the demo catalogue and the demo customers/
// promotions are opt-in, never seeded at boot. The 001 baseline (ADR-0074)
// carries no demo rows at all, so on a brand-new install the operator never
// sees them — while the structural defaults (tax codes, stock locations)
// stay. (The upgrade-path tests that used to sit beside this one replayed
// the deleted 036/038 migrations against a re-seeded catalogue; they went
// with the ledger they replayed.)
func TestDemoCatalogueRemovedOnFreshInstall(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "demo-fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	// No demo catalogue — nothing else seeds these tables, so they must be
	// completely empty on a fresh install.
	for _, table := range []string{
		"items", "item_barcodes", "item_images", "item_variants",
		"variant_barcodes", "inventory", "price_history", "shortcut_buttons",
		"categories", "brands",
	} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("fresh install: %s has %d rows, want 0 (the demo catalogue is opt-in)", table, n)
		}
	}

	// Structural defaults are NOT demo data and must survive.
	for table, want := range map[string]int{"tax_codes": 3, "stock_locations": 3} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("fresh install: %s has %d rows, want %d (structural defaults must be seeded)", table, n, want)
		}
	}

	// Sample customers/promotions are opt-in too (ut-docs#567).
	for table, want := range map[string]int{"customers": 0, "promotions": 0} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("fresh install: %s has %d rows, want %d (demo customers/promos are opt-in)", table, n, want)
		}
	}

	// The is_sample_data column exists and defaults to 0 for new rows.
	if _, err := d.DB.Exec(`INSERT INTO items (id, name, base_price) VALUES ('own-1', 'Own Item', 100)`); err != nil {
		t.Fatalf("insert own item: %v", err)
	}
	if err := d.DB.QueryRow(`SELECT is_sample_data FROM items WHERE id = 'own-1'`).Scan(&n); err != nil {
		t.Fatalf("read is_sample_data: %v", err)
	}
	if n != 0 {
		t.Errorf("is_sample_data defaults to %d, want 0", n)
	}
}

// ut-docs#566: demo_seed_items' pristine sku/name/base_price reference
// values (in demo_ids.sql, used by remove_demo.sql's removal predicate)
// must never drift from demo_catalogue.sql — the single source of truth
// those values are a literal copy of. A drift here would silently break
// the pristine check: either a genuinely-untouched item stops being
// removable, or (worse) a renamed/repriced item is compared against the
// wrong reference values and gets deleted anyway.
func TestDemoSeedItemsPristineValuesMatchCatalogue(t *testing.T) {
	catalogueRow := regexp.MustCompile(`(?m)^\s*\('(itm\d{3})',\s*'([^']*)',\s*'((?:[^']|'')*)',\s*'[^']*',\s*'[a-z_]+',\s*'[a-z_]+',\s*'[a-z]+',\s*(\d+),`)
	pristineRow := regexp.MustCompile(`(?m)^\s*\('(itm\d{3})',\s*'([^']*)',\s*'((?:[^']|'')*)',\s*(\d+)\)`)

	type pristine struct{ sku, name, price string }
	catalogue := map[string]pristine{}
	for _, m := range catalogueRow.FindAllStringSubmatch(seeddata.DemoCatalogueSQL, -1) {
		catalogue[m[1]] = pristine{sku: m[2], name: m[3], price: m[4]}
	}
	if len(catalogue) != len(seeddata.ItemIDs) {
		t.Fatalf("parsed %d item rows out of demo_catalogue.sql, want %d — regex drifted from the file's shape", len(catalogue), len(seeddata.ItemIDs))
	}

	seeded := map[string]pristine{}
	for _, m := range pristineRow.FindAllStringSubmatch(seeddata.DemoIDsSQL, -1) {
		seeded[m[1]] = pristine{sku: m[2], name: m[3], price: m[4]}
	}
	if len(seeded) != len(seeddata.ItemIDs) {
		t.Fatalf("parsed %d pristine rows out of demo_ids.sql, want %d — regex drifted from the file's shape", len(seeded), len(seeddata.ItemIDs))
	}

	for _, id := range seeddata.ItemIDs {
		c, ok := catalogue[id]
		if !ok {
			t.Errorf("%s missing from demo_catalogue.sql's items INSERT", id)
			continue
		}
		s, ok := seeded[id]
		if !ok {
			t.Errorf("%s missing from demo_ids.sql's demo_seed_items INSERT", id)
			continue
		}
		if c != s {
			t.Errorf("%s pristine values drifted: demo_catalogue.sql has (sku=%q, name=%q, base_price=%s), demo_ids.sql has (sku=%q, name=%q, base_price=%s)",
				id, c.sku, c.name, c.price, s.sku, s.name, s.price)
		}
	}
}
