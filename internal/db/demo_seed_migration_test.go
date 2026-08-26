package db

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data/seeddata"
)

// frozenRemoveDemoSQL036 is a frozen, byte-for-byte snapshot of
// seeddata/remove_demo.sql AS IT STOOD when migration 036 was authored —
// not a copy of the live (evolving) seeddata.RemoveDemoSQL. Migrations are
// append-only (universal-till/CLAUDE.md): 036 embeds this text verbatim as
// a one-time upgrade step for pre-036 databases, and can never be edited to
// pick up a later behavioural fix to the live removal script. ut-docs#640
// added an *_archive-aware clause to seeddata.RemoveDemoSQL (closing a gap
// where "Remove sample data" could delete a demo item a still-restorable
// reset-archive batch depends on) — but that gap cannot exist on a
// pre-036 database at migration time: the reset-archive mechanism
// (ADR-0042, ut-docs#187) shipped chronologically AFTER 036, so no database
// migrating through 036 can already hold archived demo sale/stock rows.
// 036 staying on the pre-#640 rule is therefore correct, not stale — this
// fixture is what TestMigration036MatchesSeedData now pins against instead
// of the live seeddata.RemoveDemoSQL, which is expected to keep evolving.
//
//go:embed testdata/frozen_remove_demo_036.sql
var frozenRemoveDemoSQL036 string

// frozenRemoveDemoCustomersPromosSQL038 is the same kind of frozen snapshot
// as frozenRemoveDemoSQL036, for migration 038 and
// seeddata/remove_demo_customers_promos.sql. ut-docs#640 also added a
// sales_archive-aware clause there (a demo customer referenced only by an
// archived — post-reset — sale must not be deleted); 038 predates the
// reset-archive mechanism the same way 036 does, so it stays pinned to the
// pre-#640 rule for the same reason.
//
//go:embed testdata/frozen_remove_demo_customers_promos_038.sql
var frozenRemoveDemoCustomersPromosSQL038 string

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

	// 001's sample customers/promotions are migration 036's out-of-scope
	// note (catalogue only) — migration 038 (ut-docs#567) closes that gap,
	// so on a fresh install they're gone too, same as the catalogue.
	for table, want := range map[string]int{"customers": 0, "promotions": 0} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("fresh install: %s has %d rows, want %d (038 must remove the demo customers/promos)", table, n, want)
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

	// Rewind the ledger so 036 and its follower replay on reopen, and undo
	// their non-idempotent DDL (ALTER TABLE ADD COLUMN), same pattern as the
	// 022/023 upgrade tests in this package.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 36`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind is_sample_data column: %v", err)
	}
	// Migration 037 adds report_archive.cloud_acked_at -- same non-idempotent
	// replay problem (ut-docs#571).
	if _, err := d.DB.Exec(`ALTER TABLE report_archive DROP COLUMN cloud_acked_at`); err != nil {
		t.Fatalf("rewind report_archive.cloud_acked_at column: %v", err)
	}
	// Migration 038 adds customers/promotions.is_sample_data -- same
	// non-idempotent replay problem (ut-docs#567).
	if _, err := d.DB.Exec(`ALTER TABLE customers DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind customers.is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE promotions DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind promotions.is_sample_data column: %v", err)
	}
	// Migration 049 adds audit_log.blocked_actor_id -- same non-idempotent
	// replay problem (ut-docs#557).
	if _, err := d.DB.Exec(`ALTER TABLE audit_log DROP COLUMN blocked_actor_id`); err != nil {
		t.Fatalf("rewind audit_log.blocked_actor_id column: %v", err)
	}
	// Migrations 050/051 add payments/payments_archive's card-present
	// columns -- same non-idempotent replay problem (ut-docs#543).
	for _, col := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
		if _, err := d.DB.Exec(`ALTER TABLE payments DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments.%s column: %v", col, err)
		}
		if _, err := d.DB.Exec(`ALTER TABLE payments_archive DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments_archive.%s column: %v", col, err)
		}
	}
	// Migration 054 adds the `tables` table and held_sales.table_id -- same
	// non-idempotent replay problem (ut-docs#814).
	rewindTables054(t, d)
	rewindTracking058(t, d)
	rewindFiscalRegisterDE059(t, d)
	rewindTipRecipient061(t, d)
	rewindServiceChargeTaxBasis062(t, d)
	rewindShiftCashRecon067(t, d)
	rewindVoucherIssueTotal069(t, d)
	rewindZReportNumbering070(t, d)
	rewindPaymentsVoucherID072(t, d)
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
	// Migration 037 adds report_archive.cloud_acked_at -- same non-idempotent
	// replay problem (ut-docs#571).
	if _, err := d.DB.Exec(`ALTER TABLE report_archive DROP COLUMN cloud_acked_at`); err != nil {
		t.Fatalf("rewind report_archive.cloud_acked_at column: %v", err)
	}
	// Migration 038 adds customers/promotions.is_sample_data -- same
	// non-idempotent replay problem (ut-docs#567).
	if _, err := d.DB.Exec(`ALTER TABLE customers DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind customers.is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE promotions DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind promotions.is_sample_data column: %v", err)
	}
	// Migration 049 adds audit_log.blocked_actor_id -- same non-idempotent
	// replay problem (ut-docs#557).
	if _, err := d.DB.Exec(`ALTER TABLE audit_log DROP COLUMN blocked_actor_id`); err != nil {
		t.Fatalf("rewind audit_log.blocked_actor_id column: %v", err)
	}
	// Migrations 050/051 add payments/payments_archive's card-present
	// columns -- same non-idempotent replay problem (ut-docs#543).
	for _, col := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
		if _, err := d.DB.Exec(`ALTER TABLE payments DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments.%s column: %v", col, err)
		}
		if _, err := d.DB.Exec(`ALTER TABLE payments_archive DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments_archive.%s column: %v", col, err)
		}
	}
	// Migration 054 adds the `tables` table and held_sales.table_id -- same
	// non-idempotent replay problem (ut-docs#814).
	rewindTables054(t, d)
	rewindTracking058(t, d)
	rewindFiscalRegisterDE059(t, d)
	rewindTipRecipient061(t, d)
	rewindServiceChargeTaxBasis062(t, d)
	rewindShiftCashRecon067(t, d)
	rewindVoucherIssueTotal069(t, d)
	rewindZReportNumbering070(t, d)
	rewindPaymentsVoucherID072(t, d)
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

