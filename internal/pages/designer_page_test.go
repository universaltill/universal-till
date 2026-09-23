package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// registerDesigner renders the shortcut-button designer, backed by the
// shortcut_buttons table (not in seedForPages), so use a migrated database.
func newDesignerTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d := openPagesTestDB(t)
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "monarch", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, d)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		BtnStore: ui.NewButtonStore(d),
		Pm:       pm,
		Settings: settings.NewStore(d),
		// A real auth.Service over this func's own real migrated DB
		// (openPagesTestDB, not seedForPages' hand-rolled schema) so
		// TestDesigner_PagePermissions below can exercise the real
		// role_permissions seed (ut-docs#2357) -- every other test in this
		// file sets UT_AUTH=off and never reaches it.
		AuthSvc: auth.NewService(d),
	}
}

func TestDesigner_RendersPage(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#2357: /designer, /items, /catalog, /modifiers, /catalog/option-sets are now catalog_management-gated; this test is about rendering, not permissions.
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The designer page composes the buttons_admin partial.
	if !strings.Contains(body, "designer") {
		t.Fatalf("expected the designer container, got %s", body)
	}
}

func TestDesigner_RendersSeededButtons(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#2357: /designer, /items, /catalog, /modifiers, /catalog/option-sets are now catalog_management-gated; this test is about rendering, not permissions.
	dp := newDesignerTestDeps(t)

	// The migrated schema seeds sample shortcut buttons; add a deterministic one
	// against a known item so the grid renders its label through ToVM.
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmZ','ZZZ','Zephyr Widget',500,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := dp.BtnStore.Add(ui.Button{Label: "Zephyr Tile", Code: "ZZZ", ItemID: "itmZ"}); err != nil {
		t.Fatalf("add button: %v", err)
	}

	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	// ut-docs#2174: /designer no longer inlines a flat admin grid of its
	// own — it renders the SAME self-refreshing placeholder the sale screen
	// (index.html) does, pointed at /ui/buttons in edit mode, so the page is
	// a live replica of the real product panel. hx-get must stay exactly
	// "/ui/buttons" (app.js's utTileJiggle unsaved-drag guard matches
	// `.products[hx-get="/ui/buttons"]` verbatim); the mode rides on
	// hx-vals, which htmx appends to a GET as ?mode=edit.
	body := rec.Body.String()
	for _, want := range []string{
		`class="products" hx-get="/ui/buttons" hx-vals='{"mode":"edit"}' hx-trigger="load" hx-swap="outerHTML"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET /designer missing the live-replica placeholder %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{`id="buttons-grid-admin"`, `reorderable-tile`, "Zephyr Tile"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("GET /designer still renders the retired flat admin grid (%q): %s", unwanted, body)
		}
	}

	// The label itself now arrives via the edit-mode fragment.
	bmux, bd := newButtonsMuxRealSession(t)
	if _, err := bd.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmZ','ZZZ','Zephyr Widget',500,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := bd.BtnStore.Add(ui.Button{Label: "Zephyr Tile", Code: "ZZZ", ItemID: "itmZ"}); err != nil {
		t.Fatalf("add button: %v", err)
	}
	mgr := auth.User{ID: "m1", Role: "manager"}
	frag := getWithUser(bmux, "/ui/buttons?mode=edit", &mgr)
	if frag.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons?mode=edit: code %d body %s", frag.Code, frag.Body.String())
	}
	if !strings.Contains(frag.Body.String(), "Zephyr Tile") {
		t.Fatalf("edit-mode fragment missing the seeded button label: %s", frag.Body.String())
	}
}

// TestButtonsPartial_EditMode (ut-docs#2174): GET /ui/buttons?mode=edit is
// the Designer's live replica of the sale screen's product panel — the
// exact same buttons.html template, with the edit-mode affordances added and
// the sale-only ones removed:
//   - gated on catalog_management (a cashier gets a plain 403, never the
//     category-management UI);
//   - the root re-fetches itself in edit mode on buttons-changed (hx-vals
//     carries the mode, hx-get stays "/ui/buttons" — see the placeholder
//     test above for why);
//   - tiles are INERT: no /api/pos/scan or modifier-picker wiring, so a tap
//     on the Designer can never add to the cashier's live basket — but they
//     keep data-code/data-pos and the .tile-cell/badge markup app.js's
//     utTileJiggle needs, so long-press/drag/arrow-key reorder works there
//     unchanged, and the edit badge returns to /designer, not /;
//   - no sale-screen search, no "edit quick buttons" link back to itself,
//     no All tab, no plugin action strip;
//   - a category-management section listing EVERY category (active and
//     inactive, with or without buttons) with real, keyboard-reachable
//     controls for create / rename+recolour / reorder / deactivate.
func TestButtonsPartial_EditMode(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	for _, stmt := range []string{
		// Two categories WITH buttons (so the strip actually renders a tab
		// bar — with one real category the sale screen, and therefore the
		// replica, shows a single headed group and no tabs), one active
		// category with no buttons, one inactive.
		`INSERT INTO categories(id,name,sort_order,is_active,color) VALUES ('cat-a','Drinks',0,1,'#0f766e'),('cat-b','Snacks',1,1,NULL),('cat-empty','Unused',2,1,NULL),('cat-off','Retired',3,0,NULL)`,
		`UPDATE items SET category_id='cat-a' WHERE id='itm-btn'`,
		`INSERT INTO items(id,sku,name,base_price,is_active,category_id) VALUES ('itm-b','B-SKU','Crisps',150,1,'cat-b')`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('BTNB','Crisps','itm-b',2)`,
	} {
		if _, err := d.Db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cashier := auth.User{ID: "c1", Role: "cashier"}
	if rec := getWithUser(mux, "/ui/buttons?mode=edit", &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier /ui/buttons?mode=edit = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// The normal sale-screen render is untouched: no edit-mode section, a
	// live scan tile, the link to /designer still there.
	plain := getWithUser(mux, "/ui/buttons", &cashier)
	if plain.Code != http.StatusOK {
		t.Fatalf("cashier /ui/buttons = %d: %s", plain.Code, plain.Body.String())
	}
	for _, unwanted := range []string{`designer-categories`, `hx-vals='{"mode":"edit"}'`} {
		if strings.Contains(plain.Body.String(), unwanted) {
			t.Fatalf("plain /ui/buttons must not carry edit-mode markup %q", unwanted)
		}
	}
	for _, want := range []string{`hx-post="/api/pos/scan"`, `href="/designer"`, `href="/catalog?item=itm-btn&return=/"`,
		// ut-docs#2174 review: the sale screen keeps its own search results
		// region and restores a persisted search -- only edit mode drops them.
		`id="search-results"`, `this.q = st.q`} {
		if !strings.Contains(plain.Body.String(), want) {
			t.Fatalf("plain /ui/buttons lost %q", want)
		}
	}

	mgr := auth.User{ID: "m1", Role: "manager"}
	rec := getWithUser(mux, "/ui/buttons?mode=edit", &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager /ui/buttons?mode=edit = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="products" hx-get="/ui/buttons" hx-vals='{"mode":"edit"}' hx-trigger="modifiers-changed from:body, buttons-changed from:body" hx-swap="outerHTML"`,
		// The real category strip + tile grid, same ids app.js/Alpine key off.
		`id="cat-tab-cat-a"`, `id="cat-tab-cat-b"`, `id="buttons-grid"`, `class="tile-cell"`,
		`data-code="BTN" data-item-id="itm-btn" data-pos="0"`,
		`data-testid="jiggle-done"`,
		`href="/catalog?item=itm-btn&return=/designer"`,
		// Category management: every category, real controls, keyboard paths.
		`class="designer-categories"`,
		`data-cat-id="cat-a"`, `data-cat-id="cat-b"`, `data-cat-id="cat-empty"`, `data-cat-id="cat-off"`,
		`data-cat-move="-1"`, `data-cat-move="1"`,
		`hx-post="/api/designer/categories"`,
		`hx-post="/api/designer/categories/cat-a"`,
		`hx-post="/api/designer/categories/cat-a/active"`,
		`hx-post="/api/designer/categories/cat-off/active"`,
		`name="active" value="1"`,
		`type="radio" name="color" value="#0f766e" checked`,
		`id="designer-categories-msg"`,
		// A category with active items says up front that deactivating it
		// will be refused, and why, before the operator taps anything.
		`data-testid="designer-cat-blocked-cat-a"`,
		// Edge tiles: the first category can't move earlier, the last can't
		// move later — rendered disabled by the server, which knows the
		// order, rather than fixed up client-side after the fact.
		`id="designer-cat-up-cat-a" data-cat-move="-1" disabled`,
		`id="designer-cat-down-cat-off" data-cat-move="1" disabled`,
		`id="designer-cat-up-cat-empty" data-cat-move="-1" aria-label`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("edit-mode fragment missing %q: %.3000s", want, body)
		}
	}
	for _, unwanted := range []string{
		`hx-post="/api/pos/scan"`, `hx-get="/ui/pos/modifiers`, // inert tiles
		`href="/designer"`,                          // no link back to itself
		`id="products-search"`,                      // no sale-screen search
		`id="cat-tab-all"`, `id="buttons-grid-all"`, // no All tab
		`hx-get="/ui/plugin-buttons"`,                  // no plugin action strip
		`data-testid="designer-cat-blocked-cat-empty"`, // nothing blocks an empty category
		// ut-docs#2174 review: /designer already has its own #search-results
		// (buttons_admin.html's add dropdown) -- a second would be a
		// duplicate id -- and a sale-screen search persisted in the
		// window-global utSaleGridState must never be restored into the
		// replica (it would hide the grid behind an empty results view the
		// replica has no search box to clear).
		`id="search-results"`, `this.q = st.q`,
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("edit-mode fragment must not contain %q: %.3000s", unwanted, body)
		}
	}
	// The strip only shows categories that actually have buttons — a
	// WYSIWYG replica — while the management list shows all three.
	for _, unwanted := range []string{`id="cat-tab-cat-empty"`, `id="cat-tab-cat-off"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("edit-mode strip must mirror the sale screen (no tab for %q)", unwanted)
		}
	}
}

// The on-screen keyboard (web/public/osk.js) types by calling setRangeText
// then dispatchEvent(new Event('input')) — a tapped virtual key never fires
// a native keydown/keyup, only a synthetic "input". A search box trigger
// scoped to "keyup" alone (as this one was before ut-docs#196) never fires
// for OSK-driven typing, so product search silently returns nothing on
// touch tills while working fine on a desktop keyboard. Guard: the trigger
// must include "input", the event OSK actually dispatches.
func TestDesigner_SearchBoxTriggerFiresOnSyntheticInputEvent(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#2357: /designer, /items, /catalog, /modifiers, /catalog/option-sets are now catalog_management-gated; this test is about rendering, not permissions.
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	idx := strings.Index(body, `id="search"`)
	if idx == -1 {
		t.Fatalf("search box missing from designer page: %s", body)
	}
	tagStart := strings.LastIndex(body[:idx], "<input")
	if tagStart == -1 {
		t.Fatalf("no <input tag containing id=\"search\": %s", body)
	}
	tagEnd := strings.Index(body[idx:], ">")
	tag := body[tagStart : idx+tagEnd]
	if !strings.Contains(tag, `hx-trigger="input`) {
		t.Fatalf("search box hx-trigger must fire on the \"input\" event (OSK-compatible), got: %s", tag)
	}
}

