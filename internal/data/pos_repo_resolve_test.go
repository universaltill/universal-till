package data_test

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ResolveShortcutLine is what every barcode scan / manual search hits at
// checkout — it tries, in order, variant barcode -> item barcode -> shortcut
// barcode -> exact SKU -> name-like, and prices the result via
// ResolveCurrentPrice (price_history override, else the item/variant's own
// price). Getting the PRIORITY ORDER and price resolution wrong here would
// silently ring up the wrong price on every sale, so this is one of the
// highest-value places in the whole repo layer to have real tests.

// TestResolveShortcutLine_PriorityOrder proves the actual priority order
// ResolveShortcutLine implements (variant barcode > item barcode > shortcut
// barcode > exact SKU), not just that each path individually works. Real
// data can never have the SAME code legitimately double-assigned across
// types (AddBarcode's ensureBarcodeAvailable blocks it) — this deliberately
// creates that collision by inserting directly, bypassing AddBarcode, so
// each assertion below has a genuine competing match to lose against.
func TestResolveShortcutLine_PriorityOrder(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "item-target", SKU: "SKU-COLLIDE", Name: "Should Lose", BasePrice: 1, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	// "COLLIDE" is simultaneously: a variant barcode (should win), an item
	// barcode, a shortcut barcode, AND the losing item's exact SKU.
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('COLLIDE','v1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('COLLIDE','item-target',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO shortcut_buttons(barcode, item_id, label) VALUES('COLLIDE','item-target','Should Also Lose')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE items SET sku = 'COLLIDE' WHERE id = 'item-target'`); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "COLLIDE")
	if !ok {
		t.Fatal("expected a resolved line")
	}
	if line.ItemID != "i1" || line.VariantID != "v1" {
		t.Fatalf("expected the VARIANT match to win over item barcode, shortcut barcode, and exact SKU, got %+v", line)
	}
	if line.Price != 350 {
		t.Fatalf("expected the variant's own price 350, got %d", line.Price)
	}
	if !line.HasVariant {
		t.Fatal("expected HasVariant=true")
	}
	if line.Name != "Latte - Large" {
		t.Fatalf("expected the composed 'Item - Variant' display name, got %q", line.Name)
	}

	// Remove the variant barcode: item barcode should now win over the
	// shortcut barcode and SKU.
	if _, err := db.Exec(`DELETE FROM variant_barcodes WHERE barcode='COLLIDE'`); err != nil {
		t.Fatal(err)
	}
	line, ok = repo.ResolveShortcutLine(ctx, "COLLIDE")
	if !ok {
		t.Fatal("expected a resolved line")
	}
	if line.ItemID != "item-target" || line.HasVariant {
		t.Fatalf("expected the ITEM barcode match to win over the shortcut barcode and SKU, got %+v", line)
	}
	if line.Name != "Should Lose" {
		t.Fatalf("expected the plain item name (not the shortcut button's label), got %q", line.Name)
	}

	// Remove the item barcode too: shortcut barcode should win over SKU.
	if _, err := db.Exec(`DELETE FROM item_barcodes WHERE barcode='COLLIDE'`); err != nil {
		t.Fatal(err)
	}
	line, ok = repo.ResolveShortcutLine(ctx, "COLLIDE")
	if !ok {
		t.Fatal("expected a resolved line")
	}
	if line.Name != "Should Also Lose" {
		t.Fatalf("expected the shortcut button's label to win now that no barcode matches, got %q", line.Name)
	}
}

func TestResolveShortcutLine_ItemBarcode(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Coffee", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('111','i1',1)`); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "111")
	if !ok {
		t.Fatal("expected a resolved line for the item barcode")
	}
	if line.ItemID != "i1" || line.HasVariant {
		t.Fatalf("expected item i1 with no variant, got %+v", line)
	}
	if line.Price != 250 {
		t.Fatalf("expected item base price 250, got %d", line.Price)
	}
	if line.TaxRateBP != 2000 {
		t.Fatalf("expected tax rate 2000bp from the joined tax_codes row, got %d", line.TaxRateBP)
	}
}

func TestResolveShortcutLine_ShortcutBarcodeUsesButtonLabel(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Generic Coffee", BasePrice: 200, IsActive: true})
	if _, err := db.Exec(`INSERT INTO shortcut_buttons(barcode, item_id, label) VALUES('BTN-1','i1','Flat White')`); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "BTN-1")
	if !ok {
		t.Fatal("expected a resolved line for the shortcut barcode")
	}
	if line.Name != "Flat White" {
		t.Fatalf("expected the shortcut button's own label to override the item name, got %q", line.Name)
	}
}

