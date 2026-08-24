package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestAddBarcode_ExplicitEAN13EscapesEmbeddedInference is ut-docs#948's F1
// acceptance criterion: an operator can add a plain-interpretation EAN-13
// barcode in the 20-29/02 prefix range even when the corresponding embedded
// symbology is enabled for the shop, by passing an explicit BarcodeType
// (ADR-0059 §3's existing "explicit type does not run inference" carve-out).
func TestAddBarcode_ExplicitEAN13EscapesEmbeddedInference(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Plain Item", BasePrice: 599, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatal(err)
	}

	// A genuine plain retail EAN-13 that happens to start with 23 (inside
	// the weight-embedded prefix range 20-29) and has a valid check digit —
	// exactly the F1 scenario. Untyped, this would infer as embedded-data.
	plainCode := ean13t(t, "231234500000")

	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{
		Barcode: plainCode, ItemID: "i1", BarcodeType: "EAN13",
	}); err != nil {
		t.Fatalf("explicit-type add rejected: %v", err)
	}

	var storedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, plainCode).Scan(&storedType); err != nil {
		t.Fatalf("expected the RAW code stored as its own row (not zeroed): %v", err)
	}
	if storedType != "EAN13" {
		t.Fatalf("barcode_type = %q, want EAN13 (explicit type bypasses inference)", storedType)
	}
	if exists, err := repo.BarcodeExists(ctx, plainCode); err != nil || !exists {
		t.Fatalf("expected the raw code to be found by BarcodeExists, got exists=%v err=%v", exists, err)
	}
}

// TestBarcodeExistsAndDelete_AgreeWithAddBarcode is ut-docs#948's F6
// acceptance criterion: BarcodeExists/DeleteBarcode pre-checks must agree
// with what AddBarcode actually stored, across three cases — the easy
// plain-code case, the genuinely-inferred embedded-data case, and the F1×F6
// interaction case where an explicit-type escape-hatch row would otherwise
// have been misdirected by blind canonicalisation.
func TestBarcodeExistsAndDelete_AgreeWithAddBarcode(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatal(err)
	}

	// (a) Ordinary plain code under the default-ish enabled set (canonical
	// == raw for every plain symbology).
	plain := ean13t(t, "978030640615")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: plain, ItemID: "i1"}); err != nil {
		t.Fatalf("plain add failed: %v", err)
	}
	if exists, err := repo.BarcodeExists(ctx, plain); err != nil || !exists {
		t.Fatalf("(a) plain: exists=%v err=%v, want true/nil", exists, err)
	}
	if err := repo.DeleteBarcode(ctx, plain); err != nil {
		t.Fatalf("(a) plain delete: %v", err)
	}
	if exists, _ := repo.BarcodeExists(ctx, plain); exists {
		t.Fatal("(a) plain: expected gone after delete")
	}

	// (b) Genuinely-inferred embedded-data code: AddBarcode stores the
	// zeroed LookupKey, not the raw scanned label. A pre-check against the
	// RAW label (as a scan-path/import caller would supply) must still find
	// it via the canonical-key fallback.
	weightLabel := ean13t(t, "251234501234")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: weightLabel, ItemID: "i1"}); err != nil {
		t.Fatalf("(b) embedded add failed: %v", err)
	}
	if exists, err := repo.BarcodeExists(ctx, weightLabel); err != nil || !exists {
		t.Fatalf("(b) embedded: BarcodeExists(rawLabel) = %v/%v, want true/nil (must agree with AddBarcode's stored zeroed key)", exists, err)
	}
	if err := repo.DeleteBarcode(ctx, weightLabel); err != nil {
		t.Fatalf("(b) embedded delete: %v", err)
	}
	if exists, _ := repo.BarcodeExists(ctx, weightLabel); exists {
		t.Fatal("(b) embedded: expected gone after delete via raw label")
	}

	// (c) The F1×F6 interaction: an explicit-type escape-hatch row stored
	// under its RAW code, even though that code would otherwise infer to
	// the embedded symbology under blind canonicalisation. A pre-check must
	// find the row as actually stored (raw), not misdirect to the zeroed
	// key nothing was ever written under.
	escaped := ean13t(t, "241234500000")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{
		Barcode: escaped, ItemID: "i1", BarcodeType: "EAN13",
	}); err != nil {
		t.Fatalf("(c) escape-hatch add failed: %v", err)
	}
	if exists, err := repo.BarcodeExists(ctx, escaped); err != nil || !exists {
		t.Fatalf("(c) escape-hatch: BarcodeExists(rawCode) = %v/%v, want true/nil (must not be misdirected to the zeroed key)", exists, err)
	}
	if err := repo.DeleteBarcode(ctx, escaped); err != nil {
		t.Fatalf("(c) escape-hatch delete: %v", err)
	}
	if exists, _ := repo.BarcodeExists(ctx, escaped); exists {
		t.Fatal("(c) escape-hatch: expected gone after delete")
	}
	// The zeroed key must never have been touched by any of this — nothing
	// was ever stored under it.
	var zeroedExists bool
	if exists, err := repo.BarcodeExists(ctx, "2412345000000"); err != nil {
		t.Fatal(err)
	} else {
		zeroedExists = exists
	}
	if zeroedExists {
		t.Fatal("(c) escape-hatch: the zeroed key must never have a row")
	}
}

