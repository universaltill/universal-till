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
		`data-type="payment"`, // tile carries its type for filtering
		"Payment",             // translated label, not "payment" slug alone
		"Theme",               // translated label
	} {
		if !strings.Contains(body, want) {
			t.Errorf("store page missing %q", want)
		}
	}
}

// TestPluginStoreJSErrorLookupCoversServerErrorKeys guards ut-docs#703: the
// store page's own inline script maps a server-returned i18n message KEY to
// translated text via a page-local `T` lookup object (CLAUDE.md's mandated
// pattern for inline <script> blocks — guard-i18n.sh can't see this gap,
// since the key IS routed through `{{ T "..." }}` server-side; the miss is
// the client-side lookup table not listing every key the handler can
// actually send). A key missing from T falls through to `T[j.error] ||
// j.error`, which shows the shop owner the raw locale-key string instead
// of a translated message — exactly what happened here for
// plugins.install.error.not_found before this fix. Every
// "plugins.install.error.*" code registerPluginStoreAPI's download/install
// handlers can emit (see plugins_store_page.go's `respond` calls) must
// have a matching entry in the rendered page's T object.
func TestPluginStoreJSErrorLookupCoversServerErrorKeys(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)

	// ut-docs#4180: the actions (and so the lookup) live on a listing's page.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plugins/store/l1", nil)
	httpx.Render("ui/pages/plugins_store_detail.html", map[string]any{
		"title":     "Plugin Store",
		"menuItems": nil,
		"Item":      storeItemForTest(storeItem{ListingID: "l1", Name: "Card reader", Version: "1.0", Type: "payment"}),
	})(rec, req)

	body := rec.Body.String()
	for _, key := range []string{
		"plugins.install.error.replica_use_primary",
		"plugins.install.error.not_entitled",
		"plugins.install.error.not_found",
		"plugins.install.error.registered_required",
		"plugins.install.error.pair_required",
		"plugins.install.error.pair_again",
		"plugins.install.error.till_removed",
	} {
		if !strings.Contains(body, "'"+key+"':") {
			t.Errorf("store page's inline-script T lookup is missing %q — a shop owner would see the raw key instead of a translated message", key)
		}
	}
}

// renderStoreDetail renders a listing's own page (ut-docs#4180) for item.
func renderStoreDetail(t *testing.T, item storeItem, lang string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plugins/store/"+item.ListingID+"?lang="+lang, nil)
	httpx.Render("ui/pages/plugins_store_detail.html", map[string]any{
		"title": item.Name, "menuItems": nil, "Item": storeItemForTest(item),
	})(rec, req)
	return rec.Body.String()
}

// FR-006: a listing with manifest-declared permissions shows them as badges
// on its page; one with none renders no empty badge row. A not-yet-installed
// listing shows the manager-approval notice, since PR #46 blanket-gates every
// install/uninstall mutation; an installed one doesn't (nothing to approve).
func TestPluginStoreRendersPermissionAndManagerApprovalBadges(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)

	body := renderStoreDetail(t, storeItem{ListingID: "l1", Name: "Stripe Terminal", Version: "1.0", Type: "payment", Permissions: []string{"net:api.stripe.com", "pos.tender"}}, "en")
	for _, want := range []string{
		"net:api.stripe.com", // permission badge text
		"pos.tender",         // permission badge text
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing page missing permission badge %q", want)
		}
	}
	if strings.Count(body, "perm-badge") != 2 {
		t.Errorf("want exactly 2 permission badges, got %d", strings.Count(body, "perm-badge"))
	}
	if got := strings.Count(body, "Installing requires manager approval"); got != 1 {
		t.Errorf("want the manager-approval notice once, got %d", got)
	}

	body = renderStoreDetail(t, storeItem{ListingID: "l2", Name: "Dark theme", Version: "1.0", Type: "theme"}, "en")
	if strings.Contains(body, "perm-badge") || strings.Contains(body, httpx.T("en", "plugins.store.permissions_label")) {
		t.Error("a listing with no permissions must not render a permission row")
	}
	body = renderStoreDetail(t, storeItem{ListingID: "l3", Name: "Already installed", Version: "1.0", Type: "theme", Installed: true}, "en")
	if strings.Contains(body, "Installing requires manager approval") {
		t.Error("an installed listing has nothing left to approve")
	}
	if strings.Contains(body, `data-store-action="`) || !strings.Contains(body, `href="/plugins"`) {
		t.Error("an installed listing offers only Manage, no store actions")
	}
}

// ut-docs#2899: a setting-bound permission is shown in words — "connects
// only to the address saved in: <keys>" — not as the raw manifest string.
func TestPluginStoreRendersSettingBoundPermissionInWords(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)
	body := renderStoreDetail(t, storeItem{ListingID: "l1", Name: "Fiscal device", Version: "1.0", Type: "payment", Permissions: []string{"tcp:@setting:okc.host:okc.port", "storage"}}, "en")
	label := httpx.T("en", "plugins.permissions.setting_bound")
	if label == "plugins.permissions.setting_bound" || !strings.Contains(body, label) {
		t.Fatalf("listing page missing the setting-bound label %q", label)
	}
	if !strings.Contains(body, "okc.host, okc.port") {
		t.Errorf("listing page must name the settings the grant follows")
	}
	if strings.Count(body, "perm-badge") != 2 {
		t.Errorf("want 2 permission badges, got %d", strings.Count(body, "perm-badge"))
	}
}

// ut-docs#4431: the import page links "Get the importer for your system" to
// /plugins/store?type=import — the store opens on that category when the
// catalog has it, and on All otherwise (never on an empty grid).
func TestStoreActiveCategoryPreselectsKnownTypeOnly(t *testing.T) {
	cats := []storeCategory{{Type: "import", Count: 2}, {Type: "payment", Count: 1}}
	if got := storeActiveCategory("import", cats); got != "import" {
		t.Fatalf("storeActiveCategory(import) = %q", got)
	}
	if got := storeActiveCategory("theme", cats); got != "" {
		t.Fatalf("a type with no listings preselected %q, want All", got)
	}
	chdirRoot(t)
	initPagesI18n(t)
	items := []storeItem{{ListingID: "l1", Name: "Importer", Version: "1.0", Type: "import"}, {ListingID: "l2", Name: "Card reader", Version: "1.0", Type: "payment"}}
	rec := httptest.NewRecorder()
	httpx.Render("ui/pages/plugins_store.html", map[string]any{
		"title": "Plugin Store", "Items": items, "Categories": storeCategories(items), "ActiveCat": "import",
	})(rec, httptest.NewRequest("GET", "/plugins/store?type=import", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `class="cat-chip active" data-cat="import"`) {
		t.Errorf("import chip not preselected")
	}
	if strings.Contains(body, `class="cat-chip active" data-cat=""`) {
		t.Errorf("All chip still active alongside the preselected category")
	}
	if !strings.Contains(body, `data-active-cat="import"`) {
		t.Errorf("chip row does not carry the preselected category for the filter script")
	}
}
