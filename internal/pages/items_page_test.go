package pages

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/uislot"
	layoutsalon "github.com/universaltill/universal-till/plugins/layout-salon"
)

func TestItemsPage_RendersFiveSectionsWithNameAndSubtitle(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Library reuses "nav.catalog" (= "Catalog") rather than a separate
	// "Library" name, so /catalog's own <h1> and this section's name match
	// (independent-review fix, ut-docs#1897).
	for _, want := range []string{"Catalog", "Categories", "Inventory", "Modifiers", "Option sets"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected section name %q in body", want)
		}
	}
	for _, want := range []string{
		"Your products and services",
		"Organise your items",
		"Track what&#39;s in stock", // html/template escapes the apostrophe
		"Add extras to items at checkout",
		// ut-docs#1900: was "Open an item to edit its variations" while the
		// generator had no screen of its own; now it describes the feature.
		"Generate item variants from reusable lists",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected subtitle %q in body", want)
		}
	}
}

func TestItemsPage_LibraryAndInventoryAndOptionSetsAndModifiersAreLiveLinks(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Count(body, `href="/catalog"`) != 1 { // Library only
		t.Errorf("expected exactly one link to /catalog (Library), got body: %s", body)
	}
	// ut-docs#1900: wired up in the same merge that landed
	// /catalog/option-sets — this row pointed at /catalog only because the
	// dedicated screen didn't exist yet (same story as Modifiers below).
	if !strings.Contains(body, `href="/catalog/option-sets"`) {
		t.Errorf("expected a link to /catalog/option-sets, got body: %s", body)
	}
	if !strings.Contains(body, `href="/inventory"`) {
		t.Errorf("expected a link to /inventory, got body: %s", body)
	}
	// ut-docs#1898: /categories shipped, so its section must be a real link
	// here. This section list is the only navigation to that page (it gets
	// no top-level nav tile of its own), so a regression back to a disabled
	// "coming soon" row makes the whole screen reachable by typed URL only.
	if !strings.Contains(body, `href="/categories"`) {
		t.Errorf("expected a link to /categories, got body: %s", body)
	}
	// ut-docs#1899: wired up in the same merge that landed /modifiers —
	// this row was disabled only because the screen didn't exist yet.
	if !strings.Contains(body, `href="/modifiers"`) {
		t.Errorf("expected a link to /modifiers, got body: %s", body)
	}
}

// TestItemsPage_NoSectionIsDisabled supersedes the two now-stale tests this
// replaced (TestItemsPage_ModifiersIsDisabledNotADeadLink and
// TestItemsPage_OnlyCategoriesIsDisabledNotDeadLink) — each was pinned to a
// transient state where exactly one section still lacked its own screen.
// ut-docs#1898 (categories) and ut-docs#1899 (modifiers) merged the same
// day and both wired up their Href, so all five sections are live links
// now; the disabled/"coming soon" rendering path itself stays in
// itemsSection/registerItemsPage for whenever the next section lands
// ahead of its own screen, it's just untested by name until that happens.
func TestItemsPage_NoSectionIsDisabled(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Count(body, `aria-disabled="true"`) != 0 {
		t.Errorf("expected no disabled sections, got body: %s", body)
	}
	if strings.Contains(body, "Coming soon") {
		t.Errorf("expected no coming-soon marker, got body: %s", body)
	}
}

func TestMenuPage_TopLevelTileIsItemsNotCatalogOrInventory(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, baseMenu)
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	// Scoped to the ☰ Menu launcher's own tile grid (class="menu-tile"),
	// not the whole page: nav.html's shared side rail keeps its own,
	// separate, deliberate one-tap "/inventory" shortcut on every page
	// (ut-docs#1332/#1349) — untouched by this card, out of scope.
	gridStart := strings.Index(body, `class="menu-grid"`)
	if gridStart < 0 {
		t.Fatalf("menu-grid not found in body: %s", body)
	}
	grid := body[gridStart:]

	if !strings.Contains(grid, `class="menu-tile" href="/items"`) {
		t.Errorf("expected the Items tile in the menu grid, got: %s", grid)
	}
	if strings.Contains(grid, `href="/catalog"`) {
		t.Errorf("did not expect a standalone Catalog tile in the menu grid, got: %s", grid)
	}
	if strings.Contains(grid, `href="/inventory"`) {
		t.Errorf("did not expect a standalone Inventory tile in the menu grid, got: %s", grid)
	}
}

