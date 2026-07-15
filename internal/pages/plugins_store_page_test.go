package pages

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

func TestStoreCategoriesCountsSortsAndSkipsEmpty(t *testing.T) {
	items := []storeItem{
		{Name: "A", Type: "payment"},
		{Name: "B", Type: "theme"},
		{Name: "C", Type: "payment"},
		{Name: "D", Type: ""}, // untyped listing must not create a category
	}
	cats := storeCategories(items)
	if len(cats) != 2 {
		t.Fatalf("want 2 categories (empty type skipped), got %d: %+v", len(cats), cats)
	}
	// sorted by type: payment before theme
	if cats[0].Type != "payment" || cats[0].Count != 2 {
		t.Fatalf("want payment×2 first, got %+v", cats[0])
	}
	if cats[1].Type != "theme" || cats[1].Count != 1 {
		t.Fatalf("want theme×1 second, got %+v", cats[1])
	}
}

// The store page must render a "browse by category" chip row with the
// human-readable, translated category label (not the raw type slug).
func TestPluginStoreRendersCategoryChips(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)

	items := []storeItem{
		{ListingID: "l1", Name: "Card reader", Version: "1.0", Type: "payment"},
		{ListingID: "l2", Name: "Dark theme", Version: "1.0", Type: "theme"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plugins/store", nil)
	httpx.Render("ui/pages/plugins_store.html", map[string]any{
		"title":      "Plugin Store",
		"menuItems":  nil,
		"Items":      items,
		"Categories": storeCategories(items),
	})(rec, req)

	body := rec.Body.String()
	for _, want := range []string{
		`class="store-cats"`,       // the chip row
		`data-cat="payment"`,       // a category chip
		`data-cat="theme"`,         // a category chip
		`data-type="payment"`,      // card carries its type for filtering
		"Payment",                  // translated label, not "payment" slug alone
		"Theme",                    // translated label
	} {
		if !strings.Contains(body, want) {
			t.Errorf("store page missing %q", want)
		}
	}
}
