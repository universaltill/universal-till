package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2717: the pilot's report, end to end at the sale-screen level. A
// category shows a library tile the till picked (#2500); a save_category
// directive from my. then sets a different icon through the same
// repository write the cloudsync hook uses. The already-cached sale screen
// must draw the NEW icon on its next request — no restart — which needs
// both the one-picture rule (the tile no longer hides the icon) and the
// tile cache's invalidation (migration 023's categories UPDATE trigger
// bumps sync_admin_version, which every cache entry is keyed on).
func TestSellScreenCache_CategoryIconDirectiveShowsNewIcon(t *testing.T) {
	f := newSellScreenFixture(t)
	const beer, coffee = "/public/assets/category-icons/beer.svg", "/public/assets/category-icons/coffee.svg"
	f.exec(t, `UPDATE categories SET image_path = ? WHERE id = 'cat-drinks'`, beer)
	h := f.handler(t, false, false)
	body, _ := f.list(t, h)
	if !strings.Contains(body, beer) || strings.Contains(body, coffee) {
		t.Fatalf("before: want the beer tile and no coffee icon on the Drinks tab:\n%s", body)
	}
	if _, q := f.list(t, h); q != 1 {
		t.Fatalf("second request ran %d SELECTs — it should be the cached render", q)
	}

	icon := "lucide:coffee"
	if _, err := data.NewCatalogRepo(f.db).SaveCategory(context.Background(), data.CategorySave{ID: "cat-drinks", Icon: &icon}); err != nil {
		t.Fatal(err)
	}
	body, q := f.list(t, h)
	if q == 1 {
		t.Fatal("the icon change was served from the stale cache")
	}
	if !strings.Contains(body, coffee) || strings.Contains(body, beer) {
		t.Fatalf("after the directive: want the coffee icon and no beer tile:\n%s", body)
	}

	// And back the other way: the till's own editor picking a library
	// icon replaces the my. icon on the next request too.
	if err := data.NewCatalogRepo(f.db).SetCategoryPicture(context.Background(), "cat-drinks", "", "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	if body, _ := f.list(t, h); !strings.Contains(body, beer) || strings.Contains(body, coffee) {
		t.Fatal("the till editor's pick did not replace the my. icon on the sale screen")
	}
}
