package data

import (
	"context"
	"testing"
)

// D3b stock-level sync ("does stock sync between tills — so important"):
// the replica's on-hand converges to the primary's shop-wide aggregate via
// corrective adjust movements, idempotently, including keys the primary
// doesn't have (corrected to zero) and variant-level rows.
func TestStockLevelSyncConverges(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	pRepo, rRepo := NewPOSRepo(primary.DB), NewPOSRepo(replica.DB)

	pLoc, err := pRepo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatalf("primary location: %v", err)
	}
	rLoc, err := rRepo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatalf("replica location: %v", err)
	}

	// Same catalog on both tills (the admin bundle sync guarantees this).
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola', 120)`)
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm2', 'BREAD', 'Bread', 110)`)
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('v1', 'itm1', 'COLA-L', 'Large', 200)`)
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola', 120)`)
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm2', 'BREAD', 'Bread', 110)`)
	mustExec(t, replica, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('v1', 'itm1', 'COLA-L', 'Large', 200)`)

	move := func(repo *POSRepo, loc, item, variant string, qty float64) {
		t.Helper()
		if _, err := repo.RecordStockMovement(ctx, nil, StockMovementInput{
			ItemID: item, VariantID: variant, LocationID: loc, Type: "receive", Quantity: qty,
		}); err != nil {
			t.Fatalf("movement: %v", err)
		}
	}
	// Primary truth: cola 40 (other tills' sales already journaled there),
	// bread 12, variant Large 7.
	move(pRepo, pLoc, "itm1", "", 40)
	move(pRepo, pLoc, "itm2", "", 12)
	move(pRepo, pLoc, "", "v1", 7)
	// Replica's drifted view: cola 50 (never saw the primary's sales),
	// bread absent, variant 9, plus a local-only item the primary zeroed out.
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm9', 'GONE', 'Ghost', 100)`)
	move(rRepo, rLoc, "itm1", "", 50)
	move(rRepo, rLoc, "", "v1", 9)
	move(rRepo, rLoc, "itm9", "", 3)

	bundle, err := NewSyncStockRepo(pRepo).DumpStock(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if bundle.Fingerprint() == "" {
		t.Fatal("empty fingerprint")
	}

	rSync := NewSyncStockRepo(rRepo)
	n, err := rSync.ApplyStockLevels(ctx, bundle, rLoc)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n != 4 { // cola -10, bread +12, variant -2, ghost -3
		t.Fatalf("corrections = %d, want 4", n)
	}

	after, err := rSync.DumpStock(ctx)
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	get := func(item, variant string) float64 {
		for _, r := range after.Rows {
			if r.ItemID == item && r.VariantID == variant {
				return r.Quantity
			}
		}
		return 0
	}
	if get("itm1", "") != 40 || get("itm2", "") != 12 || get("", "v1") != 7 || get("itm9", "") != 0 {
		t.Fatalf("replica did not converge: %+v", after.Rows)
	}

	// Idempotent: same bundle again = zero corrections.
	n2, err := rSync.ApplyStockLevels(ctx, bundle, rLoc)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("re-apply corrections = %d, want 0", n2)
	}

	// The corrections are honest ledger entries, not silent writes.
	var adjustCount int
	_ = replica.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE type = 'adjust'`).Scan(&adjustCount)
	if adjustCount != 4 {
		t.Fatalf("adjust movements = %d, want 4", adjustCount)
	}
}
