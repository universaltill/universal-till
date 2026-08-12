package db

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data/seeddata"
)

// ut-docs#539: 001_init.sql unconditionally seeded a 50-item grocery demo
// catalogue; migration 036 makes it opt-in. On a brand-new install 036 runs
// microseconds after 001, so the demo rows must be gone before the operator
// ever sees the till — while the structural defaults (tax codes, stock
// locations) and 001's non-catalogue samples stay.
func TestDemoCatalogueRemovedOnFreshInstall(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m036-fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	// The whole demo catalogue is gone — 001 seeded nothing else into these
	// tables, so they must be completely empty on a fresh install.
	for _, table := range []string{
		"items", "item_barcodes", "item_images", "item_variants",
		"variant_barcodes", "inventory", "price_history", "shortcut_buttons",
		"categories", "brands",
	} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("fresh install: %s has %d rows, want 0 (036 must remove the demo catalogue)", table, n)
		}
	}

	// Structural defaults are NOT demo data and must survive.
	for table, want := range map[string]int{"tax_codes": 3, "stock_locations": 3} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("fresh install: %s has %d rows, want %d (036 must not touch structural defaults)", table, n, want)
		}
	}

	// 001's sample customers/promotions are out of this card's scope
	// (catalogue only) — they must be untouched.
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers`).Scan(&n); err != nil {
		t.Fatalf("count customers: %v", err)
	}
	if n != 3 {
		t.Errorf("fresh install: customers has %d rows, want 3 (036 is catalogue-only)", n)
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

// The upgrade path: an existing till whose 001 seeded the catalogue long ago
// and that actually traded with some of it. 036 must keep every touched item
// (sold directly, sold via a variant, or stock-adjusted) plus its category
// and brand, and remove only the untouched rest.
func TestDemoCatalogueUpgradeKeepsTouchedItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m036-upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Reconstruct the pre-036 state: the full demo catalogue present (036
	// already ran on this fresh file, so re-seed it), plus real trading
	// history against three items:
	//   itm001 — sold directly (sale_lines.item_id)
	//   itm005 — sold via its variant var004 (sale_lines.variant_id)
	//   itm002 — stock-adjusted (stock_movements.item_id)
	if _, err := d.DB.Exec(seeddata.DemoCatalogueSQL); err != nil {
		t.Fatalf("re-seed demo catalogue: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('sale-036', 'R-036', 120, 120)`); err != nil {
		t.Fatalf("insert sale: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, variant_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES
		('sl-036-1', 'sale-036', 1, 'itm001', NULL, 'Coca-Cola Can 330ml', 1, 120, 2000, 20, 100, 120),
		('sl-036-2', 'sale-036', 2, NULL, 'var004', 'Orange Juice 500ml', 1, 140, 0, 0, 140, 140)`); err != nil {
		t.Fatalf("insert sale lines: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity)
		VALUES ('sm-036-1', 'itm002', NULL, 'loc_main', 'adjust', 5)`); err != nil {
		t.Fatalf("insert stock movement: %v", err)
	}

	// Rewind the ledger so only 036 replays on reopen, and undo its
	// non-idempotent DDL (ALTER TABLE ADD COLUMN), same pattern as the 022/
	// 023 upgrade tests in this package.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 36`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind is_sample_data column: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // replays 036 against the simulated pre-036 till
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Exactly the three touched items survive, flagged as sample data.
	rows, err := d.DB.Query(`SELECT id, is_sample_data FROM items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var id string
		var flag int
		if err := rows.Scan(&id, &flag); err != nil {
			t.Fatal(err)
		}
		got[id] = flag
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("surviving items = %v, want exactly itm001, itm002, itm005", got)
	}
	for _, id := range []string{"itm001", "itm002", "itm005"} {
		flag, ok := got[id]
		if !ok {
			t.Errorf("touched item %s was deleted by 036", id)
			continue
		}
		if flag != 1 {
			t.Errorf("%s is_sample_data = %d, want 1 (036 marks legacy demo rows)", id, flag)
		}
	}

	// A surviving item keeps its variants (var004/var005 belong to itm005;
	// var001/var002 to itm001; var003 to itm002) — and nothing else's.
	var variants []string
	vrows, err := d.DB.Query(`SELECT id FROM item_variants ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer vrows.Close()
	for vrows.Next() {
		var id string
		if err := vrows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		variants = append(variants, id)
	}
	if err := vrows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := "var001,var002,var003,var004,var005"; strings.Join(variants, ",") != want {
		t.Errorf("surviving variants = %v, want %s", variants, want)
	}

	// Their category (all three live in cat_drink) and brands survive
	// because the items do; every untouched demo category/brand is gone.
	assertIDs(t, d, "categories", []string{"cat_drink"})
	assertIDs(t, d, "brands", []string{"br_coca", "br_nestle", "br_pepsi"})

	// The trading history itself is intact.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("sale_lines = %d rows after 036, want 2", n)
	}

	// Untouched items' dependents went with them; the survivors keep theirs
	// (2 item-level inventory rows for itm001/itm002 + itm005's, and the
	// surviving variants' rows).
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id IN ('itm001','itm002','itm005')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("surviving item inventory rows = %d, want 3", n)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id IS NOT NULL AND item_id NOT IN ('itm001','itm002','itm005')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("untouched items left %d inventory rows behind", n)
	}
}

