package data

import (
	"context"
	"testing"
)

// ut-docs#1898 (same mechanism as ut-docs#1610's brands fix, see
// sync_admin_retire_display_test.go): categories gained is_active in
// migration 017, so deleteMissing's FK-blocked retire-in-place now has
// somewhere to flag a pruned-but-still-referenced category. Unlike brands/
// tax_codes/users/stock_locations/registers, categories.name carries no
// UNIQUE constraint, so this table has no `unique` entry in adminTables and
// deleteMissing never runs its name-mangling step for it — this test
// therefore only asserts is_active flips to 0, not any "<name>~<id>" mangle
// (there is none to strip).
func TestAdminApply_CategoryRetiredInPlace(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO categories (id, name) VALUES ('cat-1', 'Drinks')`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// A satellite-local item references the category and is itself FK-pinned
	// by local stock history, so neither it nor the category can be
	// hard-deleted (same fixture shape as TestAdminApply_BrandRetiredInPlace_
	// DisplayNameUnmangled).
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price, category_id) VALUES ('itm-local', 'LOCAL-1', 'Local Item', 100, 'cat-1')`)
	mustExec(t, replica, `INSERT INTO stock_locations (id, name) VALUES ('loc-local', 'Local Store')`)
	mustExec(t, replica, `INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('mv-1', 'itm-local', 'loc-local', 'sale', -1)`)

	mustExec(t, primary, `DELETE FROM categories WHERE id = 'cat-1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	// Twice: the second pass proves the retire is idempotent.
	for i := range 2 {
		if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
			t.Fatalf("apply #%d after primary delete: %v", i+1, err)
		}
	}

	var name string
	var active int
	if err := replica.QueryRow(`SELECT name, is_active FROM categories WHERE id = 'cat-1'`).Scan(&name, &active); err != nil {
		t.Fatalf("query retired category (categories must now carry is_active): %v", err)
	}
	if active != 0 {
		t.Errorf("an FK-blocked category prune must retire in place: is_active=%d, want 0", active)
	}
	// No mangle: categories.name has no UNIQUE constraint, so deleteMissing's
	// name-freeing step never applies here — the name is untouched.
	if name != "Drinks" {
		t.Errorf("categories.name after retire = %q, want unmangled %q (categories has no `unique` entry in adminTables)", name, "Drinks")
	}

	// Resolved display value: the retired-but-referenced category must still
	// surface (unmangled, which it trivially is here) via CatalogRepo's own
	// readers, same "show inactive rows too" contract ListCategories always
	// had.
	repo := NewCatalogRepo(replica.DB)
	all, err := repo.ListCategories(ctx)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	found := false
	for _, c := range all {
		if c.ID == "cat-1" {
			found = true
			if c.Name != "Drinks" {
				t.Errorf("ListCategories name for retired category = %q, want %q", c.Name, "Drinks")
			}
			if c.IsActive {
				t.Errorf("ListCategories IsActive for retired category = true, want false")
			}
		}
	}
	if !found {
		t.Errorf("ListCategories must still include the retired category (it lists active AND inactive); got %+v", all)
	}

	// The actual bug this retire path can trigger (ut-docs#1898 review
	// finding F2): SetCategoryActive's own item-count guard only protects a
	// *manual* deactivation — admin-sync's FK-blocked retire above bypasses
	// it entirely, so 'cat-1' is now is_active=0 while itm-local still
	// actively references it. /catalog's item-edit category <select> is fed
	// by ReadLookup("categories"), not ListCategories — if ReadLookup
	// filtered on is_active here, the retired-but-still-referenced category
	// would vanish from that <select>, and the next save of itm-local (e.g.
	// editing its price) would silently null category_id (a <select> reset
	// to unselected submits nothing). categories is therefore in
	// lookupUnfilteredByActive (see that var's own comment, mirroring the
	// brands carve-out ut-docs#1610 already established) and must keep
	// surfacing it, unfiltered, exactly like TestAdminApply_
	// BrandRetiredInPlace_DisplayNameUnmangled already proves for brands.
	lookups, err := repo.ReadLookup(ctx, "categories")
	if err != nil {
		t.Fatalf("ReadLookup categories: %v", err)
	}
	foundLookup := false
	for _, l := range lookups {
		if l.ID == "cat-1" {
			foundLookup = true
			if l.Name != "Drinks" {
				t.Errorf("ReadLookup(categories) name for retired-but-referenced category = %q, want %q", l.Name, "Drinks")
			}
		}
	}
	if !foundLookup {
		t.Errorf("ReadLookup(categories) dropped the retired-but-referenced category entirely — /catalog's item-edit Category <select> could no longer re-submit itm-local's own category, silently clearing it on next save; got %+v", lookups)
	}
}
