package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ean13t appends the GS1 mod-10 check digit to a 12-digit body, so tests
// can build structurally valid EAN-13 codes (incl. scale labels) without
// hardcoding magic check digits.
func ean13t(t *testing.T, body string) string {
	t.Helper()
	if len(body) != 12 {
		t.Fatalf("ean13t body must be 12 digits, got %q", body)
	}
	sum := 0
	weight := 3
	for i := len(body) - 1; i >= 0; i-- {
		sum += int(body[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return body + string(byte((10-sum%10)%10)+'0')
}

// TestResolveScanLine_WeightEmbedded (acceptance criterion 1, repo layer):
// with EAN13 and EAN13_WEIGHT_PREFIX2X both enabled, a scale label must
// match the WEIGHT symbology (not plain EAN13 — a valid label is also a
// structurally valid EAN-13, so tier order decides), resolve the item via
// the zeroed LookupKey template, and decode the embedded weight. Two labels
// of the same item with different weights both resolve the same row.
func TestResolveScanLine_WeightEmbedded(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-cheese", SKU: "CHEESE", Name: "Cheese", BasePrice: 599, IsActive: true})
	// The catalog row is the zeroed template (digits 1-7 kept, weight and
	// check digit zeroed) — the convention AddBarcode stores.
	testsupport.SeedBarcode(t, db, "2312345000000", "i-cheese", true)

	label := ean13t(t, "231234501234") // 1.234 kg
	line, dec, ok := repo.ResolveScanLine(ctx, label, []string{"EAN13", "EAN13_WEIGHT_PREFIX2X"})
	if !ok {
		t.Fatal("expected the scale label to resolve via the zeroed template")
	}
	if dec.SymbologyID != "EAN13_WEIGHT_PREFIX2X" {
		t.Fatalf("matched %q, want EAN13_WEIGHT_PREFIX2X (plain EAN13 must not consume the label)", dec.SymbologyID)
	}
	if !dec.HasEmbeddedWeight || dec.EmbeddedWeight != "1.234" {
		t.Fatalf("embedded weight = %q (has=%v), want 1.234", dec.EmbeddedWeight, dec.HasEmbeddedWeight)
	}
	if line.ItemID != "i-cheese" || line.Price != 599 {
		t.Fatalf("resolved %+v, want item i-cheese at 599", line)
	}
	if line.SKU != label {
		t.Fatalf("line.SKU = %q, want the scanned label %q (the code actually scanned)", line.SKU, label)
	}

	// A second, different-weight label of the same item hits the same row.
	label2 := ean13t(t, "231234505678")
	line2, dec2, ok := repo.ResolveScanLine(ctx, label2, []string{"EAN13", "EAN13_WEIGHT_PREFIX2X"})
	if !ok || line2.ItemID != "i-cheese" {
		t.Fatalf("second label did not resolve the same item: ok=%v line=%+v", ok, line2)
	}
	if dec2.EmbeddedWeight != "5.678" {
		t.Fatalf("second label weight = %q, want 5.678", dec2.EmbeddedWeight)
	}
}

// TestResolveScanLine_PriceEmbedded: the price symbology decodes an
// absolute price in minor units and resolves via its own zeroed template.
func TestResolveScanLine_PriceEmbedded(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-ham", SKU: "HAM", Name: "Ham", BasePrice: 100, IsActive: true})
	testsupport.SeedBarcode(t, db, "0254321000000", "i-ham", true)

	enabled := []string{"EAN13", "EAN13_PRICE_PREFIX02"}
	label := ean13t(t, "025432100350") // €3.50
	line, dec, ok := repo.ResolveScanLine(ctx, label, enabled)
	if !ok {
		t.Fatal("expected the price label to resolve via the zeroed template")
	}
	if dec.SymbologyID != "EAN13_PRICE_PREFIX02" || !dec.HasEmbeddedPrice {
		t.Fatalf("matched %+v, want EAN13_PRICE_PREFIX02 with an embedded price", dec)
	}
	if dec.EmbeddedPrice.Minor() != 350 {
		t.Fatalf("embedded price = %d, want 350", dec.EmbeddedPrice.Minor())
	}
	if line.ItemID != "i-ham" {
		t.Fatalf("resolved %+v, want item i-ham", line)
	}
}

// TestResolveScanLine_InternalPLUOnly (acceptance criterion 3): a shop with
// only INTERNAL_PLU enabled accepts a 13-digit code that FAILS the EAN-13
// check digit — today's ut-docs#293 permissiveness, now an explicit choice.
func TestResolveScanLine_InternalPLUOnly(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	const badCheckDigit = "5449000000995" // 13 digits, invalid check digit
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-plu", SKU: "PLU", Name: "PLU Item", BasePrice: 120, IsActive: true})
	testsupport.SeedBarcode(t, db, badCheckDigit, "i-plu", true)

	line, dec, ok := repo.ResolveScanLine(ctx, badCheckDigit, []string{"INTERNAL_PLU"})
	if !ok {
		t.Fatal("INTERNAL_PLU-only shop must accept a 13-digit code with a bad check digit")
	}
	if dec.SymbologyID != "INTERNAL_PLU" {
		t.Fatalf("matched %q, want INTERNAL_PLU", dec.SymbologyID)
	}
	if line.ItemID != "i-plu" {
		t.Fatalf("resolved %+v, want item i-plu", line)
	}
}

// TestResolveScanLine_NoEnabledSymbologyMatch (acceptance criterion 6, scan
// side): once the permissive catch-alls are disabled, a code matching no
// enabled symbology does not resolve — and the full ResolveShortcutLine
// chain also misses (SKU/name fallbacks don't match it either), so the sale
// screen shows its existing not-found toast.
func TestResolveScanLine_NoEnabledSymbologyMatch(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	const badCheckDigit = "5449000000995"
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedBarcode(t, db, badCheckDigit, "i1", true)

	// Only EAN13 enabled: the bad check digit fails it, and no catch-all
	// remains to accept the code.
	if _, _, ok := repo.ResolveScanLine(ctx, badCheckDigit, []string{"EAN13"}); ok {
		t.Fatal("expected no match once CODE128/INTERNAL_PLU are disabled")
	}
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatal(err)
	}
	if line, ok := repo.ResolveShortcutLine(ctx, badCheckDigit); ok {
		t.Fatalf("full chain must also miss (got %+v): the barcode row exists but no enabled symbology admits the scan", line)
	}
}

// TestScanDeleteExists_CollisionResolutionIsDeliberatelyAsymmetric is
// ADR-0059 §6's pinning test (ut-docs#958): when an explicit-type
// escape-hatch row (stored under its raw code) and a different item's
// genuine scale label (stored under the zeroed template that raw code would
// otherwise canonicalise to) collide on the same zeroed key, scan resolves
// the CANONICAL (scale-label) row while DeleteBarcode/BarcodeExists resolve
// the EXACT (escape-hatch) row — deliberately, not by oversight. Each
// ordering protects a different, independently real property:
//   - scan: a genuine embedded-data decode (weight/price, hence money) must
//     never be shadowed by an incidental raw-code match — pinned for the
//     general (non-collision) case by
//     TestResolveScanLine_WeightEmbedded/PriceEmbedded and, end to end
//     through the API, TestScanAPI_WeightEmbeddedLabel in
//     internal/pages/pos_scan_barcode_test.go, which this test must not
//     regress.
//   - delete/exists: acting on the exact code a caller named must never
//     redirect to a DIFFERENT, unrelated item's row — pinned by
//     TestDeleteBarcode_ExactMatchWinsOverCoincidentalCanonicalCollision in
//     catalog_repo_barcode_escape_hatch_test.go, which this test must not
//     regress either.
//
// Unifying the two orderings would necessarily break one of those two
// already-shipped correctness properties (verified directly: reordering
// ResolveScanLine to exact-first passed this package's tests as they stood
// before this test existed, but failed TestScanAPI_WeightEmbeddedLabel — a
// genuine scale item stops resolving whenever an unrelated item's plain
// code coincides with its zeroed template; this test itself now also fails
// under that same mutation, which is the point of adding it). ADR-0059 §6
// resolves ut-docs#958 by keeping both orderings
// exactly as they are and documenting why, rather than picking a winner
// that would trade a rare, not-yet-reachable collision bug (this ADR's own
// Context: needs #935's settings UI, unshipped) for a money or
// data-loss bug in the common case. This test exists so a future change
// can't quietly collapse the asymmetry into one without a test failure
// naming which property broke.
func TestScanDeleteExists_CollisionResolutionIsDeliberatelyAsymmetric(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	catalog := data.NewCatalogRepo(db)
	pos := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Escape-hatch item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Genuine scale item", BasePrice: 200, IsActive: true})
	enabled := []string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx, enabled); err != nil {
		t.Fatal(err)
	}

	// i1: an explicit-type escape-hatch row, stored under its raw code.
	// Its canonical (zeroed) form — weight digits AND check digit zeroed,
	// not recomputed — is the literal string "2412345000000" (13 digits).
	escaped := ean13t(t, "241234500000")
	if err := catalog.AddBarcode(ctx, catalogtypes.BarcodeInput{
		Barcode: escaped, ItemID: "i1", BarcodeType: "EAN13",
	}); err != nil {
		t.Fatalf("i1 escape-hatch add: %v", err)
	}

	// i2: a genuine scale label for a DIFFERENT item, inferred (untyped) to
	// the same zeroed key as escaped's canonical form.
	genuineScaleLabel := ean13t(t, "241234501234")
	if err := catalog.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: genuineScaleLabel, ItemID: "i2"}); err != nil {
		t.Fatalf("i2 genuine scale add: %v", err)
	}

	// Scan resolves the CANONICAL row (i2, the genuine scale label) — money
	// correctness for the embedded decode wins over the coincidental exact
	// match. This is unchanged, existing ResolveScanLine behaviour.
	line, dec, ok := pos.ResolveScanLine(ctx, escaped, enabled)
	if !ok {
		t.Fatal("expected the escape-hatch code to resolve")
	}
	if line.ItemID != "i2" {
		t.Fatalf("scan resolved item %q, want i2 (canonical wins — a genuine embedded decode must not be shadowed)", line.ItemID)
	}
	if !dec.HasEmbeddedWeight {
		t.Fatalf("expected the canonical match to decode an embedded weight, got %+v", dec)
	}

	// BarcodeExists reports true here — it finds i1's EXACT row on the
	// first, exact-first lookup (catalog_repo.go's barcodeExistsExact),
	// never reaching the canonical fallback: with both rows present,
	// existence is true either way, so this call alone doesn't pin the
	// ordering (unlike scan's ItemID or delete's which-row-survives
	// check). DeleteBarcode, unchanged, targets the EXACT row (i1)
	// specifically to avoid ever deleting an unrelated item's data by
	// canonicalisation redirect. This is the documented asymmetry, not a
	// bug: deletion's "never touch the wrong item" safety property
	// outranks matching scan's read-only resolution order.
	if exists, err := catalog.BarcodeExists(ctx, escaped); err != nil || !exists {
		t.Fatalf("BarcodeExists(escaped) = %v/%v, want true/nil", exists, err)
	}
	if err := catalog.DeleteBarcode(ctx, escaped); err != nil {
		t.Fatalf("delete escaped: %v", err)
	}

	// i1's exact escape-hatch row is gone — checked directly against the
	// DB (not BarcodeExists, which would legitimately still report true via
	// i2's surviving canonical row at the same collision).
	var i1RowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = ? AND item_id = 'i1'`, escaped).Scan(&i1RowCount); err != nil {
		t.Fatal(err)
	}
	if i1RowCount != 0 {
		t.Fatal("i1's escape-hatch row should be gone after DeleteBarcode")
	}

	// NOW BarcodeExists(escaped) genuinely exercises the canonical
	// fallback: i1's exact row is gone, so the only way this can still
	// report true is by canonicalising escaped and finding i2's surviving
	// row at that key. This is functional coverage of the fallback branch,
	// not an ordering pin: BarcodeExists's result is a plain OR of "exact
	// row exists" and "canonical row exists", so — unlike Scan's resolved
	// ItemID or Delete's which-row-survives outcome — no assertion on its
	// bool return can ever distinguish exact-first from canonical-first
	// when both rows are present; that asymmetry is provably unobservable
	// through this API, which is exactly why only Scan and Delete are
	// pinned by ItemID/row-identity assertions above.
	if exists, err := catalog.BarcodeExists(ctx, escaped); err != nil || !exists {
		t.Fatalf("BarcodeExists(escaped) after i1's row is gone = %v/%v, want true/nil (must fall through to i2's canonical row)", exists, err)
	}
	// ...i2's unrelated genuine scale-label row was never touched — proof
	// the delete acted on the exact row, not the canonical one scan just
	// resolved to.
	label2, dec2, ok := pos.ResolveScanLine(ctx, genuineScaleLabel, enabled)
	if !ok || label2.ItemID != "i2" {
		t.Fatalf("i2's genuine scale label must still resolve after deleting i1's escape-hatch code: ok=%v line=%+v", ok, label2)
	}
	if !dec2.HasEmbeddedWeight {
		t.Fatalf("i2's genuine scale label should still decode its embedded weight, got %+v", dec2)
	}
}

// TestResolveShortcutLine_NoSettingsRowPreservesLegacyResolution is
// acceptance criterion 4: a fixture shop with NO enabled-symbology setting
// resolves every shape it resolved before this card — valid EAN-13,
// 13-digit code with a bad check digit, and an arbitrary internal code —
// because the seeded default set is a superset of the old behaviour.
func TestResolveShortcutLine_NoSettingsRowPreservesLegacyResolution(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	codes := map[string]string{
		"9780306406157": "i-ean",  // valid EAN-13
		"5449000000995": "i-bad",  // 13 digits, bad check digit (pre-card: CODE128 catch-all)
		"ABC-123":       "i-arb",  // arbitrary internal code
		"5000001":       "i-shrt", // short digit code
	}
	i := 0
	for code, itemID := range codes {
		testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: itemID, SKU: "SKU-" + itemID, Name: "Item " + itemID, BasePrice: int64(100 + i), IsActive: true})
		testsupport.SeedBarcode(t, db, code, itemID, true)
		i++
	}

	for code, itemID := range codes {
		line, ok := repo.ResolveShortcutLine(ctx, code)
		if !ok {
			t.Fatalf("code %q resolved before ADR-0059 wiring and must still resolve with no settings row", code)
		}
		if line.ItemID != itemID {
			t.Fatalf("code %q resolved to %q, want %q", code, line.ItemID, itemID)
		}
	}

	// The first resolution seeded the default set (GetOrCreate).
	if _, found, err := data.NewSettingsRepo(db).Get(ctx, data.BarcodeEnabledSymbologiesKey); err != nil || !found {
		t.Fatalf("expected the default enabled set to be seeded, found=%v err=%v", found, err)
	}
}
