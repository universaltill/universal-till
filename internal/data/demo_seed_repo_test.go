package data

import (
	"context"
	"errors"
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
	if removed != 48 || len(kept) != 2 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 48, 2", removed, len(kept))
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
	if removed != 0 || len(kept) != 2 {
		t.Fatalf("second RemoveDemoCatalogue = removed %d, kept %d; want 0, 2", removed, len(kept))
	}
}

// Removing when nothing was ever seeded reports zeros, not an error.
func TestRemoveDemoCatalogueEmpty(t *testing.T) {
	d := openDemoSeedTestDB(t)
	removed, kept, err := NewDemoSeedRepo(d.DB).RemoveDemoCatalogue(context.Background())
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue on empty DB: %v", err)
	}
	if removed != 0 || len(kept) != 0 {
		t.Fatalf("RemoveDemoCatalogue on empty DB = removed %d, kept %d; want 0, 0", removed, len(kept))
	}
}

// ut-docs#1840 (supersedes ut-docs#566's original assertion, which encoded
// the exact bug this card fixes as "correct": a shop that has never traded
// for real has nothing left for the pristine-match rule to protect, so
// renaming/repricing/re-SKU'ing a demo item no longer disqualifies it from
// "Remove sample data" — AC1's till-level gate (demoTillHasNoRealHistorySQL)
// picks the relaxed script whenever no non-sample item has any trading
// history anywhere, live or archived, and this DB has only demo data in it.
// A genuinely untouched sibling item is removed exactly as before, proving
// the predicate isn't just "remove everything now."
func TestRemoveDemoCatalogueRemovesEditedItemsWhenTillHasNoRealHistory(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// itm001 renamed, itm002 repriced, itm003 re-SKU'd — no sale, no stock
	// movement against any of them, and nothing non-sample anywhere in this
	// till.
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
	if removed != 50 || len(kept) != 0 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 50, 0 (edited items are removable when the till has never traded for real)", removed, len(kept))
	}
	for _, id := range []string{"itm001", "itm002", "itm003", "itm004"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("edited item %s survived removal — want it gone, same as an untouched item", id)
		}
	}
}

// The strict counterpart: the moment the till has ANY real (non-sample)
// trading history, RemoveDemoCatalogue falls back to the pristine-match
// rule exactly as it always has — an edited demo item is kept, and
// KeptDemoItem reports why (ReasonEdited), so AC3's "remove anyway"/"keep
// as my own item" resolution has something to act on.
func TestRemoveDemoCatalogueKeepsEditedItemWhenTillHasRealHistory(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A real (non-sample) item, genuinely sold — this is what flips the
	// till-level gate to "has real history," independent of anything
	// touching the demo catalogue itself.
	seedRealSale(t, d, "own-1", "s-1")

	// itm001 renamed — no sale, no stock movement against it.
	if _, err := d.DB.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1 (a till with real trading history keeps an edited item)", removed, len(kept))
	}
	if kept[0].ID != "itm001" || kept[0].Reason != KeptReasonEdited {
		t.Fatalf("kept[0] = %+v; want itm001/edited", kept[0])
	}
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

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Removed: cust-003, PROMO500, DISC10 = 3. Kept: cust-001 (sold),
	// cust-002 (targeted by REAL10), PROMO50 (now targeted at cust-001) = 3.
	if removed != 3 || len(keptCustomers) != 2 || len(keptPromos) != 1 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 3, 2, 1",
			removed, len(keptCustomers), len(keptPromos))
	}
	byID := map[string]KeptDemoCustomer{}
	for _, c := range keptCustomers {
		byID[c.ID] = c
	}
	if byID["cust-001"].Reason != KeptReasonHistory {
		t.Errorf("cust-001 reason = %q, want history", byID["cust-001"].Reason)
	}
	if byID["cust-002"].Reason != KeptReasonTargeted {
		t.Errorf("cust-002 reason = %q, want targeted", byID["cust-002"].Reason)
	}
	if keptPromos[0].Code != "PROMO50" || keptPromos[0].Reason != KeptReasonTargeted {
		t.Fatalf("keptPromos = %+v, want [PROMO50/targeted]", keptPromos)
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
	removed, keptCustomers, keptPromos, err = repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("second RemoveDemoCustomersPromos: %v", err)
	}
	if removed != 0 || len(keptCustomers) != 2 || len(keptPromos) != 1 {
		t.Fatalf("second RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 0, 2, 1",
			removed, len(keptCustomers), len(keptPromos))
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

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Kept: cust-001 (held sale). Removed: cust-002, cust-003, PROMO50,
	// PROMO500, DISC10 = 5.
	if removed != 5 || len(keptCustomers) != 1 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 5, 1, 0",
			removed, len(keptCustomers), len(keptPromos))
	}
	if keptCustomers[0].ID != "cust-001" || keptCustomers[0].Reason != KeptReasonHeld {
		t.Fatalf("keptCustomers = %+v, want [cust-001/held]", keptCustomers)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'cust-001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("customer referenced only by a held sale was removed")
	}
}