func TestResolveShortcutLine_ExactSKUFallback(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "MUG-001", Name: "Mug", BasePrice: 500, IsActive: true})

	line, ok := repo.ResolveShortcutLine(ctx, "MUG-001")
	if !ok {
		t.Fatal("expected a resolved line via exact SKU match")
	}
	if line.ItemID != "i1" {
		t.Fatalf("expected item i1, got %+v", line)
	}
}

func TestResolveShortcutLine_NameLikeFallback(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Blueberry Muffin", BasePrice: 275, IsActive: true})

	line, ok := repo.ResolveShortcutLine(ctx, "muffin")
	if !ok {
		t.Fatal("expected a resolved line via name LIKE fallback")
	}
	if line.ItemID != "i1" {
		t.Fatalf("expected item i1, got %+v", line)
	}
}

func TestResolveShortcutLine_NotFound(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	if _, ok := repo.ResolveShortcutLine(ctx, "nothing-matches-this"); ok {
		t.Fatal("expected no match for an unknown code")
	}
	if _, ok := repo.ResolveShortcutLine(ctx, "   "); ok {
		t.Fatal("expected no match for a blank/whitespace code")
	}
}

func TestResolveShortcutLine_InactiveItemNotResolved(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Discontinued", BasePrice: 100, IsActive: false})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('111','i1',1)`); err != nil {
		t.Fatal(err)
	}

	if _, ok := repo.ResolveShortcutLine(ctx, "111"); ok {
		t.Fatal("expected an inactive item's barcode not to resolve at checkout")
	}
}

func TestResolveCurrentPrice_PriceHistoryOverridesBasePrice(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 300, IsActive: true})

	// No price_history row yet: falls back to base_price.
	price, err := repo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil || price != 300 {
		t.Fatalf("expected base price 300, got price=%d err=%v", price, err)
	}

	// An ACTIVE price_history row (started in the past, no end / future end)
	// overrides base_price — a scheduled promo/markdown.
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','i1',250,?)`, past); err != nil {
		t.Fatal(err)
	}
	price, err = repo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil || price != 250 {
		t.Fatalf("expected the active price_history override 250, got price=%d err=%v", price, err)
	}

	// A FUTURE-dated price_history row must NOT apply yet.
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`DELETE FROM price_history`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph2','i1',999,?)`, future); err != nil {
		t.Fatal(err)
	}
	price, err = repo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil || price != 300 {
		t.Fatalf("expected a future price_history row to be ignored (still base price 300), got price=%d err=%v", price, err)
	}

	// An EXPIRED price_history row must NOT apply.
	if _, err := db.Exec(`DELETE FROM price_history`); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	expiredEnd := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES('ph3','i1',111,?,?)`, expired, expiredEnd); err != nil {
		t.Fatal(err)
	}
	price, err = repo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil || price != 300 {
		t.Fatalf("expected an expired price_history row to be ignored (still base price 300), got price=%d err=%v", price, err)
	}

	// Regression: an ends_at that expired a few MINUTES ago, on the SAME
	// calendar day, must also be ignored. lookupPriceHistory used to compare
	// ends_at as a raw string against SQLite's CURRENT_TIMESTAMP instead of
	// wrapping it in datetime() (unlike starts_at, which was always
	// wrapped) — RFC3339's "T...Z" sorts lexically AFTER CURRENT_TIMESTAMP's
	// "HH:MM:SS" on the same day ('T' > ' ' and 'Z' is not numeric), so an
	// already-expired same-day promo kept silently applying at checkout.
	if _, err := db.Exec(`DELETE FROM price_history`); err != nil {
		t.Fatal(err)
	}
	startedEarlier := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	endedMinutesAgo := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES('ph4','i1',77,?,?)`, startedEarlier, endedMinutesAgo); err != nil {
		t.Fatal(err)
	}
	price, err = repo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil || price != 300 {
		t.Fatalf("expected a same-day-expired price_history row to be ignored (still base price 300), got price=%d err=%v", price, err)
	}
}