// TestDeleteBarcode_ExactMatchWinsOverCoincidentalCanonicalCollision is the
// genuinely correctness-sensitive case for ut-docs#948 F6's exact-first
// ordering (as opposed to BarcodeExists, where either ordering happens to
// give the same true/false answer): if a DIFFERENT item's real
// embedded-data row happens to sit at the same zeroed key an explicit-type
// escape-hatch code would canonicalise to, deleting by the escape-hatch
// code must remove ONLY that exact row — never the coincidentally-matching
// but unrelated row under the canonical key.
func TestDeleteBarcode_ExactMatchWinsOverCoincidentalCanonicalCollision(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Escape-hatch item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Genuine scale item", BasePrice: 200, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatal(err)
	}

	// Item i1: an explicit-type escape-hatch row. Its raw code would
	// canonicalise (if blindly inferred) to zeroed key "2412345000000".
	escaped := ean13t(t, "241234500000")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{
		Barcode: escaped, ItemID: "i1", BarcodeType: "EAN13",
	}); err != nil {
		t.Fatalf("i1 escape-hatch add: %v", err)
	}

	// Item i2: a genuine scale label for a DIFFERENT item that happens to
	// share the same prefix+item-code (24-12345), so its inferred zeroed
	// key collides exactly with the canonical form of i1's escaped code.
	genuineScaleLabel := ean13t(t, "241234501234")
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: genuineScaleLabel, ItemID: "i2"}); err != nil {
		t.Fatalf("i2 genuine scale add: %v", err)
	}
	var i2Type string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = '2412345000000' AND item_id = 'i2'`).Scan(&i2Type); err != nil {
		t.Fatalf("expected i2's genuine row at the zeroed key: %v", err)
	}

	// Delete i1's escape-hatch barcode by its raw/typed code — must remove
	// ONLY that row, leaving i2's unrelated genuine scale-label row intact.
	if err := repo.DeleteBarcode(ctx, escaped); err != nil {
		t.Fatalf("delete escaped: %v", err)
	}
	// Checked directly against the DB, not via BarcodeExists: BarcodeExists
	// is deliberately code-existence, not row-identity, so once i2's row is
	// (correctly) left in place at the colliding canonical key,
	// BarcodeExists(escaped) legitimately still reports true via the
	// canonical fallback finding i2's row — that's not evidence i1's row
	// survived.
	var i1RowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = ? AND item_id = 'i1'`, escaped).Scan(&i1RowCount); err != nil {
		t.Fatal(err)
	}
	if i1RowCount != 0 {
		t.Fatal("i1's escape-hatch row should be gone")
	}
	var survivingType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = '2412345000000' AND item_id = 'i2'`).Scan(&survivingType); err != nil {
		t.Fatalf("i2's UNRELATED genuine scale-label row must survive deleting i1's escape-hatch code, but it's gone: %v", err)
	}
	if survivingType != "EAN13_WEIGHT_PREFIX2X" {
		t.Fatalf("i2's row type changed unexpectedly: %q", survivingType)
	}
}