// ut-docs#640: right after a reset-transactions run, the live sales table
// is empty — the real reference to a demo customer sits in sales_archive
// instead. "Remove sample data" must keep a customer an archived batch
// still points to, the same way it already keeps one a LIVE sale or held
// sale points to.
func TestRemoveDemoCustomersPromosKeepsSaleArchiveCustomer(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('batch1','2026-01-01T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sales_archive (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts, currency, subtotal, discount_total, tax_total, total, rounding, created_at, till_id, service_charge_amount, order_type, order_status, customer_id, reset_batch_id)
	   VALUES ('sa1','R1','completed','sale','cash',0,'synced',0,'GBP',100,0,0,100,0,'2026-01-01T00:00:00Z','till1',0,'counter','completed','cust-001','batch1')`); err != nil {
		t.Fatal(err)
	}

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Kept: cust-001 (archived sale). Removed: cust-002, cust-003, PROMO50,
	// PROMO500, DISC10 = 5.
	if removed != 5 || len(keptCustomers) != 1 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 5, 1, 0",
			removed, len(keptCustomers), len(keptPromos))
	}
	if keptCustomers[0].ID != "cust-001" || keptCustomers[0].Reason != KeptReasonHistory {
		t.Fatalf("keptCustomers = %+v, want [cust-001/history]", keptCustomers)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'cust-001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("customer referenced only by an archived sale was removed")
	}
}

// Same gap on the held_sales_archive side (independent review follow-up):
// a demo customer parked in a basket that was then swept into the archive
// by a reset, before ever being tendered, must also survive.
func TestRemoveDemoCustomersPromosKeepsHeldSaleArchiveCustomer(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('batch1','2026-01-01T00:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	payload := `{"lines":[],"customer_id":"cust-001","customer_name":"Alice Carter","total":0}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales_archive (id, label, total_minor, line_count, payload, created_at, reset_batch_id)
	   VALUES ('ha1','Table 4',0,0,?,'2026-01-01T00:00:00Z','batch1')`, payload); err != nil {
		t.Fatal(err)
	}

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	if removed != 5 || len(keptCustomers) != 1 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 5, 1, 0",
			removed, len(keptCustomers), len(keptPromos))
	}
	if keptCustomers[0].ID != "cust-001" || keptCustomers[0].Reason != KeptReasonHeld {
		t.Fatalf("keptCustomers = %+v, want [cust-001/held]", keptCustomers)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'cust-001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("customer referenced only by an archived held sale was removed")
	}
}

// Independent review (ut-docs#567, F3): a demo promotion the shop has
// genuinely customized (edited value/description, without necessarily
// targeting a specific customer) must be kept, not just one with
// customer_id set — customer_id is not the only way a shop could rely on a
// promo, and the /promotions page (internal/pages/promotions_page.go) lets
// an operator edit or deactivate one without ever targeting a customer.
// This exercises the
// STRICT variant specifically (ut-docs#1858 gave promos a relaxed variant
// too, mirroring items — seedRealSale forces strict mode here, the same
// way TestRemoveDemoCatalogueKeepsEditedItemWhenTillHasRealHistory does for
// items) — see TestRemoveDemoCustomersPromosRemovesEditedPromoWhenTillHasNoRealHistory
// below for the relaxed counterpart.
func TestRemoveDemoCustomersPromosKeepsCustomizedPromotion(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedRealSale(t, d, "own-1", "s-1")
	if _, err := d.DB.Exec(`UPDATE promotions SET value = 1500, description = 'Summer 15% sale' WHERE code = 'DISC10'`); err != nil {
		t.Fatal(err)
	}

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// Kept: DISC10 (customized). Removed: cust-001/002/003, PROMO50,
	// PROMO500 = 5.
	if removed != 5 || len(keptCustomers) != 0 || len(keptPromos) != 1 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 5, 0, 1",
			removed, len(keptCustomers), len(keptPromos))
	}
	if keptPromos[0].Code != "DISC10" || keptPromos[0].Reason != KeptReasonEdited {
		t.Fatalf("keptPromos = %+v, want [DISC10/edited]", keptPromos)
	}
	var desc string
	if err := d.DB.QueryRow(`SELECT description FROM promotions WHERE code = 'DISC10'`).Scan(&desc); err != nil {
		t.Fatal("customized DISC10 was removed:", err)
	}
	if desc != "Summer 15% sale" {
		t.Fatalf("DISC10.description = %q, want the customized value intact", desc)
	}
}

// The relaxed counterpart (ut-docs#1858): on a till that has never traded
// for real, a merely-deactivated (or otherwise edited) demo promo is
// removed outright — the same relaxation TestRemoveDemoCatalogueRemovesEditedItemsWhenTillHasNoRealHistory
// already established for items.
func TestRemoveDemoCustomersPromosRemovesEditedPromoWhenTillHasNoRealHistory(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// PROMO500 deactivated — an entirely ordinary thing to do while trying
	// the till out (the exact scenario ut-docs#1858 was filed for) — and
	// DISC10 customized, exercising both kinds of "edited" promo.
	if _, err := d.DB.Exec(`UPDATE promotions SET is_active = 0 WHERE code = 'PROMO500'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET value = 1500, description = 'Summer 15% sale' WHERE code = 'DISC10'`); err != nil {
		t.Fatal(err)
	}

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	if removed != 6 || len(keptCustomers) != 0 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 6, 0, 0 (edited promos are removable when the till has never traded for real)",
			removed, len(keptCustomers), len(keptPromos))
	}
	for _, code := range []string{"PROMO500", "DISC10"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = ?`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("edited promo %s survived removal — want it gone, same as an untouched promo", code)
		}
	}
}

