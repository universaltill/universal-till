package db

import (
	"path/filepath"
	"regexp"
	"strings"
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

// ut-docs#1425 review finding F4: the migration-specific cross-check this
// replaces (TestMigration038MatchesSeedData) read migration 038's SQL text
// directly and was deleted along with it in the ADR-0074 squash — but the
// seeddata.DemoCustomerIDs/DemoPromoCodes vs.
// DemoCustomersPromosIDsSQL/DemoCustomersPromosSQL drift it guarded against
// has nothing to do with migration files and still needs guarding: a
// customer/promo present in the insert script but missing from the ID list
// would seed fine (DemoCustomersPromosSQL) but never be recognised as
// removable (RemoveDemoCustomersPromosSQL only targets rows in the TEMP ID
// tables demo_customers_promos_ids.sql populates) — a permanently
// unremovable "sample" customer. Mirrors
// TestDemoSeedItemsPristineValuesMatchCatalogue's item-side both-directions
// shape above.
func TestDemoCustomersPromosIDsMatchSeedData(t *testing.T) {
	for _, id := range seeddata.DemoCustomerIDs {
		if !strings.Contains(seeddata.DemoCustomersPromosIDsSQL, "('"+id+"')") {
			t.Errorf("seeddata.DemoCustomerIDs has %s but demo_customers_promos_ids.sql does not list it", id)
		}
		if !strings.Contains(seeddata.DemoCustomersPromosSQL, "'"+id+"'") {
			t.Errorf("seeddata.DemoCustomerIDs has %s but demo_customers_promos.sql does not insert it", id)
		}
	}
	for _, code := range seeddata.DemoPromoCodes {
		if !strings.Contains(seeddata.DemoCustomersPromosIDsSQL, "('"+code+"')") {
			t.Errorf("seeddata.DemoPromoCodes has %s but demo_customers_promos_ids.sql does not list it", code)
		}
		if !strings.Contains(seeddata.DemoCustomersPromosSQL, "'"+code+"'") {
			t.Errorf("seeddata.DemoPromoCodes has %s but demo_customers_promos.sql does not insert it", code)
		}
	}

	// Reverse direction (ut-docs#539 review N3's lesson, same reasoning as
	// the item-side reverse check above): a row added to
	// demo_customers_promos.sql's INSERTs without also adding it to
	// DemoCustomerIDs/DemoPromoCodes would seed but be permanently
	// unremovable, and none of the forward checks above would catch it.
	custRows := regexp.MustCompile(`(?m)^\('cust-\d+',`).FindAllString(seeddata.DemoCustomersPromosSQL, -1)
	if len(custRows) != len(seeddata.DemoCustomerIDs) {
		t.Errorf("demo_customers_promos.sql's customers INSERT has %d rows, seeddata.DemoCustomerIDs has %d — a customer was added to one but not the other",
			len(custRows), len(seeddata.DemoCustomerIDs))
	}
	promoRows := regexp.MustCompile(`(?m)^\('[A-Z0-9]+',\s*'(?:amount|percent)'`).FindAllString(seeddata.DemoCustomersPromosSQL, -1)
	if len(promoRows) != len(seeddata.DemoPromoCodes) {
		t.Errorf("demo_customers_promos.sql's promotions INSERT has %d rows, seeddata.DemoPromoCodes has %d — a promo was added to one but not the other",
			len(promoRows), len(seeddata.DemoPromoCodes))
	}
}

// ut-docs#1425 review finding F6: TestDeadTaxInclusiveSeedRemoved asserted
// on a fresh Open that pos.tax_inclusive (a dead settings key removed by
// the now-deleted migration 022) never seeds, while its neighbouring
// defaults survive — pure final-state, no replay, so it went with the rest
// of dead_seed_test.go's rewind tests by mistake (the file's replay tests
// were correctly deleted; this one wasn't a replay test). The baseline
// (001_init.sql) is currently correct — it just doesn't carry the dead
// key — but since ADR-0074 makes 001 freely editable pre-revenue, nothing
// else guards against the key being re-added by a future edit.
func TestBaselineSeedsNoDeadTaxInclusiveKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "dead-seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'pos.tax_inclusive'`).Scan(&n); err != nil {
		t.Fatalf("count pos.tax_inclusive: %v", err)
	}
	if n != 0 {
		t.Fatalf("pos.tax_inclusive present in the baseline seed (n=%d) — this key was deliberately dropped (ut-docs#22/migration 022) and must stay out of 001_init.sql", n)
	}

	// The neighboring defaults seeded by the same statement must survive.
	for _, key := range []string{"store.name", "store.currency"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, key).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", key, err)
		}
		if n != 1 {
			t.Fatalf("seed %s: got %d rows, want 1", key, n)
		}
	}
}
