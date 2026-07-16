package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// The catalog page shows each item's barcodes and variants (each variant can
// carry its own barcode). This guards the queries that feed that display.
func TestItemBarcodesAndVariants(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	ex := func(q string, args ...any) {
		if _, err := d.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it1','SKU1','Coffee',300,1,0,'each')`)
	ex(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('it1','111',1),('it1','222',0)`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v1','it1','Large',400,1)`)
	ex(`INSERT INTO variant_barcodes (variant_id, barcode, is_primary) VALUES ('v1','999',1)`)

	repo := data.NewCatalogRepo(d.DB)

	bc, err := repo.ItemBarcodes(ctx)
	if err != nil {
		t.Fatalf("ItemBarcodes: %v", err)
	}
	if got := bc["it1"]; len(got) != 2 || got[0] != "111" { // primary first
		t.Fatalf("item barcodes = %v, want [111 222]", got)
	}

	vs, err := repo.ItemVariants(ctx)
	if err != nil {
		t.Fatalf("ItemVariants: %v", err)
	}
	got := vs["it1"]
	if len(got) != 1 || got[0].Name != "Large" || got[0].Barcode != "999" {
		t.Fatalf("variants = %+v, want [{Large 999}]", got)
	}
}