// Removing when nothing was ever seeded reports zeros, not an error.
func TestRemoveDemoCustomersPromosEmpty(t *testing.T) {
	d := openDemoSeedTestDB(t)
	removed, keptCustomers, keptPromos, err := NewDemoSeedRepo(d.DB).RemoveDemoCustomersPromos(context.Background())
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos on empty DB: %v", err)
	}
	if removed != 0 || len(keptCustomers) != 0 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos on empty DB = removed %d, keptCustomers %d, keptPromos %d; want 0, 0, 0",
			removed, len(keptCustomers), len(keptPromos))
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
	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 6 || len(keptCustomers) != 0 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos = removed %d, keptCustomers %d, keptPromos %d; want 6, 0, 0",
			removed, len(keptCustomers), len(keptPromos))
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
	if removed != 50 || len(kept) != 0 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 50, 0", removed, len(kept))
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
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, len(kept))
	}
	// ut-docs#1840 review finding F4: the reported reason, not just the
	// count, must be "held" — this is what routes the Settings page to
	// render the plain "in a parked sale" text instead of offering a
	// "remove anyway" button it would then have to refuse.
	if kept[0].ID != "itm003" || kept[0].Reason != KeptReasonHeld {
		t.Fatalf("kept[0] = %+v; want itm003/held", kept[0])
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
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, len(kept))
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

