package data

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3098: the import preview shows the SKU each blank-SKU row will
// get before anything is written. Nothing is inserted between two
// predictions in one preview, so the generator takes a per-request
// reserved set that it treats as taken — and must behave exactly as before
// when that set is nil (every real insert passes nil).

func previewSKUTestDB(t *testing.T) (*sql.DB, *CatalogRepo) {
	t.Helper()
	d := testsupport.NewCatalogTestDB(t)
	return d, NewCatalogRepo(d)
}

func seedCat(t *testing.T, d *sql.DB, id, name, parentID string) {
	t.Helper()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	if _, err := d.Exec(`INSERT INTO categories (id, name, parent_id) VALUES (?, ?, ?)`, id, name, parent); err != nil {
		t.Fatalf("seed category %s: %v", name, err)
	}
}

func seedItemSKU(t *testing.T, d *sql.DB, id, sku, categoryID string) {
	t.Helper()
	var cat any
	if categoryID != "" {
		cat = categoryID
	}
	if _, err := d.Exec(`INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES (?, ?, ?, 100, ?, 1)`,
		id, sku, "item "+id, cat); err != nil {
		t.Fatalf("seed item %s: %v", id, err)
	}
}

func TestSKUGenerator_ReservedTreatedAsTaken(t *testing.T) {
	d, _ := previewSKUTestDB(t)
	ctx := context.Background()
	seedCat(t, d, "c-kuchen", "Kuchen", "")
	seedItemSKU(t, d, "i1", "30096", "c-kuchen")
	seedCat(t, d, "c-brot", "Brot", "")

	cases := []struct {
		name     string
		reserved map[string]bool
		run      func(map[string]bool) (string, error)
		want     string
	}{
		{"numeric nil", nil, func(r map[string]bool) (string, error) {
			s, _, err := nextNumericCategorySKU(ctx, d, "c-kuchen", r)
			return s, err
		}, "30097"},
		{"numeric reserved", map[string]bool{"30097": true}, func(r map[string]bool) (string, error) {
			s, _, err := nextNumericCategorySKU(ctx, d, "c-kuchen", r)
			return s, err
		}, "30098"},
		{"prefix nil", nil, func(r map[string]bool) (string, error) {
			return nextPrefixedSKU(ctx, d, "BRO", r)
		}, "BRO-0001"},
		{"prefix reserved", map[string]bool{"BRO-0001": true, "BRO-0002": true}, func(r map[string]bool) (string, error) {
			return nextPrefixedSKU(ctx, d, "BRO", r)
		}, "BRO-0003"},
		{"item numeric reserved", map[string]bool{"30097": true}, func(r map[string]bool) (string, error) {
			return nextItemSKU(ctx, d, strPtrInternal("c-kuchen"), r)
		}, "30098"},
		{"item prefix reserved", map[string]bool{"BRO-0001": true}, func(r map[string]bool) (string, error) {
			return nextItemSKU(ctx, d, strPtrInternal("c-brot"), r)
		}, "BRO-0002"},
		{"item no category nil", nil, func(r map[string]bool) (string, error) {
			return nextItemSKU(ctx, d, nil, r)
		}, "ITEM-0001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.run(tc.reserved)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	// skuTaken itself: a reserved code is taken, an unreserved free one is
	// not, and a nil set changes nothing.
	for _, tc := range []struct {
		code     string
		reserved map[string]bool
		want     bool
	}{
		{"FREE-1", nil, false},
		{"FREE-1", map[string]bool{"FREE-1": true}, true},
		{"FREE-1", map[string]bool{"OTHER": true}, false},
		{"30096", nil, true},
	} {
		got, err := skuTaken(ctx, d, tc.code, tc.reserved)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("skuTaken(%q, %v) = %v, want %v", tc.code, tc.reserved, got, tc.want)
		}
	}
}

func strPtrInternal(s string) *string { return &s }

func TestPreviewItemSKU(t *testing.T) {
	ctx := context.Background()

	t.Run("existing category continues its numeric sequence", func(t *testing.T) {
		d, repo := previewSKUTestDB(t)
		seedCat(t, d, "c-kuchen", "Kuchen", "")
		seedItemSKU(t, d, "i1", "30095", "c-kuchen")
		seedItemSKU(t, d, "i2", "30096", "c-kuchen")
		got, err := repo.PreviewItemSKU(ctx, "", "kuchen", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "30097" {
			t.Fatalf("got %q, want 30097", got)
		}
	})

	t.Run("new category falls back to prefix from its name", func(t *testing.T) {
		d, repo := previewSKUTestDB(t)
		// Another category already used KUC-0003 (prefix sequence is
		// shop-wide, as at commit).
		seedCat(t, d, "c-kuche", "Küche", "")
		seedItemSKU(t, d, "i1", "KUC-0003", "c-kuche")
		got, err := repo.PreviewItemSKU(ctx, "", "Kuchen", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "KUC-0004" {
			t.Fatalf("got %q, want KUC-0004", got)
		}
	})

	t.Run("reserved threaded through two calls gives distinct SKUs", func(t *testing.T) {
		d, repo := previewSKUTestDB(t)
		seedCat(t, d, "c-kuchen", "Kuchen", "")
		seedItemSKU(t, d, "i1", "30096", "c-kuchen")
		reserved := map[string]bool{}
		for _, cat := range []string{"Kuchen", "Brot"} {
			var got []string
			for n := 0; n < 2; n++ {
				s, err := repo.PreviewItemSKU(ctx, "", cat, reserved)
				if err != nil {
					t.Fatal(err)
				}
				reserved[s] = true
				got = append(got, s)
			}
			if got[0] == got[1] {
				t.Fatalf("%s: two predictions both %q", cat, got[0])
			}
		}
		for _, want := range []string{"30097", "30098", "BRO-0001", "BRO-0002"} {
			if !reserved[want] {
				t.Errorf("missing prediction %s in %v", want, reserved)
			}
		}
	})

	t.Run("category is scoped under its department", func(t *testing.T) {
		d, repo := previewSKUTestDB(t)
		seedCat(t, d, "d-food", "Food", "")
		seedCat(t, d, "d-drink", "Drinks", "")
		seedCat(t, d, "c-food-misc", "Misc", "d-food")
		seedCat(t, d, "c-drink-misc", "Misc", "d-drink")
		seedItemSKU(t, d, "i1", "500", "c-food-misc")
		seedItemSKU(t, d, "i2", "900", "c-drink-misc")
		got, err := repo.PreviewItemSKU(ctx, "Drinks", "Misc", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "901" {
			t.Fatalf("got %q, want 901 (Drinks/Misc, not Food/Misc)", got)
		}
		// Department only → the item sits in the department itself.
		seedItemSKU(t, d, "i3", "7000", "d-food")
		got, err = repo.PreviewItemSKU(ctx, "food", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "7001" {
			t.Fatalf("department-only: got %q, want 7001", got)
		}
		// A category that exists top-level but not under this new
		// department is a brand-new category at commit → prefix scheme.
		got, err = repo.PreviewItemSKU(ctx, "Bakery", "Misc", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "MIS-0001" {
			t.Fatalf("new department: got %q, want MIS-0001", got)
		}
	})

	t.Run("no department or category gives ITEM prefix", func(t *testing.T) {
		_, repo := previewSKUTestDB(t)
		got, err := repo.PreviewItemSKU(ctx, "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "ITEM-0001" {
			t.Fatalf("got %q, want ITEM-0001", got)
		}
	})

	t.Run("prediction for a new category matches the real insert", func(t *testing.T) {
		d, repo := previewSKUTestDB(t)
		seedCat(t, d, "c-x", "Übergrößen alt", "")
		seedItemSKU(t, d, "i1", "UBE-0007", "c-x")
		predicted, err := repo.PreviewItemSKU(ctx, "", "Übergrößen", nil)
		if err != nil {
			t.Fatal(err)
		}
		catID, err := repo.EnsureCategoryUnder(ctx, "Übergrößen", "")
		if err != nil {
			t.Fatal(err)
		}
		id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Jacket", BasePrice: 100, Unit: "each", IsActive: true, CategoryID: &catID})
		if err != nil {
			t.Fatal(err)
		}
		var actual string
		if err := d.QueryRow(`SELECT sku FROM items WHERE id = ?`, id).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if predicted != actual {
			t.Fatalf("predicted %q, insert generated %q", predicted, actual)
		}
	})
}