// ut-docs#1950 AC #1/#4: the rail must be a narrow column of compact,
// single-line rows (.items-row), not the old large card tiles (.card).
func TestItemsPage_RailRowsAreCompactNotCardTiles(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="items-rail"`) {
		t.Fatalf("expected the compact rail, got: %s", body)
	}
	if strings.Count(body, `class="items-row`) < 5 {
		t.Errorf("expected 5 compact .items-row rows, got body: %s", body)
	}
	// The old layout's large tiles must be gone from the rail.
	railStart := strings.Index(body, `id="items-rail"`)
	railEnd := strings.Index(body[railStart:], "</nav>") + railStart
	rail := body[railStart:railEnd]
	if strings.Contains(rail, `class="card"`) {
		t.Errorf("rail still renders large card tiles, got: %s", rail)
	}
}

// ut-docs#1950 AC #3: a bare /items load must not leave the right panel
// empty — the first section (Library/Catalog) renders inline.
func TestItemsPage_DefaultLoadEmbedsFirstSectionInPanel(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	panelStart := strings.Index(body, `id="items-panel"`)
	if panelStart < 0 {
		t.Fatalf("expected #items-panel, got: %s", body)
	}
	panel := body[panelStart:]
	// The catalog page's own table markup (catalog_table.html) must be
	// present inline — not just an empty placeholder waiting on JS.
	if !strings.Contains(panel, "catalog") && !strings.Contains(panel, "Catalog") {
		t.Errorf("expected /catalog's own content embedded in the panel, got: %s", panel)
	}
	// The catalog fragment must not drag its own full-page chrome in with
	// it (that would double up nav/status bar inside /items' own page).
	if strings.Count(body, `class="nav"`) != 1 {
		t.Errorf("expected exactly one nav rail (the /items page's own), got body: %s", body)
	}
	// AC #2: the rail shows which section is selected.
	railStart := strings.Index(body, `id="items-rail"`)
	rail := body[railStart:]
	if !strings.Contains(rail[:strings.Index(rail, "</nav>")], `href="/catalog"`) {
		t.Fatalf("rail missing the /catalog row: %s", rail)
	}
	catalogLinkIdx := strings.Index(rail, `href="/catalog"`)
	tagStart := strings.LastIndex(rail[:catalogLinkIdx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("expected the /catalog rail row to be marked is-current on default load: %s", rail[tagStart:tagEnd])
	}
}

// Review of ut-docs#1950: the embedded default panel must NOT bring the
// section handler's own out-of-band rail copy along with it. /items already
// draws the rail immediately above the panel, so a second one is a
// duplicate DOM id AND paints the whole section list a second time inside
// the right panel — a bare GET /items rendered 2 rails and 10 rows instead
// of 1 and 5. The embed sub-request signals this with itemsnav.EmbedHeader;
// itemsnav.WriteRailOOB skips the OOB copy when it is set.
func TestItemsPage_DefaultLoadRendersExactlyOneRail(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if n := strings.Count(body, `id="items-rail"`); n != 1 {
		t.Errorf(`expected exactly 1 id="items-rail" on a bare /items load, got %d`, n)
	}
	if n := strings.Count(body, `class="items-row`); n != len(uislot.CoreItems) {
		t.Errorf("expected exactly %d .items-row rows, got %d", len(uislot.CoreItems), n)
	}
	// An hx-swap-oob element sitting in the INITIAL page markup is always a
	// mistake: it is meaningful only in a swapped htmx response.
	if strings.Contains(body, `hx-swap-oob`) {
		t.Errorf("bare /items page must not contain an hx-swap-oob element, got: %s", body)
	}
	// ...and specifically not inside the panel.
	panel := body[strings.Index(body, `id="items-panel"`):]
	if strings.Contains(panel, `id="items-rail"`) {
		t.Errorf("the embedded panel must not contain a second copy of the rail: %s", panel)
	}
}

// A REAL htmx panel swap (no embed header) must still get the out-of-band
// rail — the fix above must not disable the mechanism it exists for.
func TestItemsSection_RealHTMXSwapStillGetsOOBRail(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /catalog: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="items-rail"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("a real htmx swap must still carry the OOB rail, got: %s", body)
	}
}

