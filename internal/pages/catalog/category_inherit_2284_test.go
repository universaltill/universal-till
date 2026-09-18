package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2284: the item editor's side of category-level modifier groups
// (ADR-0094) and kitchen-station routing. A real migrated DB (db.Open), not
// testsupport.NewCatalogTestDB's hand-rolled schema: these tests need the
// kitchen_stations/category_station_routes/item_station_routes tables and
// the category-link tables exactly as migrations define them.
func setupInheritDeps(t *testing.T) (*http.ServeMux, *db.DB) {
	t.Helper()
	chdirToRepoRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "inherit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	for _, q := range []string{
		`INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm1','SKU1','Flat White',320,1,'cat1')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-anchor','SKU9','Anchor',100,1)`,
	} {
		if _, err := dbase.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: dbase.DB, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})
	return mux, dbase
}

func postPanel(t *testing.T, mux *http.ServeMux, path, form, hxTarget string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hxTarget != "" {
		req.Header.Set("Hx-Target", hxTarget)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The nested "Manage customization groups" panel lists the groups the item
// inherits from its category, labelled as such, each with a skip/use-again
// toggle — while the item's OWN directly-linked groups keep rendering
// exactly as before (regression: the per-item link path is unchanged).
// Opting out of an inherited group never touches a direct link to the same
// group (UnlinkGroupFromItemUnlessLastLink's job, not this one).
func TestItemModifierGroupsPanel_InheritedGroupsWithOptOutToggle(t *testing.T) {
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	modRepo := data.NewModifierRepo(dbase.DB)
	// gMilk: category-inherited only. gOwn: the item's own direct group.
	// gBoth: directly linked AND category-linked.
	for _, g := range []struct{ id, name string }{{"gMilk", "Milk"}, {"gOwn", "Extras"}, {"gBoth", "Syrup"}} {
		if _, err := modRepo.CreateGroup(ctx, g.id, "itm-anchor", g.name, false, 0, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := modRepo.SetCategoryModifierGroups(ctx, "cat1", []string{"gMilk", "gBoth"}); err != nil {
		t.Fatal(err)
	}
	if err := modRepo.LinkGroupToItem(ctx, "itm1", "gOwn", 0); err != nil {
		t.Fatal(err)
	}
	if err := modRepo.LinkGroupToItem(ctx, "itm1", "gBoth", 1); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Own groups render as read-only rows (ut-docs#2330: the item-scoped
	// panel is attach/detach-only, no inline rename form) — plain text, not
	// an editable name input.
	for _, want := range []string{`class="modifier-admin-group-name">Extras<`, `class="modifier-admin-group-name">Syrup<`} {
		if !strings.Contains(body, want) {
			t.Errorf("panel lost the item's own group %s:\n%s", want, body)
		}
	}
	// The inherited section, labelled, with a skip toggle per inherited
	// group — but NOT for gBoth: it is already directly linked, so its
	// direct-link row is where it belongs, and a second "skip" control
	// there would report success while changing nothing (the direct link
	// always wins in ResolveGroupsForItem regardless of a category
	// opt-out). Review finding, ut-docs#2284.
	if !strings.Contains(body, `data-inherited-group="gMilk"`) {
		t.Fatalf("panel missing the category-inherited row for gMilk:\n%s", body)
	}
	if strings.Contains(body, `data-inherited-group="gBoth"`) {
		t.Fatalf("gBoth is already directly linked — must not also render as an inherited row with a no-op skip toggle:\n%s", body)
	}
	if !strings.Contains(body, `hx-post="/api/catalog/modifier-group/opt-out"`) {
		t.Fatalf("inherited rows carry no opt-out toggle:\n%s", body)
	}
	if strings.Contains(body, `hx-post="/api/catalog/modifier-group/opt-in"`) {
		t.Fatalf("nothing is opted out yet, no opt-in toggle expected:\n%s", body)
	}

	// Opt out of Milk from the panel: 200, re-rendered panel now offers
	// "use again" for it, and the sale-time resolver drops it.
	rec = postPanel(t, mux, "/api/catalog/modifier-group/opt-out", "itemId=itm1&groupId=gMilk&panelItem=itm1", "modifier-groups-modal-list")
	if rec.Code != http.StatusOK {
		t.Fatalf("opt-out: %d %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `id="modifier-groups-modal-list"`) {
		t.Fatalf("opt-out must answer with the item panel fragment:\n%s", body)
	}
	if !strings.Contains(body, `hx-post="/api/catalog/modifier-group/opt-in"`) {
		t.Fatalf("opted-out row must offer opt-in:\n%s", body)
	}
	if rec.Header().Get("HX-Trigger") != "modifiers-changed" {
		t.Errorf("a successful opt-out changes what the sale screen prompts for — must fire modifiers-changed, got %q", rec.Header().Get("HX-Trigger"))
	}
	resolved, err := modRepo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, g := range resolved {
		ids[g.ID] = true
	}
	if ids["gMilk"] || !ids["gOwn"] || !ids["gBoth"] {
		t.Fatalf("after opt-out resolver = %v, want Extras+Syrup only", ids)
	}

	// Opting out of gBoth (directly linked too) must NOT remove the direct
	// link: the item still resolves it as its own copy.
	rec = postPanel(t, mux, "/api/catalog/modifier-group/opt-out", "itemId=itm1&groupId=gBoth&panelItem=itm1", "modifier-groups-modal-list")
	if rec.Code != http.StatusOK {
		t.Fatalf("opt-out gBoth: %d", rec.Code)
	}
	if n, _ := modRepo.GroupLinkCount(ctx, "gBoth"); n != 2 {
		t.Fatalf("opt-out must never unlink a direct link; gBoth link count = %d, want 2", n)
	}
	resolved, _ = modRepo.ResolveGroupsForItem(ctx, "itm1")
	found := false
	for _, g := range resolved {
		if g.ID == "gBoth" && g.ItemID == "itm1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("directly-linked gBoth must survive an opt-out as the item's own copy: %+v", resolved)
	}

	// Opt back in: the row is gone and Milk is offered again.
	rec = postPanel(t, mux, "/api/catalog/modifier-group/opt-in", "itemId=itm1&groupId=gMilk&panelItem=itm1", "modifier-groups-modal-list")
	if rec.Code != http.StatusOK {
		t.Fatalf("opt-in: %d %s", rec.Code, rec.Body.String())
	}
	resolved, _ = modRepo.ResolveGroupsForItem(ctx, "itm1")
	ids = map[string]bool{}
	for _, g := range resolved {
		ids[g.ID] = true
	}
	if !ids["gMilk"] {
		t.Fatalf("after opt-in resolver = %v, want Milk back", ids)
	}

	// Missing ids are a 400, never a silent no-op.
	if rec := postPanel(t, mux, "/api/catalog/modifier-group/opt-out", "itemId=itm1", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("opt-out without groupId = %d, want 400", rec.Code)
	}
}

// The item's Variants-tab summary line names inherited groups too (marked
// "from category"), so the compact panel doesn't claim "no customization
// groups yet" for an item that inherits three.
func TestCatalogVariantsPanel_SummaryNamesInheritedGroups(t *testing.T) {
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	modRepo := data.NewModifierRepo(dbase.DB)
	if _, err := modRepo.CreateGroup(ctx, "gMilk", "itm-anchor", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := modRepo.SetCategoryModifierGroups(ctx, "cat1", []string{"gMilk"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Milk (from category)") {
		t.Fatalf("summary must name the inherited group as from category:\n%s", body)
	}
	if strings.Contains(body, "No Modifiers yet.") { // ut-docs#2211 rename
		t.Fatalf("summary must not claim no groups when the category adds one:\n%s", body)
	}
}

// Kitchen routing on the item editor: the Variants panel shows what the
// item's category routes to, lets the item override it with its own
// station set (item_station_routes — replace-all, same rule as the
// kitchen-stations page), and unticking everything follows the category
// again. Absent entirely when the shop has no stations.
func TestCatalogVariantsPanel_KitchenRoutingOverride(t *testing.T) {
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	posRepo := data.NewPOSRepo(dbase.DB)

	panel := func() string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// No stations in the shop: no routing section at all.
	if body := panel(); strings.Contains(body, `id="item-routing-form"`) {
		t.Fatalf("routing section must be absent with zero stations:\n%s", body)
	}

	grill, err := posRepo.CreateKitchenStation(ctx, "Grill", data.KitchenDestinationPrinter, "192.0.2.1:9100")
	if err != nil {
		t.Fatal(err)
	}
	bar, err := posRepo.CreateKitchenStation(ctx, "Bar", data.KitchenDestinationPrinter, "192.0.2.2:9100")
	if err != nil {
		t.Fatal(err)
	}
	if err := posRepo.SetCategoryStationRoutes(ctx, "cat1", []string{bar}); err != nil {
		t.Fatal(err)
	}

	body := panel()
	if !strings.Contains(body, `id="item-routing-form"`) || !strings.Contains(body, `hx-post="/api/catalog/item-station-routes"`) {
		t.Fatalf("routing section missing:\n%s", body)
	}
	if !strings.Contains(body, "From category: Bar") {
		t.Fatalf("panel must show the category's routing:\n%s", body)
	}
	for _, id := range []string{grill, bar} {
		if !strings.Contains(body, `type="checkbox" name="station_id" value="`+id+`"`) {
			t.Fatalf("panel missing station checkbox %s:\n%s", id, body)
		}
	}
	if strings.Contains(body, "overrides its category") {
		t.Fatalf("no override yet, must not claim one:\n%s", body)
	}

	// Override: Grill only. Item routes win outright over the category.
	rec := postPanel(t, mux, "/api/catalog/item-station-routes", "panelItem=itm1&itemId=itm1&station_id="+grill, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("set override: %d %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `id="catalog-variants"`) || !strings.Contains(body, "overrides its category") {
		t.Fatalf("override must re-render the panel flagged as overriding:\n%s", body)
	}
	if !strings.Contains(body, `value="`+grill+`" checked`) {
		t.Fatalf("Grill must render ticked after the override:\n%s", body)
	}
	if routes, _ := posRepo.ItemStationRoutes(ctx, "itm1"); len(routes) != 1 || routes[0] != grill {
		t.Fatalf("item routes = %v, want [%s]", routes, grill)
	}
	resolved, err := posRepo.ResolveKitchenStations(ctx, []string{"itm1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved["itm1"]) != 1 || resolved["itm1"][0].ID != grill {
		t.Fatalf("resolver must follow the item override: %+v", resolved["itm1"])
	}

	// Untick everything: override removed, category rule applies again.
	rec = postPanel(t, mux, "/api/catalog/item-station-routes", "panelItem=itm1&itemId=itm1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear override: %d", rec.Code)
	}
	if routes, _ := posRepo.ItemStationRoutes(ctx, "itm1"); len(routes) != 0 {
		t.Fatalf("item routes must be cleared, got %v", routes)
	}
	resolved, _ = posRepo.ResolveKitchenStations(ctx, []string{"itm1"})
	if len(resolved["itm1"]) != 1 || resolved["itm1"][0].ID != bar {
		t.Fatalf("with no override the category's Bar must apply: %+v", resolved["itm1"])
	}

	// An unknown station id is refused, nothing written.
	if rec := postPanel(t, mux, "/api/catalog/item-station-routes", "panelItem=itm1&itemId=itm1&station_id=nope", ""); rec.Code == http.StatusOK {
		t.Fatalf("unknown station must be refused, got 200")
	}
	if routes, _ := posRepo.ItemStationRoutes(ctx, "itm1"); len(routes) != 0 {
		t.Fatalf("refused write must not persist, got %v", routes)
	}
}