// ut-docs#640: right after a reset-transactions run, the live sale_lines
// table is empty — the real reference to a demo item sits in
// sale_lines_archive instead. "Remove sample data" must keep an item an
// archived batch still points to, the same way it already keeps one a LIVE
// sale_line or held sale points to.
func TestRemoveDemoCatalogueKeepsSaleArchiveItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedArchivedSaleLine(t, d, "batch1", "itm003", "")

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	// Kept: itm003 (archived sale line). Removed: the other 49 items.
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, len(kept))
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm003'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("item referenced only by an archived sale line was removed")
	}
}

// Same gap, exercised via the variant-only clause — mirrors
// TestRemoveDemoCatalogueKeepsHeldSaleVariantItem's own defense-in-depth
// rationale.
func TestRemoveDemoCatalogueKeepsSaleArchiveVariantItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// var010 belongs to itm041 (internal/data/seeddata/demo_catalogue.sql).
	seedArchivedSaleLine(t, d, "batch1", "", "var010")

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, len(kept))
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm041'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("item referenced only by an archived sale line's variant was removed")
	}
}

// Same gap on the stock_movements_archive side — a demo item stock-adjusted
// before a reset must stay kept afterward too.
func TestRemoveDemoCatalogueKeepsStockArchiveItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('batch1','2026-01-01T00:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO stock_movements_archive (id, item_id, location_id, type, quantity, created_at, reset_batch_id)
	   VALUES ('sma1','itm003','loc-main','adjustment',-1,'2026-01-01T00:00:00Z','batch1')`); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, len(kept))
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm003'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("item referenced only by an archived stock movement was removed")
	}
}

// Same gap on the held_sales_archive side (independent review follow-up):
// a demo item parked in a basket that was then swept into the archive by a
// reset, before ever being tendered, must also survive — this one is worse
// than the sale_lines/stock_movements gap above, since held_sales_archive
// carries no FK at all, so RestoreResetBatch would succeed silently and the
// shop owner would only discover the break as a raw FK failure when they
// try to tender the restored basket.
func TestRemoveDemoCatalogueKeepsHeldSaleArchiveItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('batch1','2026-01-01T00:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	payload := `{"lines":[{"sku":"SKU-0003","name":"Held Item","qty":1,"price_cents":100,"item_id":"itm003"}],"total":100}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales_archive (id, label, total_minor, line_count, payload, created_at, reset_batch_id)
	   VALUES ('ha1','Table 4',100,1,?,'2026-01-01T00:00:00Z','batch1')`, payload); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 49 || len(kept) != 1 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 49, 1", removed, len(kept))
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm003'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("item referenced only by an archived held sale was removed")
	}
}

// ut-docs#1840 AC3: "remove anyway" on a demo item kept only for
// ReasonEdited. Needs a strict-mode till (real trading history elsewhere),
// otherwise RemoveDemoCatalogue itself would already have removed the
// edited item and there'd be nothing left to demonstrate the per-item path
// on.
func TestRemoveDemoItemRemovesEditedItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedRealSale(t, d, "own-1", "s-1")
	if _, err := d.DB.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	if err := repo.RemoveDemoItem(ctx, "itm001"); err != nil {
		t.Fatalf("RemoveDemoItem: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("itm001 survived RemoveDemoItem")
	}
}

// The server-side re-check: even though the client only ever offers "remove
// anyway" for a ReasonEdited row, RemoveDemoItem must refuse an item that
// actually has trading history rather than trust the caller — the item
// could have been sold in the gap between the page rendering and the click.
func TestRemoveDemoItemRefusesItemWithHistory(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedRealSale(t, d, "own-1", "s-1")
	if _, err := d.DB.Exec(`INSERT INTO stock_movements (id, item_id, location_id, type, quantity)
		VALUES ('sm-1', 'itm001', 'loc_main', 'adjust', 3)`); err != nil {
		t.Fatal(err)
	}

	err := repo.RemoveDemoItem(ctx, "itm001")
	if !errors.Is(err, ErrDemoItemHasHistory) {
		t.Fatalf("RemoveDemoItem = %v, want ErrDemoItemHasHistory", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("itm001 was removed despite having stock-movement history")
	}
}

// A held (parked) sale is refused the same way as sold/stock-adjusted
// history — this is what "never widen the FK-safety clauses" (ut-docs#1840's
// own "Do not regress") means for the per-item path specifically.
func TestRemoveDemoItemRefusesHeldItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	payload := `{"lines":[{"sku":"SKU-0001","name":"Held Item","qty":1,"price_cents":100,"item_id":"itm001"}],"total":100}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, payload) VALUES ('h-1', 'Table 4', ?)`, payload); err != nil {
		t.Fatal(err)
	}

	err := repo.RemoveDemoItem(ctx, "itm001")
	if !errors.Is(err, ErrDemoItemHasHistory) {
		t.Fatalf("RemoveDemoItem = %v, want ErrDemoItemHasHistory", err)
	}
}

// ut-docs#1840 review finding F4: the held_sales_archive arm specifically —
// a demo item parked in a basket that was later swept into the archive by a
// reset, before ever being tendered — must refuse "remove anyway" exactly
// like a still-live held sale does. This is the sharpest version of the
// non-regression the card's own text demands: demoItemReasonCaseSQL is
// shared between the bulk kept-list AND this single-item safety re-check,
// so a bug in this specific arm would both mislabel the row "edited" in the
// list AND let "remove anyway" delete an item a restorable archive batch
// still depends on.
func TestRemoveDemoItemRefusesHeldArchiveItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('batch1','2026-01-01T00:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	payload := `{"lines":[{"sku":"SKU-0001","name":"Held Item","qty":1,"price_cents":100,"item_id":"itm001"}],"total":100}`
	if _, err := d.DB.Exec(`INSERT INTO held_sales_archive (id, label, total_minor, line_count, payload, created_at, reset_batch_id)
	   VALUES ('ha1','Table 4',100,1,?,'2026-01-01T00:00:00Z','batch1')`, payload); err != nil {
		t.Fatal(err)
	}

	err := repo.RemoveDemoItem(ctx, "itm001")
	if !errors.Is(err, ErrDemoItemHasHistory) {
		t.Fatalf("RemoveDemoItem = %v, want ErrDemoItemHasHistory", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("itm001 was removed despite being referenced by an archived held sale")
	}
}

// A non-existent (or already-removed / already non-sample) id is a clean
// not-found, not a silent no-op or a generic error.
func TestRemoveDemoItemNotFound(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.RemoveDemoItem(ctx, "does-not-exist"); !errors.Is(err, ErrDemoItemNotFound) {
		t.Fatalf("RemoveDemoItem(does-not-exist) = %v, want ErrDemoItemNotFound", err)
	}
	// A real, non-sample item id is equally "not found" from this method's
	// point of view — it only ever acts on is_sample_data = 1 rows.
	if _, err := d.DB.Exec(`INSERT INTO items (id, name, base_price) VALUES ('own-1', 'My Own Item', 250)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveDemoItem(ctx, "own-1"); !errors.Is(err, ErrDemoItemNotFound) {
		t.Fatalf("RemoveDemoItem(own-1) = %v, want ErrDemoItemNotFound (not a sample item)", err)
	}
}

