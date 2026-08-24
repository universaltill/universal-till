package data_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// AddBarcode's untyped-inference path now runs the shared ADR-0059 registry
// matcher against the shop-enabled symbology set (ut-docs#934). These tests
// extend catalog_repo_crud_test.go's AddBarcode coverage — which continues
// to pin the DEFAULT-set behaviour (EAN13-if-valid-else-CODE128, explicit
// EAN13 check-digit rejection: acceptance criteria 4 and 5) — with the
// narrowed-set and embedded-data cases the registry adds.

// TestAddBarcode_EmbeddedSymbologyStoresZeroedLookupKey: for the two
// embedded-data symbologies the row must store the zeroed template
// (LookupKey), NOT the raw label — the raw label's weight/price digits are
// label-specific, and storing them would make every other label of the same
// item unresolvable (ADR-0059 §3).
func TestAddBarcode_EmbeddedSymbologyStoresZeroedLookupKey(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cheese", BasePrice: 599, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X", "EAN13_PRICE_PREFIX02"}); err != nil {
		t.Fatal(err)
	}

	// Weight label 1.234 kg for item code 12345, prefix 23.
	weightLabel := ean13t(t, "231234501234")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: weightLabel, ItemID: "i1"}); err != nil {
		t.Fatalf("weight label rejected: %v", err)
	}
	var storedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, "2312345000000").Scan(&storedType); err != nil {
		t.Fatalf("expected the ZEROED template row 2312345000000, not the raw label: %v", err)
	}
	if storedType != "EAN13_WEIGHT_PREFIX2X" {
		t.Fatalf("barcode_type = %q, want EAN13_WEIGHT_PREFIX2X", storedType)
	}
	if exists, _ := repo.BarcodeExists(ctx, weightLabel); exists {
		t.Fatal("the raw label must not be stored as its own row")
	}

	// Price label €3.50 for item code 54321, prefix 02.
	priceLabel := ean13t(t, "025432100350")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: priceLabel, ItemID: "i1"}); err != nil {
		t.Fatalf("price label rejected: %v", err)
	}
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, "0254321000000").Scan(&storedType); err != nil {
		t.Fatalf("expected the zeroed price template row 0254321000000: %v", err)
	}
	if storedType != "EAN13_PRICE_PREFIX02" {
		t.Fatalf("barcode_type = %q, want EAN13_PRICE_PREFIX02", storedType)
	}
}

// TestAddBarcode_NoEnabledSymbologyMatchIsNamedRejection is acceptance
// criterion 6's write side: with the CODE128/INTERNAL_PLU catch-alls
// disabled, an untyped code matching nothing enabled fails with the named
// sentinel error — naming what was scanned and what is enabled — and NO row
// is written.
func TestAddBarcode_NoEnabledSymbologyMatchIsNamedRejection(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatal(err)
	}

	const badCheckDigit = "5449000000995"
	err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: badCheckDigit, ItemID: "i1"})
	if err == nil {
		t.Fatal("expected a rejection once the catch-alls are disabled")
	}
	if !errors.Is(err, data.ErrBarcodeNoSymbologyMatch) {
		t.Fatalf("error = %v, want ErrBarcodeNoSymbologyMatch", err)
	}
	if !strings.Contains(err.Error(), badCheckDigit) || !strings.Contains(err.Error(), "EAN13") {
		t.Fatalf("the error must name the scanned code and the enabled symbologies, got: %v", err)
	}
	if exists, _ := repo.BarcodeExists(ctx, badCheckDigit); exists {
		t.Fatal("no row may be written on a rejected barcode")
	}
}

// TestAddBarcode_InternalPLUOnlyAcceptsBadCheckDigit is acceptance
// criterion 3's write side: a shop with only INTERNAL_PLU enabled still
// accepts a 13-digit code failing the EAN-13 check digit (the ut-docs#293
// behaviour, now via the shop's own explicit choice) — typed as
// INTERNAL_PLU, since that is the symbology that actually admitted it.
func TestAddBarcode_InternalPLUOnlyAcceptsBadCheckDigit(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx, []string{"INTERNAL_PLU"}); err != nil {
		t.Fatal(err)
	}

	const badCheckDigit = "5449000000995"
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: badCheckDigit, ItemID: "i1"}); err != nil {
		t.Fatalf("INTERNAL_PLU-only shop must accept the code: %v", err)
	}
	var storedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, badCheckDigit).Scan(&storedType); err != nil {
		t.Fatal(err)
	}
	if storedType != "INTERNAL_PLU" {
		t.Fatalf("barcode_type = %q, want INTERNAL_PLU", storedType)
	}
}

// TestAddBarcode_NoSettingsRowSeedsDefaultAndInfersAsBefore is acceptance
// criterion 4's write side: with no settings row at all, untyped inference
// behaves exactly as pre-card (EAN13 when the check digit passes, CODE128
// otherwise) and the first call seeds the default set.
func TestAddBarcode_NoSettingsRowSeedsDefaultAndInfersAsBefore(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})

	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "9780306406157", ItemID: "i1"}); err != nil {
		t.Fatal(err)
	}
	var storedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = '9780306406157'`).Scan(&storedType); err != nil {
		t.Fatal(err)
	}
	if storedType != "EAN13" {
		t.Fatalf("valid EAN-13 typed %q, want EAN13", storedType)
	}

	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "5449000000995", ItemID: "i1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = '5449000000995'`).Scan(&storedType); err != nil {
		t.Fatal(err)
	}
	if storedType != "CODE128" {
		t.Fatalf("bad-check-digit 13-digit code typed %q, want CODE128 (legacy inference preserved)", storedType)
	}

	if _, found, err := data.NewSettingsRepo(db).Get(ctx, data.BarcodeEnabledSymbologiesKey); err != nil || !found {
		t.Fatalf("expected the default enabled set to be seeded on first inference, found=%v err=%v", found, err)
	}

	// ut-docs#934 review finding F4: an 8-digit code used to fall through
	// to the generic CODE128 catch-all (pre-registry inference only ever
	// special-cased 13-digit EAN13). Under the default set it's now typed
	// more specifically as EAN8 — pin this drift explicitly so it reads as
	// intended behaviour, not a regression. barcode_type is write-only
	// (never read to drive scan behaviour), and LookupKey == the typed
	// code for every plain symbology, so resolution is unaffected either
	// way — only the recorded diagnostic type changes.
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "96385074", ItemID: "i1"}); err != nil {
		t.Fatal(err)
	}
	var storedCode string
	if err := db.QueryRow(`SELECT barcode, barcode_type FROM item_barcodes WHERE barcode = '96385074'`).Scan(&storedCode, &storedType); err != nil {
		t.Fatal(err)
	}
	if storedType != "EAN8" {
		t.Fatalf("valid EAN-8 typed %q, want EAN8 (more specific than legacy CODE128 fallback)", storedType)
	}
	if storedCode != "96385074" {
		t.Fatalf("plain symbology must store the code as typed, got %q", storedCode)
	}
}
