package data_test

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// save_item_variant (ut-docs#3477): the repository write path behind the
// directive — create with the cloud-minted id, update with price history,
// full variant-barcode-set replace, deactivate, idempotent replay, all in
// one transaction.

func TestSaveVariant_CreateWithIDEveryField(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	res, err := f.catalog.SaveVariant(ctx, data.VariantSave{
		ItemID: "itm1", ID: "var-new", Create: true, Name: strp("Large"), SKU: strp("FW-L"),
		PriceMinor: i64p(380), Barcodes: idsp("5000000000017", "VAR-BC-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Name != "Large" {
		t.Fatalf("result = %+v, want created Large", res)
	}
	got := f.str(t, `SELECT item_id || '|' || name || '|' || sku || '|' || price || '|' || is_active FROM item_variants WHERE id = 'var-new'`)
	if got != "itm1|Large|FW-L|380|1" {
		t.Fatalf("variant row = %s", got)
	}
	if bc := f.col(t, `SELECT barcode FROM variant_barcodes WHERE variant_id = 'var-new' ORDER BY is_primary DESC, barcode`); !reflect.DeepEqual(bc, sl("5000000000017", "VAR-BC-2")) {
		t.Fatalf("barcodes = %v, want first listed primary", bc)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM inventory WHERE variant_id = 'var-new'`); n != "1" {
		t.Fatalf("inventory rows for the new variant = %s, want 1", n)
	}
}

func TestSaveVariant_CreateBlankSKUGeneratesOneAndReplayKeepsIt(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	p := data.VariantSave{ItemID: "itm1", ID: "var-auto", Create: true, Name: strp("Small"), SKU: strp(""), PriceMinor: i64p(290)}
	if _, err := f.catalog.SaveVariant(ctx, p); err != nil {
		t.Fatal(err)
	}
	sku := f.str(t, `SELECT sku FROM item_variants WHERE id = 'var-auto'`)
	if !regexp.MustCompile(`^VAR-[0-9A-F]{8}$`).MatchString(sku) {
		t.Fatalf("generated sku = %q, want VAR-XXXXXXXX", sku)
	}
	// The cloud re-serves a create whose result post was lost.
	res, err := f.catalog.SaveVariant(ctx, p)
	if err != nil {
		t.Fatalf("replayed create: %v", err)
	}
	if res.Created {
		t.Fatal("replayed create reported Created again")
	}
	if again := f.str(t, `SELECT sku FROM item_variants WHERE id = 'var-auto'`); again != sku {
		t.Fatalf("replay changed the generated sku %s -> %s", sku, again)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM item_variants WHERE item_id = 'itm1'`); n != "1" {
		t.Fatalf("variants after replay = %s, want 1", n)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM inventory WHERE variant_id = 'var-auto'`); n != "1" {
		t.Fatalf("inventory rows after replay = %s, want 1", n)
	}
}

func TestSaveVariant_UpdateRecordsPriceHistoryOnceAndReplacesBarcodes(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), PriceMinor: i64p(380), Barcodes: idsp("OLD-1", "OLD-2")}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the replay must not append a second history row
		res, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Name: strp("Grande"), SKU: strp("FW-G"), PriceMinor: i64p(420), Barcodes: idsp("NEW-1", "OLD-2")})
		if err != nil {
			t.Fatal(err)
		}
		if res.Created || res.Name != "Grande" {
			t.Fatalf("update result = %+v", res)
		}
	}
	if got := f.str(t, `SELECT name || '|' || sku || '|' || price FROM item_variants WHERE id = 'var1'`); got != "Grande|FW-G|420" {
		t.Fatalf("variant row = %s", got)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM price_history WHERE variant_id = 'var1'`); n != "1" {
		t.Fatalf("price_history rows = %s, want exactly 1 (one change, one replay)", n)
	}
	if bc := f.col(t, `SELECT barcode FROM variant_barcodes WHERE variant_id = 'var1' ORDER BY is_primary DESC, barcode`); !reflect.DeepEqual(bc, sl("NEW-1", "OLD-2")) {
		t.Fatalf("barcodes = %v, want the full new set with NEW-1 primary", bc)
	}
	// Absent barcodes keep the set; an empty list clears it.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Name: strp("Grande")}); err != nil {
		t.Fatal(err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM variant_barcodes WHERE variant_id = 'var1'`); n != "2" {
		t.Fatalf("absent barcodes changed the set: %s rows", n)
	}
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Barcodes: idsp()}); err != nil {
		t.Fatal(err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM variant_barcodes WHERE variant_id = 'var1'`); n != "0" {
		t.Fatalf("empty list left %s barcodes", n)
	}
}

func TestSaveVariant_DeactivateAndReactivateNeverDeletes(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), PriceMinor: i64p(380), Barcodes: idsp("VB-1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Active: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if a := f.str(t, `SELECT is_active FROM item_variants WHERE id = 'var1'`); a != "0" {
		t.Fatalf("deactivate: is_active = %s", a)
	}
	// An inactive variant is still editable, barcodes included.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Barcodes: idsp("VB-2")}); err != nil {
		t.Fatalf("edit inactive variant: %v", err)
	}
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT is_active FROM item_variants WHERE id = 'var1'`); got != "1" {
		t.Fatalf("reactivate: is_active = %s", got)
	}
}

func TestSaveVariant_BarcodeConflictNamesOwnerAndWritesNothing(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary) VALUES ('TAKEN-1', 'itm-nocat', 'CODE128', 1)`)
	_, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), PriceMinor: i64p(380), Barcodes: idsp("FREE-1", "TAKEN-1")})
	var conflict *data.BarcodeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a *BarcodeConflictError", err)
	}
	if conflict.TargetType != "item" || conflict.TargetID != "itm-nocat" || conflict.Barcode != "TAKEN-1" {
		t.Fatalf("conflict = %+v", conflict)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM item_variants WHERE id = 'var1'`); n != "0" {
		t.Fatal("a failed create left the variant behind (not one transaction)")
	}
	if n := f.str(t, `SELECT COUNT(*) FROM variant_barcodes WHERE barcode = 'FREE-1'`); n != "0" {
		t.Fatal("a failed create left a barcode behind")
	}
	// Another variant's barcode conflicts too.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm-nocat", ID: "var-other", Create: true, Name: strp("Pot"), PriceMinor: i64p(500), Barcodes: idsp("VAR-TAKEN")}); err != nil {
		t.Fatal(err)
	}
	_, err = f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), PriceMinor: i64p(380), Barcodes: idsp("VAR-TAKEN")})
	if !errors.As(err, &conflict) || conflict.TargetType != "variant" || conflict.TargetID != "var-other" {
		t.Fatalf("err = %v, want a conflict with var-other", err)
	}
}

func TestSaveVariant_SKUTakenNamesOwner(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	// An item's SKU.
	_, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), SKU: strp("SKU2"), PriceMinor: i64p(380)})
	if !errors.Is(err, data.ErrSKUExists) || !strings.Contains(err.Error(), "Loose Tea") {
		t.Fatalf("err = %v, want ErrSKUExists naming Loose Tea", err)
	}
	// Another variant's SKU.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm-nocat", ID: "var-pot", Create: true, Name: strp("Pot"), SKU: strp("LT-POT"), PriceMinor: i64p(500)}); err != nil {
		t.Fatal(err)
	}
	_, err = f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), SKU: strp("LT-POT"), PriceMinor: i64p(380)})
	if !errors.Is(err, data.ErrSKUExists) || !strings.Contains(err.Error(), "Loose Tea Pot") {
		t.Fatalf("err = %v, want ErrSKUExists naming Loose Tea Pot", err)
	}
}

func TestSaveVariant_Validation(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), PriceMinor: i64p(380)}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		p    data.VariantSave
		want string
	}{
		{"missing id", data.VariantSave{ItemID: "itm1", Name: strp("X")}, "missing variant_id"},
		{"missing item", data.VariantSave{ID: "var1", Name: strp("X")}, "missing item_id"},
		{"blank name", data.VariantSave{ItemID: "itm1", ID: "var1", Name: strp("  ")}, "name must not be blank"},
		{"negative price", data.VariantSave{ItemID: "itm1", ID: "var1", PriceMinor: i64p(-1)}, "must not be negative"},
		{"huge price", data.VariantSave{ItemID: "itm1", ID: "var1", PriceMinor: i64p(1_000_000_000)}, "at most"},
		{"blank sku on update", data.VariantSave{ItemID: "itm1", ID: "var1", SKU: strp("")}, "sku must not be blank"},
		{"bad barcode", data.VariantSave{ItemID: "itm1", ID: "var1", Barcodes: idsp("has space")}, "printable"},
		{"twice", data.VariantSave{ItemID: "itm1", ID: "var1", Barcodes: idsp("A-1", "A-1")}, "listed twice"},
		{"unknown variant", data.VariantSave{ItemID: "itm1", ID: "var-nope", Name: strp("X")}, "not found"},
		{"wrong item", data.VariantSave{ItemID: "itm-nocat", ID: "var1", Name: strp("X")}, "belongs to another item"},
		{"unknown item", data.VariantSave{ItemID: "itm-nope", ID: "var2", Create: true, Name: strp("X"), PriceMinor: i64p(1)}, "does not exist"},
		{"create needs name", data.VariantSave{ItemID: "itm1", ID: "var2", Create: true, PriceMinor: i64p(1)}, "needs a name"},
		{"create needs price", data.VariantSave{ItemID: "itm1", ID: "var2", Create: true, Name: strp("X")}, "needs a price"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.catalog.SaveVariant(ctx, tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if got := f.str(t, `SELECT name || '|' || price FROM item_variants WHERE id = 'var1'`); got != "Large|380" {
		t.Fatalf("a refused patch changed the variant: %s", got)
	}
}

func TestSaveVariant_CapPerItem(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	for i := 0; i < data.MaxItemVariants; i++ {
		f.exec(t, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES (?, 'itm1', ?, ?, 100)`,
			"seed-"+string(rune('a'+i/26))+string(rune('a'+i%26)), "S-"+string(rune('a'+i/26))+string(rune('a'+i%26)), "V")
	}
	_, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var-over", Create: true, Name: strp("One more"), PriceMinor: i64p(1)})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v, want the per-item cap refusal", err)
	}
}

func TestSaveVariant_UntrackedItemGetsNoInventoryRow(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE items SET stock_untracked = 1 WHERE id = 'itm1'`)
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "var1", Create: true, Name: strp("Large"), PriceMinor: i64p(380)}); err != nil {
		t.Fatal(err)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM inventory WHERE variant_id = 'var1'`); n != "0" {
		t.Fatalf("variant of an untracked item got %s inventory rows", n)
	}
}
