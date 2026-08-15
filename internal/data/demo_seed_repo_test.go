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

// ut-docs#566: a shop that configures its till before opening — renaming,
// repricing, or re-SKU'ing a demo item — has already made it real, even
// with zero sale_lines/stock_movements history. Each edit is independently
// sufficient to keep the item; a genuinely untouched sibling item is
// removed as before, proving the predicate doesn't just keep everything.
func TestRemoveDemoCatalogueKeepsEditedItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// itm001 renamed, itm002 repriced, itm003 re-SKU'd — no sale, no stock
	// movement against any of them.
	if _, err := d.DB.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE items SET base_price = 350 WHERE id = 'itm002'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE items SET sku = 'MY-OWN-SKU' WHERE id = 'itm003'`); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 47 || kept != 3 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 47, 3", removed, kept)
	}
	for _, id := range []string{"itm001", "itm002", "itm003"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("edited item %s was removed", id)
		}
	}
	// itm004, genuinely untouched, is gone like the rest of the 47.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm004'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("untouched item itm004 was kept — predicate over-broadened, not just the edited items")
	}
	// Their name/price edits survived the removal pass untouched.
	var name string
	if err := d.DB.QueryRow(`SELECT name FROM items WHERE id = 'itm001'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Flat White" {
		t.Errorf("itm001.name = %q after removal, want the operator's rename intact", name)
	}
}

// ut-docs#567: the 3 demo customers + 3 demo promo codes get the same
// opt-in treatment ut-docs#539 gave the demo catalogue.
func TestSeedDemoCustomersPromos(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)

	// Post-038 fresh install: nothing there yet.
	if n, err := repo.SampleCustomerPromoCount(ctx); err != nil || n != 0 {
		t.Fatalf("SampleCustomerPromoCount before seed = %d, %v; want 0, nil", n, err)
	}

	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("SeedDemoCustomersPromos: %v", err)
	}

	for table, want := range map[string]int{"customers": 3, "promotions": 3} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("after seed: %s = %d rows, want %d", table, n, want)
		}
	}
	for table := range map[string]int{"customers": 0, "promotions": 0} {
		var unflagged int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE is_sample_data != 1`).Scan(&unflagged); err != nil {
			t.Fatal(err)
		}
		if unflagged != 0 {
			t.Errorf("%d seeded %s rows are not flagged is_sample_data = 1", unflagged, table)
		}
	}
	if n, err := repo.SampleCustomerPromoCount(ctx); err != nil || n != 6 {
		t.Fatalf("SampleCustomerPromoCount after seed = %d, %v; want 6, nil", n, err)
	}

	// Idempotent: seeding again must neither fail nor duplicate.
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("second SeedDemoCustomersPromos: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("after re-seed: customers = %d, want 3 (INSERT OR IGNORE must not duplicate)", n)
	}
}

// The Settings "Remove sample data" action, customer/promo half: removes
// every untouched demo customer/promo and reports removed vs kept. A demo
// customer is kept once actually sold-to or targeted by any promotion; a
// demo promo is kept once targeted at a specific customer (see
// remove_demo_customers_promos.sql for why the promo rule differs).
func TestRemoveDemoCustomersPromos(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Touch three of the six rows, each via a DIFFERENT signal so each rule
	// is independently exercised: cust-001 sold-to (sales.customer_id),
	// cust-002 targeted by a real (non-demo) promotion
	// (promotions.customer_id), PROMO50 targeted at cust-001 — a customer
	// already kept for its own reason, so PROMO50's own removability can't
	// be confused with "did targeting this promo save a customer."
	// cust-003, PROMO500 and DISC10 are left completely untouched.
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, customer_id, subtotal, total) VALUES ('s-1', 'R-1', 'cust-001', 120, 120)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO promotions (code, type, value, description, is_active, customer_id) VALUES ('REAL10', 'amount', 100, 'loyalty perk', 1, 'cust-002')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET customer_id = 'cust-001' WHERE code = 'PROMO50'`); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Removed: cust-003, PROMO500, DISC10 = 3. Kept: cust-001 (sold),
	// cust-002 (targeted by REAL10), PROMO50 (now targeted at cust-001) = 3.
	if removed != 3 || kept != 3 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, kept %d; want 3, 3", removed, kept)
	}

	for _, id := range []string{"cust-001", "cust-002"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("touched customer %s was removed", id)
		}
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'cust-003'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("untouched customer cust-003 was kept")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO50'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("PROMO50 (now targeted at a customer) was removed")
	}
	for _, code := range []string{"PROMO500", "DISC10"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = ?`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("untouched promo %s was kept", code)
		}
	}
	// The non-demo REAL10 promotion, and its targeting, is untouched by any
	// of this — it's not is_sample_data.
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'REAL10'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("non-demo promotion REAL10 was removed")
	}

	// Running removal again is a no-op that reports the same kept count.
	removed, kept, err = repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("second RemoveDemoCustomersPromos: %v", err)
	}
	if removed != 0 || kept != 3 {
		t.Fatalf("second RemoveDemoCustomersPromos = removed %d, kept %d; want 0, 3", removed, kept)
	}
}

// Independent review (ut-docs#567, F2): a demo customer referenced only by
// a HELD (parked) sale — not yet in the sales table — must still be kept.
// Without this, removal could delete a customer a parked basket still
// points at, and tendering that sale later would FK-fail (checkout must
// never break — the till's own non-negotiable).
func TestRemoveDemoCustomersPromosKeepsHeldSaleCustomer(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Mirrors pos.SnapshotPayload's real shape (internal/pos/hold.go) just
	// enough to exercise the LIKE match — only customer_id matters here.
	payload := `{"lines":[],"customer_id":"cust-001","customer_name":"Alice Carter","total":0}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, payload) VALUES ('h-1', 'Table 4', ?)`, payload); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Kept: cust-001 (held sale). Removed: cust-002, cust-003, PROMO50,
	// PROMO500, DISC10 = 5.
	if removed != 5 || kept != 1 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, kept %d; want 5, 1", removed, kept)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'cust-001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("customer referenced only by a held sale was removed")
	}
}

// Independent review (ut-docs#567, F3): a demo promotion the shop has
// genuinely customized (edited value/description, without necessarily
// targeting a specific customer) must be kept, not just one with
// customer_id set — there is no promotions management UI, so customer_id
// is not the only way a shop could rely on a promo.
func TestRemoveDemoCustomersPromosKeepsCustomizedPromotion(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET value = 1500, description = 'Summer 15% sale' WHERE code = 'DISC10'`); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Kept: DISC10 (customized), plus every customer (none touched, but
	// DISC10 no longer counts toward "removed" either way since it's kept).
	// Removed: cust-001/002/003, PROMO50, PROMO500 = 5.
	if removed != 5 || kept != 1 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, kept %d; want 5, 1", removed, kept)
	}
	var desc string
	if err := d.DB.QueryRow(`SELECT description FROM promotions WHERE code = 'DISC10'`).Scan(&desc); err != nil {
		t.Fatal("customized DISC10 was removed:", err)
	}
	if desc != "Summer 15% sale" {
		t.Fatalf("DISC10.description = %q, want the customized value intact", desc)
	}
}

