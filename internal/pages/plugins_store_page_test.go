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
		`class="store-cats"`,  // the chip row
		`data-cat="payment"`,  // a category chip
		`data-cat="theme"`,    // a category chip
		`data-type="payment"`, // card carries its type for filtering
		"Payment",             // translated label, not "payment" slug alone
		"Theme",               // translated label
	} {
		if !strings.Contains(body, want) {
			t.Errorf("store page missing %q", want)
		}
	}
}

// FR-006: a card with manifest-declared permissions must show them as
// badges; a card with none must not render an empty badge row. Every
// not-yet-installed card (installed or not) shows the manager-approval
// notice, since PR #46 blanket-gates every install/uninstall mutation.
func TestPluginStoreRendersPermissionAndManagerApprovalBadges(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)

	items := []storeItem{
		{ListingID: "l1", Name: "Stripe Terminal", Version: "1.0", Type: "payment", Permissions: []string{"net:api.stripe.com", "pos.tender"}},
		{ListingID: "l2", Name: "Dark theme", Version: "1.0", Type: "theme"},
		{ListingID: "l3", Name: "Already installed", Version: "1.0", Type: "theme", Installed: true},
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
		"net:api.stripe.com", // permission badge text
		"pos.tender",         // permission badge text
	} {
		if !strings.Contains(body, want) {
			t.Errorf("store page missing permission badge %q", want)
		}
	}
	if strings.Count(body, "perm-badge") != 2 {
		t.Errorf("want exactly 2 permission badges (only the plugin declaring permissions), got %d", strings.Count(body, "perm-badge"))
	}
	// Not-yet-installed cards (2 of the 3) get the manager-approval notice;
	// the already-installed card does not (nothing left to approve).
	if got := strings.Count(body, "Installing requires manager approval"); got != 2 {
		t.Errorf("want manager-approval notice on the 2 not-yet-installed cards, got %d occurrences", got)
	}
}