// ut-docs#566: a shop that renamed a demo item before ever trading with it
// keeps that item across the upgrade path too, the same as it does via the
// Settings removal action (data.TestRemoveDemoCatalogueKeepsEditedItem) —
// migration 036 and DemoSeedRepo.RemoveDemoCatalogue share the exact same
// predicate, but this proves the migration path independently rather than
// assuming the shared-block guard is enough.
func TestDemoCatalogueUpgradeKeepsRenamedUntradedItem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m036-upgrade-renamed.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Reconstruct the pre-036 state: full demo catalogue present, with
	// itm001 renamed by the shop before ever selling or stock-adjusting it.
	if _, err := d.DB.Exec(seeddata.DemoCatalogueSQL); err != nil {
		t.Fatalf("re-seed demo catalogue: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatalf("rename itm001: %v", err)
	}

	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 36`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE report_archive DROP COLUMN cloud_acked_at`); err != nil {
		t.Fatalf("rewind report_archive.cloud_acked_at column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE customers DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind customers.is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE promotions DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind promotions.is_sample_data column: %v", err)
	}
	// Migration 049 adds audit_log.blocked_actor_id -- same non-idempotent
	// replay problem (ut-docs#557).
	if _, err := d.DB.Exec(`ALTER TABLE audit_log DROP COLUMN blocked_actor_id`); err != nil {
		t.Fatalf("rewind audit_log.blocked_actor_id column: %v", err)
	}
	// Migrations 050/051 add payments/payments_archive's card-present
	// columns -- same non-idempotent replay problem (ut-docs#543).
	for _, col := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
		if _, err := d.DB.Exec(`ALTER TABLE payments DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments.%s column: %v", col, err)
		}
		if _, err := d.DB.Exec(`ALTER TABLE payments_archive DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments_archive.%s column: %v", col, err)
		}
	}
	// Migration 054 adds the `tables` table and held_sales.table_id -- same
	// non-idempotent replay problem (ut-docs#814).
	rewindTables054(t, d)
	rewindTracking058(t, d)
	rewindFiscalRegisterDE059(t, d)
	rewindTipRecipient061(t, d)
	rewindServiceChargeTaxBasis062(t, d)
	rewindShiftCashRecon067(t, d)
	rewindVoucherIssueTotal069(t, d)
	rewindZReportNumbering070(t, d)
	rewindPaymentsVoucherID072(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // replays 036 against the simulated pre-036 till
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// itm001 survives, still renamed, still flagged as sample data; every
	// other (genuinely untouched) demo item is gone.
	assertIDs(t, d, "items", []string{"itm001"})
	var name string
	var flag int
	if err := d.DB.QueryRow(`SELECT name, is_sample_data FROM items WHERE id = 'itm001'`).Scan(&name, &flag); err != nil {
		t.Fatal(err)
	}
	if name != "Flat White" {
		t.Errorf("itm001.name = %q after 036, want the shop's rename intact", name)
	}
	if flag != 1 {
		t.Errorf("itm001.is_sample_data = %d after 036, want 1", flag)
	}
}

