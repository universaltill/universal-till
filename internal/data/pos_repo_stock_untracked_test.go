package data

import (
	"context"
	"testing"
)

// TestPOSRepo_UntrackedByKey covers ut-docs#1850: resolving whether an item
// or variant is stock-tracked, by item id, by variant id (resolved through
// its parent item), and the fail-safe default (missing key => tracked)
// for an id that matches nothing.
func TestPOSRepo_UntrackedByKey(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	// itmA/varC(->itmA) are seeded tracked by default; add an explicitly
	// untracked item alongside them.
	mustExec(t, dbo, `INSERT INTO items(id, sku, name, base_price, stock_untracked) VALUES('itmU', 'U', 'Untracked Item', 300, 1)`)
	mustExec(t, dbo, `INSERT INTO item_variants(id, item_id, name, price) VALUES('varU', 'itmU', 'Untracked Variant', 350)`)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	keys := []StockTrackKey{
		{ItemID: "itmA"},    // tracked (default)
		{VariantID: "varC"}, // tracked, resolved through parent itmA
		{ItemID: "itmU"},    // explicitly untracked
		{VariantID: "varU"}, // untracked, resolved through parent itmU
		{ItemID: "itmA"},    // duplicate, must be tolerated
	}
	got, err := repo.UntrackedByKey(ctx, nil, keys)
	if err != nil {
		t.Fatalf("UntrackedByKey: %v", err)
	}
	if untracked := got[StockTrackKey{ItemID: "itmA"}]; untracked {
		t.Errorf("itmA: got untracked=true, want false (tracked)")
	}
	if untracked := got[StockTrackKey{VariantID: "varC"}]; untracked {
		t.Errorf("varC: got untracked=true, want false (tracked, via parent itmA)")
	}
	if untracked := got[StockTrackKey{ItemID: "itmU"}]; !untracked {
		t.Errorf("itmU: got untracked=false, want true")
	}
	if untracked := got[StockTrackKey{VariantID: "varU"}]; !untracked {
		t.Errorf("varU: got untracked=false, want true (via parent itmU)")
	}

	// A key matching nothing is simply absent — the caller's job to treat
	// that as tracked=true, not this method's.
	if _, ok := got[StockTrackKey{ItemID: "does-not-exist"}]; ok {
		t.Errorf("expected unknown id to be absent from the map")
	}

	if _, err := repo.UntrackedByKey(ctx, nil, []StockTrackKey{{}}); err == nil {
		t.Errorf("expected error for a key with neither ItemID nor VariantID set")
	}
	if _, err := repo.UntrackedByKey(ctx, nil, []StockTrackKey{{ItemID: "itmA", VariantID: "varC"}}); err == nil {
		t.Errorf("expected error for a key with both ItemID and VariantID set")
	}
}
