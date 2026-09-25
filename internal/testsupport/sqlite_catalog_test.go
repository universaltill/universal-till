package testsupport

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2180: NewCatalogTestDB opened its *sql.DB with no t.Cleanup/Close
// anywhere in the file, so every one of its 26+ call sites that didn't
// remember its own `defer db.Close()` leaked the handle (and its background
// connectionOpener goroutine) forever. This asserts the helper closes its
// own DB once the (sub)test that opened it finishes, regardless of whether
// the caller ever closes it themselves.
func TestNewCatalogTestDB_ClosesOnCleanup(t *testing.T) {
	var db interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		db = NewCatalogTestDB(t)
		if err := db.Ping(); err != nil {
			t.Fatalf("expected a usable db inside the subtest, got: %v", err)
		}
	})
	if err := db.Ping(); err == nil {
		t.Fatal("expected NewCatalogTestDB's db to be closed once its test finished, but Ping still succeeded")
	}
}

// TestNewCatalogTestDB_HasSellScreenHiddenColumn (ut-docs#2541 review): the
// real schema (migration 040) added items.sell_screen_hidden -- this
// fixture had drifted from it, the same fixture-drift class as
// ut-docs#2209/#625 elsewhere in this helper, so any repo call that reads
// or writes the column (CatalogRepo.SellScreenStates/
// SetSellScreenHidden, ButtonStore.LoadAllActive) failed with "no such
// column" against a db built from this helper.
func TestNewCatalogTestDB_HasSellScreenHiddenColumn(t *testing.T) {
	db := NewCatalogTestDB(t)
	SeedItem(t, db, ItemSeed{ID: "i1", SKU: "S1", Name: "Apple", BasePrice: 100, IsActive: true})

	repo := data.NewCatalogRepo(db)
	// ut-docs#2698: SellScreenStates reads sell_screen_removed too.
	if _, _, err := repo.SellScreenStates(context.Background()); err != nil {
		t.Fatalf("SellScreenStates: %v (items table is missing sell_screen_hidden/sell_screen_removed)", err)
	}
	if err := repo.RemoveFromSellScreen(context.Background(), "i1"); err != nil {
		t.Fatalf("RemoveFromSellScreen: %v", err)
	}
	if err := repo.SetSellScreenHidden(context.Background(), "i1", true); err != nil {
		t.Fatalf("SetSellScreenHidden: %v", err)
	}
}
