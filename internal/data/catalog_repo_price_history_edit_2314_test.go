package data_test

// ut-docs#2314: saving a new price from the catalog item/variant edit form
// (and the cloud's SetItemPrice directive, covered separately in
// catalog_repo_crud_test.go) must end the currently active price_history
// row and start a new one — the till resolves the CHARGED price through
// price_history (ItemCurrentPrices/ResolveCurrentPrice), not raw
// base_price/item_variants.price, so an edit that only touches the latter
// is invisible at checkout whenever an active price_history row exists.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestUpdateItemReturningWasActive_UpdatesPriceHistory is acceptance
// criterion 1: an item with an active open-ended price_history row, edited
// to a new price, must make ItemCurrentPrices/ResolveCurrentPrice see the
// NEW price, and the old row must be closed (ends_at set), not left open
// alongside the new one.
func TestUpdateItemReturningWasActive_UpdatesPriceHistory(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	posRepo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','i1',250,?)`, past); err != nil {
		t.Fatal(err)
	}

	// Precondition: the active promotional row (250) wins over base_price
	// (300) before the edit — same resolution order the sale screen uses.
	if before, err := posRepo.ResolveCurrentPrice(ctx, "i1", ""); err != nil || before != 250 {
		t.Fatalf("precondition: expected resolved price 250 before edit, got %d err=%v", before, err)
	}

	if _, err := repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{
		ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 400, IsActive: true,
	}); err != nil {
		t.Fatalf("update item: %v", err)
	}

	got, err := posRepo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != 400 {
		t.Fatalf("expected the edited price 400 to be what the till charges now, got %d", got)
	}

	currentPrices, err := repo.ItemCurrentPrices(ctx, []string{"i1"})
	if err != nil {
		t.Fatal(err)
	}
	if currentPrices["i1"] != 400 {
		t.Fatalf("expected ItemCurrentPrices to also see 400, got %d", currentPrices["i1"])
	}

	var ends sql.NullString
	if err := db.QueryRow(`SELECT ends_at FROM price_history WHERE id = 'ph1'`).Scan(&ends); err != nil {
		t.Fatal(err)
	}
	if !ends.Valid || ends.String == "" {
		t.Fatalf("expected the old price_history row's ends_at to be set (closed), got %#v", ends)
	}

	var openRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM price_history WHERE item_id = 'i1' AND ends_at IS NULL`).Scan(&openRows); err != nil {
		t.Fatal(err)
	}
	if openRows != 1 {
		t.Fatalf("expected exactly one open price_history row after the edit, got %d", openRows)
	}
}

// TestUpdateVariant_UpdatesPriceHistory is acceptance criterion 2: the same
// behavior for a variant's own price_history stream.
func TestUpdateVariant_UpdatesPriceHistory(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	posRepo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Coffee", BasePrice: 200, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('phv1','v1',300,?)`, past); err != nil {
		t.Fatal(err)
	}

	if before, err := posRepo.ResolveCurrentPrice(ctx, "", "v1"); err != nil || before != 300 {
		t.Fatalf("precondition: expected resolved price 300 before edit, got %d err=%v", before, err)
	}

	if err := repo.UpdateVariant(ctx, catalogtypes.VariantInput{
		ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 450, IsActive: true,
	}); err != nil {
		t.Fatalf("update variant: %v", err)
	}

	got, err := posRepo.ResolveCurrentPrice(ctx, "", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 450 {
		t.Fatalf("expected the edited variant price 450 to be what the till charges now, got %d", got)
	}

	var ends sql.NullString
	if err := db.QueryRow(`SELECT ends_at FROM price_history WHERE id = 'phv1'`).Scan(&ends); err != nil {
		t.Fatal(err)
	}
	if !ends.Valid || ends.String == "" {
		t.Fatalf("expected the old variant price_history row's ends_at to be set (closed), got %#v", ends)
	}
}

