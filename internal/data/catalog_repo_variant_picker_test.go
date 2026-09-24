package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2209: the sale-screen variant picker needs the same batch
// "which of these items need a picker step" query ItemIDsWithModifiers
// already provides for modifiers — TestButtonStoreLoad_* (internal/ui)
// wires the two flags together on the button-grid VM, but the query
// itself is proven here, mirroring TestModifierRepo_ItemIDsWithModifiers
// (batch7_stragglers_test.go) exactly.
func TestCatalogRepo_ItemIDsWithVariants(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "variants-batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewCatalogRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-sized','SKU-S','Coffee',300,1)`)
	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-retired','SKU-R','Old Cup',300,1)`)
	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-plain','SKU-P','Water',100,1)`)

	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-small','itm-sized','SKU-S-S','Small',250,1)`)
	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-large','itm-sized','SKU-S-L','Large',350,1)`)
	// itm-retired's only variant is INACTIVE — must not count as "has variants".
	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-old','itm-retired','SKU-R-O','Old Size',300,0)`)

	// Empty input: nothing to look up, and no SQL "IN ()" syntax error.
	got, err := repo.ItemIDsWithVariants(ctx, nil)
	if err != nil {
		t.Fatalf("ItemIDsWithVariants(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result for empty input, got %#v", got)
	}

	got, err = repo.ItemIDsWithVariants(ctx, []string{"itm-sized", "itm-retired", "itm-plain", "itm-ghost"})
	if err != nil {
		t.Fatalf("ItemIDsWithVariants: %v", err)
	}
	if !got["itm-sized"] {
		t.Fatal("item with an active variant must be flagged")
	}
	for _, id := range []string{"itm-retired", "itm-plain", "itm-ghost"} {
		if got[id] {
			t.Fatalf("%s must not be flagged: %#v", id, got)
		}
	}
}

// A retired-but-present variant must not leak into the flagged set even
// when it is the ONLY variant an item has (regression for a query that
// forgot the is_active filter entirely).
func TestCatalogRepo_ItemIDsWithVariants_NoActiveVariantAtAllIsAbsent(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "variants-batch2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewCatalogRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-only-retired','SKU-X','Mug',300,1)`)
	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-x','itm-only-retired','SKU-X-1','Big',300,0)`)

	got, err := repo.ItemIDsWithVariants(ctx, []string{"itm-only-retired"})
	if err != nil {
		t.Fatal(err)
	}
	if got["itm-only-retired"] {
		t.Fatal("an item whose only variant is retired must not be flagged")
	}
}

// TestCatalogRepo_ItemIDsWithVariants_WhitespaceOnlySKUIsNotResolvable is
// ut-docs#2247's convergence fix: this method used to treat a
// whitespace-only sku as a resolvable code (COALESCE(v.sku,”) <> ”),
// disagreeing with migration 028/backfillCodelessSyncedVariants' own
// TRIM-based "codeless" definition. An item whose only active variant has
// a whitespace-only sku and no barcode must NOT be flagged as having a
// sellable variant — same as one whose sku is genuinely NULL/blank.
func TestCatalogRepo_ItemIDsWithVariants_WhitespaceOnlySKUIsNotResolvable(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "variants-whitespace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewCatalogRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-ws','SKU-WS','Ghost Pepper',300,1)`)
	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-ws','itm-ws','   ','Whitespace',250,1)`)

	got, err := repo.ItemIDsWithVariants(ctx, []string{"itm-ws"})
	if err != nil {
		t.Fatal(err)
	}
	if got["itm-ws"] {
		t.Fatal("an item whose only active variant has a whitespace-only sku and no barcode must not be flagged as sellable")
	}
}
