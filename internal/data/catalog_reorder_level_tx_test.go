package data

import (
	"testing"
)

// ut-docs#2084: the import sets reorder_level inside its per-row tx.
func TestSetItemReorderLevelTx_RollsBackWithTx(t *testing.T) {
	dbo, _ := openB8InvDB(t)
	db := dbo.DB
	repo := NewCatalogRepo(db)
	ctx := t.Context()
	if _, err := db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('i1','S1','One',100,1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemReorderLevelTx(ctx, tx, "i1", 9); err != nil {
		t.Fatalf("set in tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ItemReorderLevel(ctx, "i1"); got != 0 {
		t.Errorf("after rollback = %d, want 0", got)
	}
	tx, _ = db.BeginTx(ctx, nil)
	if err := repo.SetItemReorderLevelTx(ctx, tx, "i1", 9); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ItemReorderLevel(ctx, "i1"); got != 9 {
		t.Errorf("after commit = %d, want 9", got)
	}
	tx, _ = db.BeginTx(ctx, nil)
	defer tx.Rollback()
	if err := repo.SetItemReorderLevelTx(ctx, tx, "", 1); err == nil {
		t.Error("empty id must error")
	}
}