// TestDesigner_HelpHintResolvesToOwnTopic (ut-docs#1388) guards against the
// contextual "?" on /designer resolving to an unrelated topic. It used to
// land on "Catalog, variants & barcodes" because catalog.md's front matter
// claimed the /designer route — a stale claim from before Till Designer had
// its own manual page. Anchored on data-testid="help-hint" the same way
// TestHelpHintResolvesPerPage is, independent of markup/attribute order.
func TestDesigner_HelpHintResolvesToOwnTopic(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#2357: /designer, /items, /catalog, /modifiers, /catalog/option-sets are now catalog_management-gated; this test is about rendering, not permissions.
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	tag := helpHintTag.FindString(rec.Body.String())
	if tag == "" {
		t.Fatal("no help hint rendered on /designer")
	}
	m := helpHintHrefAttr.FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("help hint tag has no href: %s", tag)
	}
	if m[1] != "/help/till-designer" {
		t.Errorf("hint on /designer → %s, want /help/till-designer", m[1])
	}
}

// TestDesigner_RendersAddErrorSurface (ut-docs#1220) guards the Designer's
// only channel for telling an operator that "add as button" failed. htmx
// swaps nothing into hx-target for a non-2xx, so without the dedicated
// #buttons-add-error element plus a page-level htmx:responseError listener,
// a rejected add is invisible — the exact silent failure the card reports.
// htmx:sendError covers the transport half (tablet off the LAN), which
// never reaches responseError and carries no response body, so it needs the
// locale copy rendered into the page rather than read off the xhr.
func TestDesigner_RendersAddErrorSurface(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#2357: /designer, /items, /catalog, /modifiers, /catalog/option-sets are now catalog_management-gated; this test is about rendering, not permissions.
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="buttons-add-error"`) {
		t.Fatalf("designer page has no #buttons-add-error element for a failed add to render into")
	}
	for _, ev := range []string{"htmx:responseError", "htmx:sendError"} {
		if !strings.Contains(body, ev) {
			t.Fatalf("designer page registers no %s listener — a failed add on that path stays invisible", ev)
		}
	}
	// The sendError arm has no response body to show, so its copy must be
	// the rendered locale string, not a hardcoded literal or an empty one.
	if want := httpx.T("en", buttonsErrorKey); !strings.Contains(body, want) {
		t.Fatalf("designer page does not render the localized %s copy %q for the transport-failure message", buttonsErrorKey, want)
	}
}

// TestDesigner_PagePermissions (ut-docs#2357): /designer's nav tile is
// already VisibleIf: "catalog_management" (uislot.CoreAdmin), but nothing
// stopped a cashier who typed the URL directly before this card. Same
// contract and rig shape as locations_page_test.go's
// TestLocationsPagePermissions (cashier 403 + rail intact) and
// catalog/catalog_management_gate_test.go's role sweep.
func TestDesigner_PagePermissions(t *testing.T) {
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	cashier := auth.User{ID: "c1", Role: "cashier"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/designer", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /designer = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /designer has no nav rail:\n%s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-session GET /designer = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	for _, role := range []string{"manager", "admin", "super_admin"} {
		mgr := auth.User{ID: "u-" + role, Role: role}
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/designer", nil), mgr)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s GET /designer = 403, want past the catalog_management gate: %s", role, rec.Body.String())
		}
	}
}
