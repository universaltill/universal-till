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

// TestResolveShortcutLine_MaterializedRowFallsBackToLiveItemName
// (ut-docs#2541 review finding 1): ButtonStore.UpdateOrder materializes an
// implicit tile with an intentionally EMPTY label (see
// ShortcutsRepo.MaterializeAndReorder) so a tile shows the item's LIVE
// name/thumbnail rather than freezing them at drag time. The scan/basket
// resolver (this method, via resolveShortcut) must fall back to the item's
// own name for such a row too -- not resolve to a blank name, which would
// print an empty line on the receipt/journal.
func TestResolveShortcutLine_MaterializedRowFallsBackToLiveItemName(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Apple", BasePrice: 200, IsActive: true})
	// A materialized row: empty label, exactly what MaterializeAndReorder
	// inserts.
	if _, err := db.Exec(`INSERT INTO shortcut_buttons(barcode, item_id, label) VALUES('S1','i1','')`); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "S1")
	if !ok {
		t.Fatal("expected a resolved line")
	}
	if line.Name != "Apple" {
		t.Fatalf("expected the item's own (live) name as the fallback for an empty shortcut label, got %q", line.Name)
	}

	// Rename the item -- the resolved line must follow, proving the name
	// isn't frozen anywhere along this path either.
	if _, err := db.Exec(`UPDATE items SET name = 'Granny Smith Apple' WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	line, ok = repo.ResolveShortcutLine(ctx, "S1")
	if !ok {
		t.Fatal("expected a resolved line")
	}
	if line.Name != "Granny Smith Apple" {
		t.Fatalf("expected the RENAMED item name, got %q", line.Name)
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
	// The basket line must carry the code that was actually searched for —
	// resolveVariantSKU has to populate SKU itself (the variant query does
	// not select it), and an empty SKU would reach the basket unnoticed.
	if line.SKU != "S1-L" {
		t.Fatalf("expected the variant's own SKU on the line, got %q", line.SKU)
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
	if line.Name != "Latte - Large" {
		t.Fatalf("expected the composed 'Item - Variant' display name, got %q", line.Name)
	}
}

// TestResolveShortcutLine_ItemSKUStillWinsOverVariantSKU is the regression
// guard for the new variant fallback: it must only fire when no ITEM
// matches by sku. items.sku and item_variants.sku are separately-UNIQUE
// columns (001_init.sql), so one item's SKU legitimately CAN equal another
// item's variant's SKU — that collision is the only shape in which the
// fallback could steal a match from an item, so it is what this test
// creates. (A single item with no competing variant proves nothing here:
// it passes with the fallback removed entirely.)
func TestResolveShortcutLine_ItemSKUStillWinsOverVariantSKU(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "MUG-001", Name: "Mug", BasePrice: 500, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "TEE-001", Name: "Tee", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "i2", SKU: "MUG-001", Name: "Large", Price: 900, IsActive: true})

	line, ok := repo.ResolveShortcutLine(ctx, "MUG-001")
	if !ok {
		t.Fatal("expected a resolved line via exact SKU match")
	}
	if line.ItemID != "i1" || line.VariantID != "" || line.HasVariant {
		t.Fatalf("expected the ITEM match to win over another item's variant with the same SKU, got %+v", line)
	}
	if line.Price != 500 || line.Name != "Mug" {
		t.Fatalf("expected the item's own price/name (500 / %q), got %d / %q", "Mug", line.Price, line.Name)
	}
}

// TestResolveShortcutLine_ItemNameStillWinsOverVariantName is the same
// guard on the name-LIKE path: an item whose NAME matches must keep
// winning over a variant whose name also matches.
func TestResolveShortcutLine_ItemNameStillWinsOverVariantName(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Large Fries", BasePrice: 200, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "i2", SKU: "S2-L", Name: "Large", Price: 350, IsActive: true})

	line, ok := repo.ResolveShortcutLine(ctx, "large")
	if !ok {
		t.Fatal("expected a resolved line")
	}
	if line.ItemID != "i1" || line.VariantID != "" || line.HasVariant {
		t.Fatalf("expected the ITEM name match to win over the variant name match, got %+v", line)
	}
}

// TestResolveShortcutLine_InactiveVariantNotResolvable is the active-row
// guard for the two new variant queries: a discontinued variant — or an
// active variant hanging off a discontinued parent item — must not become
// ringable at checkout via SKU or name search. Without the
// `v.is_active = 1 AND i.is_active = 1` predicates the row still resolves
// AND still prices (ResolveCurrentPrice errors on the inactive variant, so
// resolvePrice silently falls back to the row's own price), which is
// exactly how a withdrawn product would quietly go back on sale.
func TestResolveShortcutLine_InactiveVariantNotResolvable(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	// Inactive variant under an active item.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-XL", Name: "Discontinued Size", Price: 350, IsActive: false})
	// Active variant under an INACTIVE item.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Withdrawn", BasePrice: 400, IsActive: false})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "i2", SKU: "S2-XL", Name: "Withdrawn Size", Price: 450, IsActive: true})

	for _, code := range []string{"S1-XL", "Discontinued Size", "S2-XL", "Withdrawn Size"} {
		if line, ok := repo.ResolveShortcutLine(ctx, code); ok {
			t.Fatalf("%q must not resolve at checkout, got %+v", code, line)
		}
	}
}

// TestResolveShortcutLine_ItemIDCodePrefixResolvesCodelessItem (ut-docs#2294):
// the sell screen's All tab lists EVERY active catalog item, quick button
// or not — including one with neither a barcode nor a SKU, which the
// pre-existing barcode/shortcut/SKU/name tiers have nothing to match it
// on. ButtonStore.LoadAllActive/SearchSellable fall back to the same
// "item:<id>" synthesized-code scheme ButtonStore.Add already uses for a
// codeless quick button (ut-docs#1459); this is what makes that code
// resolvable when NO shortcut_buttons row exists for it at all (unlike
// ut-docs#1459's own case, where a shortcut_buttons row IS the thing that
// makes it resolve).
func TestResolveShortcutLine_ItemIDCodePrefixResolvesCodelessItem(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	// No SKU, no barcode, no shortcut_buttons row — exactly the item the
	// pre-existing tiers can never reach.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "loose-1", SKU: "", Name: "Loose Doughnut", BasePrice: 150, IsActive: true})

	line, ok := repo.ResolveShortcutLine(ctx, "item:loose-1")
	if !ok {
		t.Fatal("expected the item:<id> synthesized code to resolve")
	}
	if line.ItemID != "loose-1" || line.Name != "Loose Doughnut" || line.Price != 150 {
		t.Fatalf("unexpected resolved line: %+v", line)
	}
	// The synthesized code must never leak onto the line as a fake SKU —
	// same treatment ButtonStore.Add's own version of this prefix already
	// gets in internal/ui's PriceResolverAdapter.resolve.
	if line.SKU != "item:loose-1" {
		// ResolveShortcutLine/ResolveShortcutLineDecoded themselves don't
		// blank it (that's the UI-layer adapter's job — see
		// internal/ui/buttons.go's PriceResolverAdapter.resolve); this pins
		// the raw repo-layer contract precisely so a future change to
		// either side is caught wherever it actually breaks.
		t.Fatalf("SKU = %q, want the raw synthesized code (blanking happens one layer up)", line.SKU)
	}
}

// TestResolveShortcutLine_ItemIDCodePrefixLosesToRealShortcutRow (ut-docs#2294
// review, BL-1): ut-docs#1459's codeless quick button writes this exact
// "item:<id>" literal as its OWN shortcut_buttons.barcode — so when a real
// shortcut_buttons row exists for the code, it is not a synthesized fallback
// at all, and must win over the item:<id> tier below it, carrying the
// operator's chosen button label. The first version of this tier checked
// itemIDCodePrefix BEFORE resolveShortcut and returned immediately either
// way, so a shop with a labelled codeless quick button silently lost that
// label off the basket line and receipt the moment ut-docs#2294 shipped —
// caught in review, not by either pre-existing test above, since neither
// seeds a shortcut_buttons row for the id under test.
func TestResolveShortcutLine_ItemIDCodePrefixLosesToRealShortcutRow(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "loose-2", SKU: "", Name: "Catalog Name", BasePrice: 150, IsActive: true})
	if _, err := db.Exec(`INSERT INTO shortcut_buttons(barcode, item_id, label) VALUES('item:loose-2','loose-2','Operator Label')`); err != nil {
		t.Fatal(err)
	}

	line, ok := repo.ResolveShortcutLine(ctx, "item:loose-2")
	if !ok {
		t.Fatal("expected the item:<id> code to resolve")
	}
	if line.Name != "Operator Label" {
		t.Fatalf("expected the real shortcut_buttons row's label to win over the raw catalog name, got %q", line.Name)
	}
	if line.ItemID != "loose-2" || line.Price != 150 {
		t.Fatalf("unexpected resolved line: %+v", line)
	}
}

// TestResolveShortcutLine_ItemIDCodePrefixMisses covers the two ways this
// new tier must fail closed: an id that doesn't exist, and an id that
// exists but is inactive (ut-docs#2281 context — a deactivated item must
// never be addable, same as every other resolution tier already enforces).
func TestResolveShortcutLine_ItemIDCodePrefixMisses(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()

	if _, ok := repo.ResolveShortcutLine(ctx, "item:does-not-exist"); ok {
		t.Fatal("expected no match for an unknown item id")
	}

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "retired-1", SKU: "", Name: "Retired Item", BasePrice: 100, IsActive: false})
	if _, ok := repo.ResolveShortcutLine(ctx, "item:retired-1"); ok {
		t.Fatal("expected an inactive item not to resolve via the item:<id> code")
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