// ut-docs#1911's demonstration AC end to end, against the REAL shipped
// plugin, not a test fixture: installing plugins/layout-salon (the same
// manifest builtinlayouts.Sync installs for shop_type=service) makes the
// /items rail's Library row read "Services" and move to the front — the
// Items slot amended by the same mechanism the Menu tile already was.
// installShippedSalonLayout installs the REAL shipped plugins/layout-salon manifest
// (the same one builtinlayouts.Sync installs for shop_type=service) into
// dp's till and reloads plugin state — shared by every test that proves an
// ADR-0088 slot end to end against that plugin rather than a fixture
// (menu_layout_settings_test.go's installSalonLayout is the FIXTURE twin:
// a hand-built hide-only manifest for the restore-surface tests).
//
// syncLocales (internal/plugins.Manager) reads a plugin's locale files back
// from paths.Plugins() on disk, never from the manifest bytes (Decision G)
// — so a real overlay needs them written there first, the same step
// internal/plugins/builtinlayouts.installSalon takes for a real till.
// Isolated to a temp dir so this test can't touch (or race) another test's
// plugin files. Call BEFORE newMenuPageTestDeps (which, via its own
// pm.SetLocalizer call, does a paths.Plugins() read) so that read already
// sees the isolated dir, not repo-root ./data — same orig/restore shape as
// sync_assets_test.go's own paths.Init use (independent review of
// ut-docs#1911, finding 11).
func isolatePluginDir(t *testing.T) {
	t.Helper()
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })
}

func installShippedSalonLayout(t *testing.T, dp *common.Deps) {
	t.Helper()
	m, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatalf("parse plugins/layout-salon/plugin.json: %v", err)
	}
	localeEntries, err := fs.ReadDir(layoutsalon.Locales, "locales")
	if err != nil {
		t.Fatalf("read embedded salon locales: %v", err)
	}
	destDir := paths.Plugins(m.ID, m.Version, "locales")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range localeEntries {
		raw, err := fs.ReadFile(layoutsalon.Locales, path.Join("locales", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destDir, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.PersistManifest(t.Context(), dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatalf("install salon layout: %v", err)
	}
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestItemsPage_SalonLayoutRelabelsAndReordersLibraryRow(t *testing.T) {
	isolatePluginDir(t)
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)
	installShippedSalonLayout(t, dp)

	body := getPage(t, mux, "/items").Body.String()
	if !strings.Contains(body, "Services") {
		t.Fatalf("expected the relabelled row to render \"Services\", got: %s", body)
	}
	// The rail's five rows are all still there — a relabel/reorder amendment
	// must not drop or duplicate any.
	if n := strings.Count(body, `class="items-row`); n != len(uislot.CoreItems) {
		t.Errorf("expected exactly %d .items-row rows after the amendment, got %d", len(uislot.CoreItems), n)
	}
	// Reordered to Order 50 (ahead of every other row's 100+), so it must be
	// the FIRST link in the rail, not merely present.
	rail := body[strings.Index(body, `id="items-rail"`):]
	afterAttr := rail[strings.Index(rail, `href="`)+len(`href="`):]
	firstHref := afterAttr[:strings.Index(afterAttr, `"`)]
	if firstHref != "/catalog" {
		t.Errorf("expected /catalog's row to be reordered first, got first href %q", firstHref)
	}
}

// ut-docs#1912's demonstration AC end to end, against the REAL shipped
// plugin: installing plugins/layout-salon moves Orders ahead of Inventory
// in the nav rail on every page (here: /items, but nav.html is a shared
// partial, so any whole-page render would do) — the rail amended by the
// same mechanism the Menu tile and the /items section list already are.
// Nothing else about the rail changes: the same four anchors, the same
// data-testids, Sell/Menu still first.
func TestNavRail_SalonLayoutReordersOrdersAheadOfInventory(t *testing.T) {
	isolatePluginDir(t)
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	before := getPage(t, mux, "/items").Body.String()
	if i, o := strings.Index(before, `data-testid="kiosk-inventory-link"`), strings.Index(before, `data-testid="nav-orders"`); i < 0 || o < 0 || o < i {
		t.Fatalf("zero-plugin rail must list Inventory before Orders, got inventory@%d orders@%d", i, o)
	}

	installShippedSalonLayout(t, dp)

	body := getPage(t, mux, "/items").Body.String()
	rail := body[strings.Index(body, `<div class="nav-primary">`):strings.Index(body, `<div class="nav-right">`)]
	var testids []string
	for _, m := range regexp.MustCompile(`data-testid="([^"]+)"`).FindAllStringSubmatch(rail, -1) {
		testids = append(testids, m[1])
	}
	want := []string{"nav-till", "nav-menu", "nav-orders", "kiosk-inventory-link"}
	if strings.Join(testids, ",") != strings.Join(want, ",") {
		t.Fatalf("salon layout must reorder the rail to %v, got %v", want, testids)
	}
	if !strings.Contains(rail, `href="/orders" class="nav-toggle nav-rail-only" data-testid="nav-orders"`) {
		t.Fatalf("the moved Orders anchor must keep its classes and testid, got: %s", rail)
	}
}
