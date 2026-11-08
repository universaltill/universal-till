package data

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// stockDriftFixture seeds a primary holding 12 of itm1 and a replica holding
// 3 of it. It returns the replica DB and location, and the primary's bundle,
// which differs from the replica's local levels.
func stockDriftFixture(t *testing.T) (*db.DB, string, StockBundle) {
	t.Helper()
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openReplicaDB(t, "replica.db")
	pRepo, rRepo := NewPOSRepo(primary.DB), NewPOSRepo(replica.DB)
	pLoc, err := pRepo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rLoc, err := rRepo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*db.DB{primary, replica} {
		mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola', 120)`)
	}
	rec := func(repo *POSRepo, loc string, qty float64) {
		t.Helper()
		if _, err := repo.RecordStockMovement(ctx, nil, StockMovementInput{ItemID: "itm1", LocationID: loc, Type: "receive", Quantity: qty}); err != nil {
			t.Fatal(err)
		}
	}
	rec(pRepo, pLoc, 12)
	rec(rRepo, rLoc, 3)
	bundle, err := NewSyncStockRepo(pRepo).DumpStock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	local, err := NewSyncStockRepo(rRepo).DumpStock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(bundle, local) {
		t.Fatal("precondition: bundle must differ from the replica's local levels")
	}
	return replica, rLoc, bundle
}

func rowCount5903(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ut-docs#5903: stock corrections must not commit once promote has.
func TestApplyStockLevels_RefusesAfterPromote_5903(t *testing.T) {
	ctx := context.Background()
	replica, rLoc, bundle := stockDriftFixture(t)
	repo := NewSyncStockRepo(NewPOSRepo(replica.DB))
	before, err := repo.DumpStock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	moves := rowCount5903(t, replica, "stock_movements")

	if err := NewSettingsRepo(replica.DB).ClearReplicaIdentity(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	audits := rowCount5903(t, replica, "audit_log") // promote may audit; compare from here
	n, err := repo.ApplyStockLevels(ctx, bundle, rLoc, "v1")
	if !errors.Is(err, ErrNotReplica) {
		t.Fatalf("err = %v, want ErrNotReplica", err)
	}
	if n != 0 {
		t.Fatalf("corrections = %d, want 0", n)
	}
	if got := rowCount5903(t, replica, "stock_movements"); got != moves {
		t.Fatalf("stock_movements %d -> %d", moves, got)
	}
	if got := rowCount5903(t, replica, "audit_log"); got != audits {
		t.Fatalf("audit_log %d -> %d", audits, got)
	}
	after, err := repo.DumpStock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("stock levels changed after promote: %+v -> %+v", before, after)
	}
	if v, ok := settingPresent(t, NewSettingsRepo(replica.DB), "sync.stock_version"); ok {
		t.Fatalf("sync.stock_version written after promote: %q", v)
	}
}

func TestApplyStockLevels_CommitsVersionWithCorrections_5903(t *testing.T) {
	ctx := context.Background()
	replica, rLoc, bundle := stockDriftFixture(t)
	repo := NewSyncStockRepo(NewPOSRepo(replica.DB))
	settings := NewSettingsRepo(replica.DB)
	n, err := repo.ApplyStockLevels(ctx, bundle, rLoc, "v1")
	if err != nil || n != 1 {
		t.Fatalf("apply = %d, %v; want 1, nil", n, err)
	}
	if v, _ := settingPresent(t, settings, "sync.stock_version"); v != "v1" {
		t.Fatalf("sync.stock_version = %q, want v1", v)
	}
	n, err = repo.ApplyStockLevels(ctx, bundle, rLoc, "v2")
	if err != nil || n != 0 {
		t.Fatalf("re-apply = %d, %v; want 0, nil", n, err)
	}
	if v, _ := settingPresent(t, settings, "sync.stock_version"); v != "v2" {
		t.Fatalf("sync.stock_version = %q, want v2", v)
	}
}
