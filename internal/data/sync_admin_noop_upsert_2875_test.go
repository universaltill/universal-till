package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#2875: ApplyAdmin used an unconditional INSERT … ON CONFLICT DO
// UPDATE, and SQLite fires AFTER UPDATE triggers even when every value is
// identical. So re-applying an unchanged admin bundle bumped
// sync_admin_version and sell_screen_version once per row, and every
// replica's open sale screen refreshed its tile grid for nothing. The upsert
// now carries a WHERE that only updates a row whose values differ.

// seedNoopPrimary gives the primary a catalog that exercises every upsert
// shape: plain columns, NULLs, a sticky column (item_variants.sku, with a
// codeless variant the replica backfills), a redacted column (tills) and a
// shop-wide setting.
func seedNoopPrimary(t *testing.T, primary *db.DB) {
	t.Helper()
	mustExec(t, primary, `INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`)
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price, category_id) VALUES ('itm1', 'COLA', 'Cola Can', 120, 'cat1')`)
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm2', 'TEA', 'Tea', 250)`)
	mustExec(t, primary, `INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('500123', 'itm1', 1)`)
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless', 'itm2', NULL, 'Small', 200, 1)`)
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-coded', 'itm2', 'TEA-L', 'Large', 300, 1)`)
	mustExec(t, primary, `INSERT INTO users (id, username, display_name, role) VALUES ('u1', 'jo', 'Jo', 'cashier')`)
	mustExec(t, primary, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Front Counter', 'realhash-a')`)
	mustExec(t, primary, `INSERT INTO settings (key, value) VALUES ('shop.name', 'Corner Shop')`)
}

func TestApplyAdmin_IdenticalBundleTwiceMovesNoVersion(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedNoopPrimary(t, primary)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	rrepo := NewSyncAdminRepo(replica.DB)
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	adminBefore, sellBefore := syncAdminGeneration(t, replica), sellScreenGeneration(t, replica)

	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := syncAdminGeneration(t, replica); got != adminBefore {
		t.Errorf("sync_admin_version %d -> %d on an identical bundle, want unchanged", adminBefore, got)
	}
	if got := sellScreenGeneration(t, replica); got != sellBefore {
		t.Errorf("sell_screen_version %d -> %d on an identical bundle, want unchanged", sellBefore, got)
	}
}

func TestApplyAdmin_SettingOnlyChangeMovesAdminNotSellVersion(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedNoopPrimary(t, primary)

	prepo := NewSyncAdminRepo(primary.DB)
	rrepo := NewSyncAdminRepo(replica.DB)
	bundle, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	adminBefore, sellBefore := syncAdminGeneration(t, replica), sellScreenGeneration(t, replica)

	mustExec(t, primary, `UPDATE settings SET value = 'Corner Shop & Cafe' WHERE key = 'shop.name'`)
	changed, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump after setting change: %v", err)
	}
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, changed)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := sellScreenGeneration(t, replica); got != sellBefore {
		t.Errorf("sell_screen_version %d -> %d on a setting-only change, want unchanged", sellBefore, got)
	}
	if got := syncAdminGeneration(t, replica); got == adminBefore {
		t.Errorf("sync_admin_version stayed %d after a real setting change, want it to move", got)
	}
	var name string
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'shop.name'`).Scan(&name); err != nil || name != "Corner Shop & Cafe" {
		t.Fatalf("replica shop.name = %q (err %v), want the new value", name, err)
	}
}

// A real catalog change still lands and still moves both counters, and a
// redacted column holding a local value is still scrubbed (its SET is
// `= NULL`, so the WHERE must see that as a change).
func TestApplyAdmin_RealChangesStillApply(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedNoopPrimary(t, primary)

	prepo := NewSyncAdminRepo(primary.DB)
	rrepo := NewSyncAdminRepo(replica.DB)
	bundle, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	mustExec(t, replica, `UPDATE tills SET bearer_hash = 'leaked' WHERE id = 'till-a'`)
	adminBefore, sellBefore := syncAdminGeneration(t, replica), sellScreenGeneration(t, replica)

	mustExec(t, primary, `UPDATE items SET base_price = 130 WHERE id = 'itm1'`)
	changed, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump after price change: %v", err)
	}
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, changed)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var price int64
	if err := replica.QueryRow(`SELECT base_price FROM items WHERE id = 'itm1'`).Scan(&price); err != nil || price != 130 {
		t.Fatalf("replica itm1 base_price = %d (err %v), want 130", price, err)
	}
	var hash *string
	if err := replica.QueryRow(`SELECT bearer_hash FROM tills WHERE id = 'till-a'`).Scan(&hash); err != nil || hash != nil {
		t.Fatalf("replica till-a bearer_hash = %v (err %v), want NULL", hash, err)
	}
	if got := sellScreenGeneration(t, replica); got == sellBefore {
		t.Errorf("sell_screen_version stayed %d after a price change, want it to move", got)
	}
	if got := syncAdminGeneration(t, replica); got == adminBefore {
		t.Errorf("sync_admin_version stayed %d after a price change, want it to move", got)
	}
}

// A row the primary deleted but this replica can't (local sales history
// FK-blocks the DELETE) is retired in place once. Later applies must leave
// the already-retired row alone; before, the retire UPDATE ran again on
// every pull and moved both counters.
func TestApplyAdmin_AlreadyRetiredRowMovesNoVersion(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	seedNoopPrimary(t, primary)

	prepo := NewSyncAdminRepo(primary.DB)
	rrepo := NewSyncAdminRepo(replica.DB)
	bundle, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	mustExec(t, replica, `INSERT INTO stock_locations (id, name) VALUES ('loc-local', 'Local Store')`)
	mustExec(t, replica, `INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('mv-1', 'itm1', 'loc-local', 'sale', -1)`)

	mustExec(t, primary, `DELETE FROM item_barcodes WHERE item_id = 'itm1'`)
	mustExec(t, primary, `DELETE FROM items WHERE id = 'itm1'`)
	deleted, err := prepo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump after delete: %v", err)
	}
	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, deleted)); err != nil {
		t.Fatalf("retire apply: %v", err)
	}
	var active int
	var sku string
	if err := replica.QueryRow(`SELECT is_active, sku FROM items WHERE id = 'itm1'`).Scan(&active, &sku); err != nil {
		t.Fatalf("itm1 should be retired in place, not deleted: %v", err)
	}
	if active != 0 || sku != "COLA~itm1" {
		t.Fatalf("itm1 is_active=%d sku=%q, want 0 and COLA~itm1", active, sku)
	}
	adminBefore, sellBefore := syncAdminGeneration(t, replica), sellScreenGeneration(t, replica)

	if err := rrepo.ApplyAdmin(ctx, wireTrip(t, deleted)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := syncAdminGeneration(t, replica); got != adminBefore {
		t.Errorf("sync_admin_version %d -> %d re-retiring an already-retired row, want unchanged", adminBefore, got)
	}
	if got := sellScreenGeneration(t, replica); got != sellBefore {
		t.Errorf("sell_screen_version %d -> %d re-retiring an already-retired row, want unchanged", sellBefore, got)
	}
}
