package data_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// The cloud pull's one-row barcode write (ut-docs#4521).

func putBarcode(t *testing.T, f saveFixture, p data.BarcodePut, code string) error {
	t.Helper()
	ctx := context.Background()
	res, err := f.catalog.ResolveBarcode(ctx, code)
	if err != nil {
		return err
	}
	p.Barcode = res
	tx, err := f.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := f.catalog.PutBarcodeTx(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

func TestPutBarcodeTx_CapsAnOwnersBarcodesAndRefusesAnotherOwnersCode(t *testing.T) {
	f := newSaveFixture(t)
	for i := 0; i < 20; i++ {
		if err := putBarcode(t, f, data.BarcodePut{ItemID: "itm1"}, fmt.Sprintf("CAP-%02d", i)); err != nil {
			t.Fatalf("barcode %d: %v", i, err)
		}
	}
	// Re-putting a code the item already holds is not a new barcode.
	if err := putBarcode(t, f, data.BarcodePut{ItemID: "itm1", IsPrimary: true}, "CAP-00"); err != nil {
		t.Fatalf("refresh at the cap: %v", err)
	}
	if err := putBarcode(t, f, data.BarcodePut{ItemID: "itm1"}, "CAP-20"); err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Fatalf("21st barcode: err = %v, want the cap", err)
	}
	err := putBarcode(t, f, data.BarcodePut{ItemID: "itm-nocat"}, "CAP-01")
	var conflict *data.BarcodeConflictError
	if !errors.As(err, &conflict) || conflict.TargetID != "itm1" || conflict.Barcode != "CAP-01" {
		t.Fatalf("another item's code: err = %v, want a conflict naming itm1", err)
	}
	if got := f.str(t, `SELECT is_primary FROM item_barcodes WHERE barcode = 'CAP-00'`); got != "1" {
		t.Fatalf("refreshed primary = %s", got)
	}
}

func TestSaveVariantTx_RefusesBarcodes(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	tx, err := f.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = f.catalog.SaveVariantTx(ctx, tx, data.VariantSave{ItemID: "itm1", ID: "v1", Create: true, Name: strp("L"), PriceMinor: i64p(100), Barcodes: idsp("X-1")})
	if err == nil || !strings.Contains(err.Error(), "inside a caller's transaction") {
		t.Fatalf("err = %v, want barcodes refused inside a transaction", err)
	}
}