// A shop that never touched anything upgrades to a completely clean
// catalogue — the exact fresh-install outcome, reached via the upgrade path.
func TestDemoCatalogueUpgradeRemovesAllWhenUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m036-upgrade-clean.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(seeddata.DemoCatalogueSQL); err != nil {
		t.Fatalf("re-seed demo catalogue: %v", err)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 36`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind is_sample_data column: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	for _, table := range []string{"items", "categories", "brands", "inventory", "price_history", "shortcut_buttons"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("untouched upgrade: %s has %d rows, want 0", table, n)
		}
	}
}

// Migration 036 embeds the two shared seeddata scripts VERBATIM — the
// migration's removal ID lists and safety predicate must never drift from
// internal/data/seeddata, which the opt-in seed and the Settings removal
// action execute at runtime.
func TestMigration036MatchesSeedData(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var m036 string
	for _, m := range migs {
		if m.Version == 36 {
			m036 = m.SQL
		}
	}
	if m036 == "" {
		t.Fatal("migration 036 not found")
	}
	if !strings.Contains(m036, seeddata.DemoIDsSQL) {
		t.Error("036 does not contain seeddata/demo_ids.sql verbatim — regenerate the migration from the shared assets")
	}
	if !strings.Contains(m036, seeddata.RemoveDemoSQL) {
		t.Error("036 does not contain seeddata/remove_demo.sql verbatim — regenerate the migration from the shared assets")
	}
	// And the Go-side ID slices mirror the SQL lists.
	for _, id := range seeddata.ItemIDs {
		if !strings.Contains(seeddata.DemoIDsSQL, "('"+id+"')") {
			t.Errorf("seeddata.ItemIDs has %s but demo_ids.sql does not list it", id)
		}
		if !strings.Contains(seeddata.DemoCatalogueSQL, "'"+id+"'") {
			t.Errorf("seeddata.ItemIDs has %s but demo_catalogue.sql does not insert it", id)
		}
	}
	for _, id := range append(append([]string{}, seeddata.CategoryIDs...), seeddata.BrandIDs...) {
		if !strings.Contains(seeddata.DemoIDsSQL, "('"+id+"')") {
			t.Errorf("seeddata ID %s missing from demo_ids.sql", id)
		}
	}
	if n := strings.Count(seeddata.DemoIDsSQL, "('itm"); n != len(seeddata.ItemIDs) {
		t.Errorf("demo_ids.sql lists %d items, seeddata.ItemIDs has %d", n, len(seeddata.ItemIDs))
	}
	// Reverse direction too (ut-docs#539 review, N3): the checks above only
	// catch an ID present in seeddata.ItemIDs but missing from the SQL — an
	// item added to demo_catalogue.sql's INSERT block WITHOUT also adding it
	// to ItemIDs/demo_ids.sql would seed fine but be permanently
	// unremovable (never in demo_seed_items, so never eligible for
	// deletion), and none of the above would catch it. Count each item's
	// own INSERT row by its literal id prefix, not just "'itm" (which also
	// matches every item_barcodes/inventory/... row referencing an item).
	itemRows := regexp.MustCompile(`(?m)^\s*\('itm\d{3}',`).FindAllString(seeddata.DemoCatalogueSQL, -1)
	if len(itemRows) != len(seeddata.ItemIDs) {
		t.Errorf("demo_catalogue.sql's items INSERT has %d rows, seeddata.ItemIDs has %d — an item was added to one but not the other",
			len(itemRows), len(seeddata.ItemIDs))
	}
}

// assertIDs fails unless table's id set is exactly want (sorted).
func assertIDs(t *testing.T, d *DB, table string, want []string) {
	t.Helper()
	rows, err := d.DB.Query(fmt.Sprintf(`SELECT id FROM %s ORDER BY id`, table))
	if err != nil {
		t.Fatalf("query %s: %v", table, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s ids = %v, want %v", table, got, want)
	}
}
