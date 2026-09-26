package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2785: one pull's ledger changes land in a single transaction —
// records (which clear a miss), misses (counted, first-miss time kept) and
// deletes.
func TestSyncAssetLedger_ApplyBatch(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewSyncAssetLedgerRepo(dbo.DB)

	if err := repo.Apply(ctx, "items", SyncAssetLedgerBatch{Record: []SyncAssetLedgerRow{
		{Path: "a.png", Size: 1, Mod: 10}, {Path: "b.png", Size: 2, Mod: 20}, {Path: "c.png", Size: 3, Mod: 30},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Apply(ctx, "items", SyncAssetLedgerBatch{MissAt: 100, Miss: []string{"a.png", "b.png"}, Delete: []string{"c.png"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Apply(ctx, "items", SyncAssetLedgerBatch{MissAt: 200, Miss: []string{"a.png"}, Record: []SyncAssetLedgerRow{{Path: "b.png", Size: 2, Mod: 20}}}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.List(ctx, "items")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected c.png deleted, got %v", rows)
	}
	if a := rows["a.png"]; a.Misses != 2 || a.UnreferencedSince != 100 {
		t.Fatalf("a.png: want 2 misses since 100 (first miss kept), got %+v", a)
	}
	if b := rows["b.png"]; b.Misses != 0 || b.UnreferencedSince != 0 {
		t.Fatalf("b.png: a relisting must reset both the miss count and the time, got %+v", b)
	}
}