// ut-docs#567: migration 038 gives the 3 demo customers + 3 demo promo
// codes the same opt-in treatment 036 gave the catalogue. Upgrade path: an
// existing till whose 001 seeded them long ago, and that actually used one
// of each kind. 038 must keep every touched customer (sold-to, or targeted
// by a promotion) and every touched promotion (targeted at a customer),
// and remove only the untouched rest.
func TestDemoCustomersPromosUpgradeKeepsTouchedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m038-upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Reconstruct the pre-038 state: the full demo customers/promos present
	// (038 already ran on this fresh file, so re-seed them), plus real
	// usage: cust-001 sold-to, PROMO50 targeted at cust-002, DISC10 edited
	// (independent review, ut-docs#567 F3 — customization without
	// targeting must also count as touched).
	if _, err := d.DB.Exec(seeddata.DemoCustomersPromosSQL); err != nil {
		t.Fatalf("re-seed demo customers/promos: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, customer_id, subtotal, total) VALUES ('sale-038', 'R-038', 'cust-001', 120, 120)`); err != nil {
		t.Fatalf("insert sale: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET customer_id = 'cust-002' WHERE code = 'PROMO50'`); err != nil {
		t.Fatalf("target PROMO50: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET value = 1500, description = 'Summer 15% sale' WHERE code = 'DISC10'`); err != nil {
		t.Fatalf("customize DISC10: %v", err)
	}

	// Rewind the ledger so 038 and its follower replay on reopen, and undo
	// its non-idempotent DDL (ALTER TABLE ADD COLUMN), same pattern as the
	// 036 upgrade tests above.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 38`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE customers DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind customers.is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE promotions DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind promotions.is_sample_data column: %v", err)
	}
	// Migration 049 adds audit_log.blocked_actor_id -- same non-idempotent
	// replay problem (ut-docs#557).
	if _, err := d.DB.Exec(`ALTER TABLE audit_log DROP COLUMN blocked_actor_id`); err != nil {
		t.Fatalf("rewind audit_log.blocked_actor_id column: %v", err)
	}
	// Migrations 050/051 add payments/payments_archive's card-present
	// columns -- same non-idempotent replay problem (ut-docs#543).
	for _, col := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
		if _, err := d.DB.Exec(`ALTER TABLE payments DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments.%s column: %v", col, err)
		}
		if _, err := d.DB.Exec(`ALTER TABLE payments_archive DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments_archive.%s column: %v", col, err)
		}
	}
	// Migration 054 adds the `tables` table and held_sales.table_id -- same
	// non-idempotent replay problem (ut-docs#814).
	rewindTables054(t, d)
	rewindTracking058(t, d)
	rewindFiscalRegisterDE059(t, d)
	rewindTipRecipient061(t, d)
	rewindServiceChargeTaxBasis062(t, d)
	rewindShiftCashRecon067(t, d)
	rewindVoucherIssueTotal069(t, d)
	rewindZReportNumbering070(t, d)
	rewindPaymentsVoucherID072(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // replays 038 against the simulated pre-038 till
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// cust-001 (sold) and cust-002 (targeted by PROMO50) survive, flagged;
	// cust-003 is gone.
	assertIDs(t, d, "customers", []string{"cust-001", "cust-002"})
	var flag int
	for _, id := range []string{"cust-001", "cust-002"} {
		if err := d.DB.QueryRow(`SELECT is_sample_data FROM customers WHERE id = ?`, id).Scan(&flag); err != nil {
			t.Fatal(err)
		}
		if flag != 1 {
			t.Errorf("customer %s is_sample_data = %d, want 1 (038 marks legacy demo rows)", id, flag)
		}
	}

	// PROMO50 (now targeted) and DISC10 (now customized) survive; PROMO500
	// alone is gone.
	var codes []string
	rows, err := d.DB.Query(`SELECT code FROM promotions ORDER BY code`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := "DISC10,PROMO50"; strings.Join(codes, ",") != want {
		t.Errorf("surviving promotions = %v, want [%s]", codes, want)
	}
	var disc10Desc string
	if err := d.DB.QueryRow(`SELECT description FROM promotions WHERE code = 'DISC10'`).Scan(&disc10Desc); err != nil {
		t.Fatal(err)
	}
	if disc10Desc != "Summer 15% sale" {
		t.Errorf("DISC10.description = %q after 038, want the customized value intact", disc10Desc)
	}

	// The trading history and the real targeting are intact.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE id = 'sale-038'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sale-038 missing after 038, want it intact")
	}
	var targetedCustomer string
	if err := d.DB.QueryRow(`SELECT customer_id FROM promotions WHERE code = 'PROMO50'`).Scan(&targetedCustomer); err != nil {
		t.Fatal(err)
	}
	if targetedCustomer != "cust-002" {
		t.Errorf("PROMO50.customer_id = %q after 038, want cust-002 (targeting must survive)", targetedCustomer)
	}
}

// A shop that never touched anything upgrades to a completely clean set —
// the exact fresh-install outcome, reached via the upgrade path.
func TestDemoCustomersPromosUpgradeRemovesAllWhenUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m038-upgrade-clean.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(seeddata.DemoCustomersPromosSQL); err != nil {
		t.Fatalf("re-seed demo customers/promos: %v", err)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 38`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE customers DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind customers.is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE promotions DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind promotions.is_sample_data column: %v", err)
	}
	// Migration 049 adds audit_log.blocked_actor_id -- same non-idempotent
	// replay problem (ut-docs#557).
	if _, err := d.DB.Exec(`ALTER TABLE audit_log DROP COLUMN blocked_actor_id`); err != nil {
		t.Fatalf("rewind audit_log.blocked_actor_id column: %v", err)
	}
	// Migrations 050/051 add payments/payments_archive's card-present
	// columns -- same non-idempotent replay problem (ut-docs#543).
	for _, col := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
		if _, err := d.DB.Exec(`ALTER TABLE payments DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments.%s column: %v", col, err)
		}
		if _, err := d.DB.Exec(`ALTER TABLE payments_archive DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments_archive.%s column: %v", col, err)
		}
	}
	// Migration 054 adds the `tables` table and held_sales.table_id -- same
	// non-idempotent replay problem (ut-docs#814).
	rewindTables054(t, d)
	rewindTracking058(t, d)
	rewindFiscalRegisterDE059(t, d)
	rewindTipRecipient061(t, d)
	rewindServiceChargeTaxBasis062(t, d)
	rewindShiftCashRecon067(t, d)
	rewindVoucherIssueTotal069(t, d)
	rewindZReportNumbering070(t, d)
	rewindPaymentsVoucherID072(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	for _, table := range []string{"customers", "promotions"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("untouched upgrade: %s has %d rows, want 0", table, n)
		}
	}
}

// Migration 038 embeds the two shared seeddata scripts VERBATIM — same
// drift guard as TestMigration036MatchesSeedData, scoped to customers/
// promos.
func TestMigration038MatchesSeedData(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var m038 string
	for _, m := range migs {
		if m.Version == 38 {
			m038 = m.SQL
		}
	}
	if m038 == "" {
		t.Fatal("migration 038 not found")
	}
	if !strings.Contains(m038, seeddata.DemoCustomersPromosIDsSQL) {
		t.Error("038 does not contain seeddata/demo_customers_promos_ids.sql verbatim — regenerate the migration from the shared assets")
	}
	if !strings.Contains(m038, frozenRemoveDemoCustomersPromosSQL038) {
		t.Error("038 no longer matches its frozen pre-ut-docs#640 remove_demo_customers_promos.sql snapshot (testdata/frozen_remove_demo_customers_promos_038.sql) — 038 is an append-only migration and must never change; if this fails, something edited 038 itself, not seeddata.RemoveDemoCustomersPromosSQL")
	}
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
	if !strings.Contains(m036, frozenRemoveDemoSQL036) {
		t.Error("036 no longer matches its frozen pre-ut-docs#640 remove_demo.sql snapshot (testdata/frozen_remove_demo_036.sql) — 036 is an append-only migration and must never change; if this fails, something edited 036 itself, not seeddata.RemoveDemoSQL")
	}
	// And the Go-side ID slices mirror the SQL lists. demo_seed_items now
	// carries sku/name/base_price alongside id (ut-docs#566), so its rows are
	// no longer a bare 1-tuple — match the id as the row's leading column.
	for _, id := range seeddata.ItemIDs {
		if !strings.Contains(seeddata.DemoIDsSQL, "('"+id+"',") {
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