// ut-docs#1840 AC3's other resolution: "keep as my own item" clears
// is_sample_data permanently — the item survives, stops counting as sample
// data, and a later RemoveDemoCatalogue run never touches it again even
// when the till has no real trading history (the case that would otherwise
// remove it outright).
func TestKeepDemoItemAsOwn(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	if err := repo.KeepDemoItemAsOwn(ctx, "itm001"); err != nil {
		t.Fatalf("KeepDemoItemAsOwn: %v", err)
	}
	var flagged int
	if err := d.DB.QueryRow(`SELECT is_sample_data FROM items WHERE id = 'itm001'`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 {
		t.Fatal("itm001 still flagged is_sample_data after KeepDemoItemAsOwn")
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	// itm001 is no longer sample data at all, so it's neither removed nor
	// kept-and-reported — it's simply outside this method's scope now,
	// exactly like any other operator-owned item.
	if removed != 49 || len(kept) != 0 {
		t.Fatalf("RemoveDemoCatalogue after KeepDemoItemAsOwn = removed %d, kept %d; want 49, 0", removed, len(kept))
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("itm001 was removed even though it was kept as the operator's own item")
	}
}

// ut-docs#1858: RemoveDemoPromo mirrors RemoveDemoItem's "remove anyway"
// resolution for a promo kept only because it was edited.
func TestRemoveDemoPromoRemovesEditedPromo(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedRealSale(t, d, "own-1", "s-1") // force strict mode
	if _, err := d.DB.Exec(`UPDATE promotions SET is_active = 0 WHERE code = 'PROMO500'`); err != nil {
		t.Fatal(err)
	}

	if err := repo.RemoveDemoPromo(ctx, "PROMO500"); err != nil {
		t.Fatalf("RemoveDemoPromo: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO500'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("PROMO500 survived RemoveDemoPromo")
	}
}

// The server-side re-check: RemoveDemoPromo must refuse a promo that is
// actually targeted at a customer, regardless of what the client believed
// when it rendered the button — the promo could have been targeted in the
// gap between the page rendering and the click.
func TestRemoveDemoPromoRefusesTargetedPromo(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET customer_id = 'cust-001' WHERE code = 'PROMO50'`); err != nil {
		t.Fatal(err)
	}

	err := repo.RemoveDemoPromo(ctx, "PROMO50")
	if !errors.Is(err, ErrDemoPromoTargeted) {
		t.Fatalf("RemoveDemoPromo = %v, want ErrDemoPromoTargeted", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO50'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("PROMO50 was removed despite being targeted at a customer")
	}
}

// A non-existent (or already-removed / already non-sample) code is a clean
// not-found, not a silent no-op or a generic error.
func TestRemoveDemoPromoNotFound(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.RemoveDemoPromo(ctx, "DOES-NOT-EXIST"); !errors.Is(err, ErrDemoPromoNotFound) {
		t.Fatalf("RemoveDemoPromo(DOES-NOT-EXIST) = %v, want ErrDemoPromoNotFound", err)
	}
	// A real, non-sample promo code is equally "not found" from this
	// method's point of view — it only ever acts on is_sample_data = 1 rows.
	if _, err := d.DB.Exec(`INSERT INTO promotions (code, type, value, is_active) VALUES ('OWNCODE', 'amount', 100, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveDemoPromo(ctx, "OWNCODE"); !errors.Is(err, ErrDemoPromoNotFound) {
		t.Fatalf("RemoveDemoPromo(OWNCODE) = %v, want ErrDemoPromoNotFound (not a sample promo)", err)
	}
}

// ut-docs#1858 "keep as my own" resolution: clears is_sample_data
// permanently — the promo survives, stops counting as sample data, and a
// later RemoveDemoCustomersPromos run never touches it again even when the
// till has no real trading history (the case that would otherwise remove
// it outright).
func TestKeepDemoPromoAsOwn(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE promotions SET is_active = 0 WHERE code = 'PROMO500'`); err != nil {
		t.Fatal(err)
	}

	if err := repo.KeepDemoPromoAsOwn(ctx, "PROMO500"); err != nil {
		t.Fatalf("KeepDemoPromoAsOwn: %v", err)
	}
	var flagged int
	if err := d.DB.QueryRow(`SELECT is_sample_data FROM promotions WHERE code = 'PROMO500'`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 {
		t.Fatal("PROMO500 still flagged is_sample_data after KeepDemoPromoAsOwn")
	}

	removed, keptCustomers, keptPromos, err := repo.RemoveDemoCustomersPromos(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCustomersPromos: %v", err)
	}
	// PROMO500 is no longer sample data at all, so it's neither removed nor
	// kept-and-reported — it's simply outside this method's scope now,
	// exactly like any other operator-owned promo.
	if removed != 5 || len(keptCustomers) != 0 || len(keptPromos) != 0 {
		t.Fatalf("RemoveDemoCustomersPromos after KeepDemoPromoAsOwn = removed %d, keptCustomers %d, keptPromos %d; want 5, 0, 0",
			removed, len(keptCustomers), len(keptPromos))
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO500'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("PROMO500 was removed even though it was kept as the operator's own promo")
	}
}

func TestKeepDemoPromoAsOwnNotFound(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCustomersPromos(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.KeepDemoPromoAsOwn(ctx, "DOES-NOT-EXIST"); !errors.Is(err, ErrDemoPromoNotFound) {
		t.Fatalf("KeepDemoPromoAsOwn(DOES-NOT-EXIST) = %v, want ErrDemoPromoNotFound", err)
	}
}

func TestKeepDemoItemAsOwnNotFound(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.KeepDemoItemAsOwn(ctx, "does-not-exist"); !errors.Is(err, ErrDemoItemNotFound) {
		t.Fatalf("KeepDemoItemAsOwn(does-not-exist) = %v, want ErrDemoItemNotFound", err)
	}
}

// keptDemoItems' reason priority: an item that is BOTH edited AND has
// trading history reports "history", not "edited" — the harder blocker
// wins, since "edited" is the only one of the three a merchant can act on
// via "remove anyway."
func TestRemoveDemoCatalogueKeptReasonPriorityHistoryOverEdited(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedRealSale(t, d, "own-1", "s-1") // force strict mode
	if _, err := d.DB.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO stock_movements (id, item_id, location_id, type, quantity)
		VALUES ('sm-1', 'itm001', 'loc_main', 'adjust', 3)`); err != nil {
		t.Fatal(err)
	}

	_, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if len(kept) != 1 || kept[0].ID != "itm001" || kept[0].Reason != KeptReasonHistory {
		t.Fatalf("kept = %+v; want exactly itm001/history", kept)
	}
}

// seedRealSale inserts one minimal real (non-sample) item and sale line —
// the shared setup several tests above use purely to flip
// demoTillHasNoRealHistorySQL to "has real history" (strict mode), when the
// specific item/line ids don't matter to the test itself.
func seedRealSale(t *testing.T, d *db.DB, itemID, saleID string) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO items (id, name, base_price) VALUES (?, 'My Own Item', 250)`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES (?, 'R-1', 250, 250)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES (?, ?, 1, ?, 'My Own Item', 1, 250, 0, 0, 250, 250)`, saleID+"-sl", saleID, itemID); err != nil {
		t.Fatal(err)
	}
}

// seedArchivedSaleLine inserts one reset_batches row plus one minimal
// sale_lines_archive row referencing either itemID or variantID (never
// both — mirrors sale_lines_archive's own CHECK constraint). Archive tables
// carry no FK to live tables (migration 040's own header comment), so this
// needs no real prior sale or reset to set up.
func seedArchivedSaleLine(t *testing.T, d *db.DB, batchID, itemID, variantID string) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES (?, '2026-01-01T00:00:00Z', 0)`, batchID); err != nil {
		t.Fatal(err)
	}
	var item, variant any
	if itemID != "" {
		item = itemID
	}
	if variantID != "" {
		variant = variantID
	}
	if _, err := d.DB.Exec(`INSERT INTO sale_lines_archive
	   (id, sale_id, line_no, item_id, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, reset_batch_id)
	   VALUES ('sla1', 'sale-x', 1, ?, ?, 'Archived snapshot', 1, 100, 0, 0, 0, 100, 100, ?)`,
		item, variant, batchID); err != nil {
		t.Fatal(err)
	}
}