// Removing when nothing was ever seeded reports zeros, not an error.
func TestRemoveDemoCustomersPromosEmpty(t *testing.T) {
	d := openDemoSeedTestDB(t)
	removed, kept, err := NewDemoSeedRepo(d.DB).RemoveDemoCustomersPromos(context.Background())
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos on empty DB: %v", err)
	}
	if removed != 0 || kept != 0 {
		t.Fatalf("RemoveDemoCustomersPromos on empty DB = removed %d, kept %d; want 0, 0", removed, kept)
	}
}

// An operator's own customer/promotion is never counted or touched, even if
// it clashes with nothing — only is_sample_data = 1 rows are.
func TestRemoveDemoCustomersPromosLeavesOwnRecordsAlone(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if _, err := d.DB.Exec(`INSERT INTO customers (id, name) VALUES ('own-cust', 'My Own Customer')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO promotions (code, type, value, is_active) VALUES ('OWNCODE', 'amount', 100, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatal(err)
	}
	removed, kept, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 6 || kept != 0 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, kept %d; want 6, 0", removed, kept)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'own-cust'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("operator's own customer was removed by the sample-data cleanup")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'OWNCODE'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("operator's own promotion was removed by the sample-data cleanup")
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

// ut-docs#633: a demo item referenced only by a HELD (parked) sale — not
// yet in sale_lines/stock_movements — must still be kept, the same gap
// ut-docs#567 (TestRemoveDemoCustomersPromosKeepsHeldSaleCustomer) already
// closed for demo customers. Without this, "Remove sample data" could
// delete an item a parked basket still points at, and recalling +
// tendering that held sale later would FK-fail.
func TestRemoveDemoCatalogueKeepsHeldSaleItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Mirrors pos.SnapshotLine's real shape (internal/pos/hold.go) just
	// enough to exercise the LIKE match — only item_id matters here.
	payload := `{"lines":[{"sku":"SKU-0003","name":"Held Item","qty":1,"price_cents":100,"item_id":"itm003"}],"total":100}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, payload) VALUES ('h-1', 'Table 4', ?)`, payload); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	// Kept: itm003 (held sale). Removed: the other 49 items.
	if removed != 49 || kept != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, kept)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm003'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("item referenced only by a held sale was removed")
	}
}

// Same gap as above, exercised via the variant-only NOT EXISTS clause in
// isolation: a real resolved variant line actually carries BOTH item_id and
// variant_id (internal/ui/buttons.go's resolve() / internal/data/pos_repo.go's
// resolveVariant set both from the same row), so in practice the item_id
// clause above already catches a held variant line too — this payload
// (variant_id only, no item_id) is a synthetic shape that exists purely to
// prove the variant clause is independently correct, as defense-in-depth
// against a payload shape production doesn't currently produce.
func TestRemoveDemoCatalogueKeepsHeldSaleVariantItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// var010 belongs to itm041 (internal/data/seeddata/demo_catalogue.sql).
	payload := `{"lines":[{"sku":"SKU-0041-250","name":"Held Variant","qty":1,"price_cents":210,"variant_id":"var010"}],"total":210}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, payload) VALUES ('h-1', 'Table 4', ?)`, payload); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	// Kept: itm041 (held sale, via its variant). Removed: the other 49.
	if removed != 49 || kept != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, kept)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm041'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("item referenced only by a held sale's variant line was removed")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE id = 'var010'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("variant referenced only by a held sale did not survive (item_variants cascades from its parent item, so this would only fail if the item above wrongly did too)")
	}
}