// TestUpdateItemReturningWasActive_LeavesFutureScheduledRowUntouched is
// acceptance criterion 3: a future-dated scheduled price_history row must
// be left completely alone by an edit of the CURRENT price — it's a
// deliberate future change, not the current price. This also exercises the
// AppendPriceHistoryItem WHERE-clause fix (ut-docs#2314) at the
// catalog-edit call site, not just AppendPriceHistoryItem in isolation.
func TestUpdateItemReturningWasActive_LeavesFutureScheduledRowUntouched(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	posRepo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Muffin", BasePrice: 300, IsActive: true})
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph-future','i1',999,?)`, future); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{
		ID: "i1", SKU: "S1", Name: "Muffin", BasePrice: 450, IsActive: true,
	}); err != nil {
		t.Fatalf("update item: %v", err)
	}

	got, err := posRepo.ResolveCurrentPrice(ctx, "i1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != 450 {
		t.Fatalf("expected the edited price 450 to be what the till charges now, got %d", got)
	}

	var price int64
	var ends sql.NullString
	if err := db.QueryRow(`SELECT price, ends_at FROM price_history WHERE id = 'ph-future'`).Scan(&price, &ends); err != nil {
		t.Fatal(err)
	}
	if price != 999 {
		t.Fatalf("expected the future-dated row's price to stay 999, got %d", price)
	}
	if ends.Valid {
		t.Fatalf("expected the future-dated scheduled row to remain untouched (ends_at still NULL), got %q", ends.String)
	}
}

// TestUpdateItemReturningWasActive_NoSpuriousPriceHistoryOnUnrelatedEdit is
// acceptance criterion 5: only touch price_history when the price actually
// CHANGED — an edit that resubmits the currently-resolved price (e.g. the
// operator only renamed the item; the edit form now pre-fills the Price
// field with the resolved price, so an unrelated edit round-trips it
// unchanged) must not insert a new price_history row or touch the existing
// open one's ends_at.
func TestUpdateItemReturningWasActive_NoSpuriousPriceHistoryOnUnrelatedEdit(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','i1',250,?)`, past); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Rename only — BasePrice resubmitted as 250, the item's currently
	// resolved (price_history-backed) price, exactly what a form
	// pre-filled with the resolved price would round-trip unchanged.
	if _, err := repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{
		ID: "i1", SKU: "S1", Name: "Latte Renamed", BasePrice: 250, IsActive: true,
	}); err != nil {
		t.Fatalf("update item: %v", err)
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("expected no new price_history row on an unrelated (price-unchanged) edit, before=%d after=%d", before, after)
	}

	var ends sql.NullString
	if err := db.QueryRow(`SELECT ends_at FROM price_history WHERE id = 'ph1'`).Scan(&ends); err != nil {
		t.Fatal(err)
	}
	if ends.Valid {
		t.Fatalf("expected the existing open price_history row to stay open (untouched), got ends_at=%q", ends.String)
	}

	// The rename itself must still have gone through.
	itm, ok, err := repo.GetItem(ctx, "i1")
	if err != nil || !ok {
		t.Fatalf("expected item to exist, ok=%v err=%v", ok, err)
	}
	if itm.Name != "Latte Renamed" {
		t.Fatalf("expected the unrelated field edit to still apply, got name=%q", itm.Name)
	}
}

// TestUpdateVariant_NoSpuriousPriceHistoryOnUnrelatedEdit is the variant
// counterpart of the above.
func TestUpdateVariant_NoSpuriousPriceHistoryOnUnrelatedEdit(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Coffee", BasePrice: 200, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('phv1','v1',300,?)`, past); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if err := repo.UpdateVariant(ctx, catalogtypes.VariantInput{
		ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large (Renamed)", Price: 300, IsActive: true,
	}); err != nil {
		t.Fatalf("update variant: %v", err)
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("expected no new price_history row on an unrelated (price-unchanged) variant edit, before=%d after=%d", before, after)
	}
}
