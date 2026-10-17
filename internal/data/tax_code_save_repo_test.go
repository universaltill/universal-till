package data

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// SaveTaxCodeTx (ut-docs#4463): the cloud catalogue pull's tax-code write,
// with the tax-code screen's rules.
func TestSaveTaxCodeTx_Rules(t *testing.T) {
	d := openMigratedDB(t, "tax-save.db")
	ctx := context.Background()
	repo := NewCatalogRepo(d.DB)
	inTx := func(fn func(tx *sql.Tx) error) error {
		tx, err := d.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	name, rate, same := "Reduced", 700, 700
	err := inTx(func(tx *sql.Tx) error {
		res, err := repo.SaveTaxCodeTx(ctx, tx, TaxCodeSave{ID: "tc-1", Create: true, Name: &name, RateBP: &rate, TakeawayBP: &same}, nil)
		if err == nil && !res.Created {
			t.Error("not reported created")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.GetTaxCode(ctx, "tc-1")
	if err != nil || v.RateBP != 700 || v.TakeawayRateBP != nil || !v.IsActive {
		t.Fatalf("tax code = %+v %v: an equal takeaway rate must be stored as no override", v, err)
	}
	tooHigh := 10001
	if err := inTx(func(tx *sql.Tx) error {
		_, err := repo.SaveTaxCodeTx(ctx, tx, TaxCodeSave{ID: "tc-1", RateBP: &tooHigh}, nil)
		return err
	}); err == nil {
		t.Fatal("a rate above 100 % was accepted")
	}
	other := 1900
	if err := inTx(func(tx *sql.Tx) error {
		_, err := repo.SaveTaxCodeTx(ctx, tx, TaxCodeSave{ID: "tc-1", RateBP: &other}, func(rateBP int, _ *int) bool { return rateBP == 1900 })
		return err
	}); err == nil {
		t.Fatal("the country rate rule was not applied")
	}
	if err := inTx(func(tx *sql.Tx) error {
		_, err := repo.SaveTaxCodeTx(ctx, tx, TaxCodeSave{ID: "tc-2", Create: true, Name: &name, RateBP: &other}, nil)
		return err
	}); !errors.Is(err, ErrTaxCodeNameExists) {
		t.Fatalf("duplicate name: %v, want ErrTaxCodeNameExists", err)
	}
	if err := inTx(func(tx *sql.Tx) error {
		_, err := repo.SaveTaxCodeTx(ctx, tx, TaxCodeSave{ID: "nope"}, nil)
		return err
	}); !errors.Is(err, ErrTaxCodeNotFound) {
		t.Fatalf("unknown id without create: %v", err)
	}
}

func TestSaveItemTx_RefusesBarcodes(t *testing.T) {
	d := openMigratedDB(t, "item-save-tx.db")
	ctx := context.Background()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	codes := []string{"4006381333931"}
	name, price := "X", int64(1)
	if _, err := NewCatalogRepo(d.DB).SaveItemTx(ctx, tx, ItemPatch{ID: "i-1", Create: true, Name: &name, PriceMinor: &price, Barcodes: &codes}); err == nil {
		t.Fatal("barcodes accepted inside a caller's transaction")
	}
}