func TestResolveCurrentPrice_Variant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 350, IsActive: true})

	price, err := repo.ResolveCurrentPrice(ctx, "", "v1")
	if err != nil || price != 350 {
		t.Fatalf("expected the variant's own price 350, got price=%d err=%v", price, err)
	}
}

// TestResolveShortcutLine_VariantSKUExact proves a variant can be found by
// its OWN sku (not the parent item's) and that an active price_history
// override on the variant applies. Before this fix, resolveSKU only ever
// queried items.sku, so a variant's own SKU never matched anything here —
// ResolveShortcutLine returned ok=false, and the variant was unreachable
// by exact-SKU search entirely (found during independent review of
// ut-docs#744's fix, filed as ut-docs#751).
func TestResolveShortcutLine_VariantSKUExact(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('ph1','v1',280,?)`, past); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "S1-L")
	if !ok {
		t.Fatal("expected the variant's own SKU to resolve")
	}
	if line.ItemID != "i1" || line.VariantID != "v1" || !line.HasVariant {
		t.Fatalf("expected item i1 / variant v1, got %+v", line)
	}
	if line.Price != 280 {
		t.Fatalf("expected the variant's active price_history override 280, got %d", line.Price)
	}
	if line.Name != "Latte - Large" {
		t.Fatalf("expected the composed 'Item - Variant' display name, got %q", line.Name)
	}
}

// TestResolveShortcutLine_VariantNameLike is the same gap on the name-LIKE
// fallback: a variant's own name never matched items.name, so it was
// unreachable by name search too.
func TestResolveShortcutLine_VariantNameLike(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('ph1','v1',280,?)`, past); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "large")
	if !ok {
		t.Fatal("expected the variant's own name to resolve via name-LIKE search")
	}
	if line.ItemID != "i1" || line.VariantID != "v1" || !line.HasVariant {
		t.Fatalf("expected item i1 / variant v1, got %+v", line)
	}
	if line.Price != 280 {
		t.Fatalf("expected the variant's active price_history override 280, got %d", line.Price)
	}
}

// TestResolveShortcutLine_ItemSKUStillWinsOverVariantSKU is a regression
// guard: the variant fallback must only fire when no ITEM matches by sku —
// an item's own exact-SKU match must keep winning, unaffected by the new
// fallback.
func TestResolveShortcutLine_ItemSKUStillWinsOverVariantSKU(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "MUG-001", Name: "Mug", BasePrice: 500, IsActive: true})

	line, ok := repo.ResolveShortcutLine(ctx, "MUG-001")
	if !ok {
		t.Fatal("expected a resolved line via exact SKU match")
	}
	if line.ItemID != "i1" || line.HasVariant {
		t.Fatalf("expected the plain item match, got %+v", line)
	}
}

func TestResolveCurrentPrice_ValidationAndNotFound(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	if _, err := repo.ResolveCurrentPrice(ctx, "", ""); err == nil {
		t.Fatal("expected an error when neither itemID nor variantID is given")
	}
	if _, err := repo.ResolveCurrentPrice(ctx, "i1", "v1"); err == nil {
		t.Fatal("expected an error when BOTH itemID and variantID are given")
	}
	if _, err := repo.ResolveCurrentPrice(ctx, "does-not-exist", ""); err == nil {
		t.Fatal("expected an error for an unknown item")
	}

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Retired", BasePrice: 100, IsActive: false})
	if _, err := repo.ResolveCurrentPrice(ctx, "i1", ""); err == nil {
		t.Fatal("expected an error for an inactive item (base_price lookup filters is_active=1)")
	}
}
