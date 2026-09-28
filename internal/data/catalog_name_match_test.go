package data

import (
	"context"
	"sort"
	"testing"
)

// ut-docs#2703 (reopened): a legacy pay-at-counter order stored only line
// NAMES; opening it on the till matches each name to an active catalog item
// by exact, case-insensitive, trimmed name -- never a partial match (the
// sale screen's name-LIKE search tier would turn "Tea" into "Iced Tea").
func TestCatalogRepo_ActiveItemIDsByName(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "catalog_name_match.db")
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-bagel','B1','Avocado Lachs Bagel',850,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-tea','T1','Tea',200,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-iced','T2','Iced Tea',300,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-old','O1','Black Shadow',400,0)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-cay1','C1','Çay',100,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-cay2','C2','çay ',100,1)`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	got, err := NewCatalogRepo(d.DB).ActiveItemIDsByName(ctx, []string{"  avocado lachs BAGEL ", "Tea", "Black Shadow", "ÇAY", "Nothing"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, want ...string) {
		t.Helper()
		ids := append([]string(nil), got[FoldItemName(name)]...)
		sort.Strings(ids)
		sort.Strings(want)
		if len(ids) != len(want) {
			t.Fatalf("%q -> %v, want %v", name, ids, want)
		}
		for i := range ids {
			if ids[i] != want[i] {
				t.Fatalf("%q -> %v, want %v", name, ids, want)
			}
		}
	}
	check("Avocado Lachs Bagel", "i-bagel")
	check("tea", "i-tea")            // exact: never Iced Tea
	check("Black Shadow")            // inactive item: no match
	check("çay", "i-cay1", "i-cay2") // Unicode case fold; ambiguous is reported as such
	check("Nothing")
}
