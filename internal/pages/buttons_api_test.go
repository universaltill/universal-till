package pages

import (
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	// Aliased: this file already imports the stdlib "html" for escaping.
	xhtml "golang.org/x/net/html"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// newButtonsMux wires registerButtonsAPI over a DB seeded with the pages
// fixture (openPagesTestDB now runs the real migrations, ut-docs#1657/#1677
// — shortcut_buttons and item_images come from there, including their real
// FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE CASCADE).
func newButtonsMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	// ut-docs#2312: the mutating routes registerButtonsAPI wires now gate on
	// catalog_management. Every pre-existing test built on this helper
	// predates that gate and isn't itself exercising auth/role behaviour, so
	// it bypasses the gate the same way every other already-gated page's
	// test suite does for its own auth-agnostic tests (e.g.
	// tax_codes_page_test.go) -- the dedicated real-session gate tests below
	// override this back to "on" via their own t.Setenv.
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	d := &common.Deps{Db: db, BtnStore: ui.NewButtonStore(db), Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)
	return mux, d
}

// newButtonsAndCatalogMux is newButtonsMux plus catalog.Register on the SAME
// mux/DB — needed by the ut-docs#2210 regression tests below that drive the
// REAL Designer/item-detail modifier-group endpoints (create/update/attach/
// detach) rather than raw SQL, so a mutation neither the UI nor those
// handlers could actually produce can never sneak into a test's setup.
func newButtonsAndCatalogMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	// ut-docs#2312: see newButtonsMux's identical comment above -- this
	// helper also wires catalog.Register, whose mutating routes now gate on
	// catalog_management too.
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	d := &common.Deps{
		Db:       db,
		BtnStore: ui.NewButtonStore(db),
		Settings: settings.NewStore(db),
		State:    common.RuntimeState{Theme: "default", Currency: "GBP"},
		Menu:     []common.MenuItem{},
	}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)
	catalog.Register(mux, d)
	return mux, d
}

// assertTileOpensModifiers checks, robustly against attribute/param
// reordering, that body's tile for itemID/code routes a tap to the
// customization picker rather than a straight scan.
func assertTileOpensModifiers(t *testing.T, body, itemID, code string) {
	t.Helper()
	if !strings.Contains(body, `hx-get="/ui/pos/modifiers?`) {
		t.Fatalf("expected a tile opening the customization picker (hx-get=\"/ui/pos/modifiers?...\"), got: %.1200s", body)
	}
	for _, want := range []string{"item=" + itemID, "code=" + code} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected the /ui/pos/modifiers query to carry %q, got: %.1200s", want, body)
		}
	}
}

// assertTileScansDirectly is assertTileOpensModifiers's opposite: the tile
// must post straight to /api/pos/scan and must NOT open the picker at all.
func assertTileScansDirectly(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, `hx-post="/api/pos/scan"`) {
		t.Fatalf("expected a tile scanning straight to the basket (hx-post=\"/api/pos/scan\"), got: %.1200s", body)
	}
	if strings.Contains(body, "/ui/pos/modifiers") {
		t.Fatalf("tile must not offer the customization picker, got: %.1200s", body)
	}
}

// seedPlainItem inserts a catalog item with NO variants and NO modifier
// groups -- deliberately NOT itm1 (seedForPages gives itm1 a real variant,
// 'var1', for other tests' own purposes). Since ut-docs#2209's
// {{ if or .HasModifiers .HasVariants }} gate (buttons.html:378,
// internal/ui/buttons.go's Store.Load()), an item with ANY active variant
// already routes its tile to the customization picker regardless of
// modifier-group state, which would make itm1 a false negative for these
// ut-docs#2210 tests' "before attaching, the tile scans straight to the
// basket" baseline. A dedicated variant-free item isolates the assertion
// to the modifier-group axis alone.
func seedPlainItem(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES(?,?,?,100,1)`, id, id+"-SKU", id); err != nil {
		t.Fatalf("seed plain item %s: %v", id, err)
	}
}

func TestButtonsUIFragmentRendersSeededButtons(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('ABC','Apple Tile','itm1')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Apple Tile") {
		t.Fatalf("buttons fragment missing seeded label: %s", rec.Body.String())
	}
}

// The sale-screen tile itself (not just the Designer admin panel) previously
// interpolated a shortcut button's barcode/code directly into a hand-written
// hx-vals JSON literal -- same bug class as buttons_admin.html's AddVals fix,
// but higher-impact: this is the tile a cashier actually taps at checkout
// (ut-docs#19, flagged by independent review as the highest-impact miss in
// the original sweep). shortcut_buttons.barcode has no format restriction
// (TestBarcodeAttach_PanelHxValsSurvivesQuotedBarcode already proves a
// quote-containing barcode can be attached), so a button seeded with one
// must still round-trip as valid JSON via httpx.jsonVals.
func TestButtonsUIFragment_HxValsSurvivesQuotedCode(t *testing.T) {
	mux, d := newButtonsMux(t)
	weird := `we"ird'code`
	// ut-docs#2209: this test needs a tile on the PLAIN (straight-to-basket)
	// branch, and seedForPages' itm1 carries a variant (var1, seeded for
	// ut-docs#744), so its tile now correctly opens the variant picker via
	// hx-get instead of posting hx-vals to /api/pos/scan. Seed a
	// variant-less, modifier-less item of our own rather than relaxing the
	// assertion -- the escaping this test guards is a property of the plain
	// tile specifically, and asserting it against the picker branch would
	// silently stop testing it.
	if _, err := d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm_novar','NOVAR','Plain',100,'tax_std',1)`); err != nil {
		t.Fatalf("seed variant-less item: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES (?,'Weird Tile','itm_novar')`, weird); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	idx := strings.Index(body, `hx-post="/api/pos/scan"`)
	if idx == -1 {
		t.Fatalf("expected a sale-screen tile posting to /api/pos/scan, got %.500s", body)
	}
	valsIdx := strings.Index(body[idx:], "hx-vals='")
	if valsIdx == -1 {
		t.Fatalf("expected an hx-vals attribute on the tile, got %.500s", body[idx:])
	}
	start := idx + valsIdx + len("hx-vals='")
	end := strings.Index(body[start:], "'")
	if end == -1 {
		t.Fatalf("unterminated hx-vals attribute")
	}
	raw := body[start : start+end]

	var vals map[string]string
	if err := json.Unmarshal([]byte(html.UnescapeString(raw)), &vals); err != nil {
		t.Fatalf("hx-vals is not valid JSON after HTML-unescape: %v (raw attribute: %q)", err, raw)
	}
	if vals["code"] != weird {
		t.Errorf("code round-trip = %q, want %q", vals["code"], weird)
	}
}

func TestButtonsAddValidatesPersistsAndNormalizesImage(t *testing.T) {
	mux, d := newButtonsMux(t)

	// Missing required fields -> 400 from the store's validation, with a
	// localized HTML body an htmx:responseError listener can show the
	// operator (ut-docs#1220: a bare http.Error text body here used to
	// leave the failure completely invisible on the Designer screen — the
	// dropdown just closed with nothing else happening).
	//
	// Assert the localized copy, not merely a non-empty body: the reverted
	// http.Error(w, err.Error(), 400) also wrote a non-empty body, so a
	// blank-check alone passes against the very regression it claims to
	// pin. This also mechanically ties ui.designerErrorServerKey (which
	// internal/ui has to duplicate — internal/pages/common imports
	// internal/ui, so the reverse import is a cycle) to this file's
	// buttonsErrorKey: if either drifts, this assertion fails.
	rec := postForm(mux, "/api/buttons/add", url.Values{"label": {"No Code"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("add without code/itemId = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "label, code, and itemId are required") {
		t.Fatalf("raw Go validation error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if want := httpx.T("en", buttonsErrorKey); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("add error body = %q, want the localized %s copy %q", rec.Body.String(), buttonsErrorKey, want)
	}

	// A bare image filename is normalized into /public/images/.
	rec = postForm(mux, "/api/buttons/add", url.Values{
		"label":    {"Apple"},
		"code":     {"ABC"},
		"itemId":   {"itm1"},
		"imageUrl": {"apple.png"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d (%s)", rec.Code, rec.Body.String())
	}
	var image string
	if err := d.Db.QueryRow(`SELECT image_path FROM shortcut_buttons WHERE barcode='ABC'`).Scan(&image); err != nil {
		t.Fatalf("added button missing: %v", err)
	}
	if image != "/public/images/apple.png" {
		t.Fatalf("image_path = %q, want /public/images/apple.png", image)
	}
	// ut-docs#2174: the response used to be the re-rendered flat admin grid;
	// the Designer is a live replica of the sale screen now and refreshes
	// off the HX-Trigger header instead, so the success body is empty.
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("add HX-Trigger = %q, want buttons-changed", got)
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("add success body should be empty (no admin grid to re-render), got: %s", rec.Body.String())
	}

	// An absolute URL is stored untouched.
	rec = postForm(mux, "/api/buttons/add", url.Values{
		"label":    {"Pear"},
		"code":     {"DEF"},
		"itemId":   {"itm1"},
		"imageUrl": {"https://cdn.example.com/pear.png"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add absolute-url = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT image_path FROM shortcut_buttons WHERE barcode='DEF'`).Scan(&image); err != nil {
		t.Fatalf("added button missing: %v", err)
	}
	if image != "https://cdn.example.com/pear.png" {
		t.Fatalf("image_path = %q, want the absolute URL untouched", image)
	}
}

func TestButtonsRemoveDeletesRow(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('ABC','Apple','itm1')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := postForm(mux, "/api/buttons/remove", url.Values{"code": {"ABC"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d (%s)", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM shortcut_buttons WHERE barcode='ABC'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("button still present after remove")
	}
}

func TestButtonsSearchShortQueryHintAndResults(t *testing.T) {
	mux, _ := newButtonsMux(t)

	// Under 3 characters: a hint, not a query.
	rec := postForm(mux, "/api/buttons/search", url.Values{"q": {"Ap"}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Type 3+ characters") {
		t.Fatalf("short query: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// A real query finds the seeded item (name "Apple", barcode ABC) via
	// either the q or the legacy search field name.
	for _, field := range []string{"q", "search"} {
		rec = postForm(mux, "/api/buttons/search", url.Values{field: {"Appl"}}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("search via %s = %d (%s)", field, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Apple") {
			t.Fatalf("search via %s missing seeded item: %s", field, rec.Body.String())
		}
	}

	// A large offset pages past the single result.
	rec = postForm(mux, "/api/buttons/search?offset=50", url.Values{"q": {"Appl"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("offset search = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("offset=50 should page past the only result: %s", rec.Body.String())
	}

	// A non-numeric offset is ignored, not an error.
	rec = postForm(mux, "/api/buttons/search?offset=bogus", url.Values{"q": {"Appl"}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("bogus offset: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestButtonsReorderEdgeCases(t *testing.T) {
	mux, d := newButtonsMux(t)
	// item_id is NOT NULL with a real FK to items(id) now that openPagesTestDB
	// runs real migrations (ut-docs#1676/#1677) -- itm1 is seedForPages' own
	// fixture item; reordering doesn't care which item each button points at.
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('b1','A','itm1'),('b2','B','itm1'),('b3','C','itm1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// No codes at all -> 400.
	if rec := postForm(mux, "/api/buttons/reorder", url.Values{}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reorder = %d, want 400", rec.Code)
	}

	// A single comma-joined field is split and applied (urlencoded path).
	rec := postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"b2, b3, b1"}}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("comma-joined reorder = %d (%s)", rec.Code, rec.Body.String())
	}
	var first string
	if err := d.Db.QueryRow(`SELECT barcode FROM shortcut_buttons ORDER BY sort_order LIMIT 1`).Scan(&first); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if first != "b2" {
		t.Fatalf("first after reorder = %q, want b2 (comma-joined codes must split + trim)", first)
	}
}

// ut-docs#944 (ut-docs#924 increment 2 of 4): both UpdateOrder and
// SearchItems failures used to leak raw Go/SQL error text (err.Error()) via
// http.Error(w, err.Error(), 500) regardless of locale. These are the only
// two of this file's six raw-leak sites reachable by a real, non-mocked
// failure from an HTTP request -- the other four (ui.NewRenderer parse
// failures in the /ui/buttons, /api/buttons/add, /api/buttons/remove and
// /api/buttons/search handlers) parse an embed.FS baked in at compile time,
// so there is no live input that can make them fail; they get the identical
// fix for consistency/defense-in-depth but no forced-failure test, since
// fabricating one wouldn't exercise the real code path (see this file's
// package comment near buttonsErrorKey's definition in buttons_api.go).
func TestButtonsStoreErrorsSurfaceAs500(t *testing.T) {
	mux, d := newButtonsMux(t)
	// ut-docs#1679: DROP TABLE shortcut_buttons/items used to break storage
	// under both handlers below, but items now has real incoming FKs that
	// block the DROP under real migrations. A closed *sql.DB forces the
	// same generic repo-error path for both requests deterministically.
	d.Db.Close()

	rec := postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"b1"}}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("reorder with broken store = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "shortcut_buttons") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized designer.error.server copy, got: %s", rec.Body.String())
	}

	rec = postForm(mux, "/api/buttons/search", url.Values{"q": {"Appl"}}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("search with broken store = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized designer.error.server copy, got: %s", rec.Body.String())
	}
}

// ut-docs#1697: shortcut_buttons syncs shop-wide as an admin table
// (adminTables, sync_admin_repo.go) via a one-way primary-wins pull, so a
// write accepted on a satellite would silently vanish -- a reorder
// reverted, an added/removed button undone -- on the very next admin pull.
// All three mutating routes must refuse on a replica with a clear,
// localized 409, same pattern as catalog/handlers.go's
// TestCatalogItemMutations_RefusedOnReplica (same defect class as
// ut-docs#1689/#1667/#1590/#1546).
func TestButtonsAPI_MutationsRefusedOnReplica(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('ABC','Existing','itm1',0),('ZZZ','Other','itm1',1)`); err != nil {
		t.Fatalf("seed buttons: %v", err)
	}
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	const wantMsg = "manage quick-sale buttons on the primary till"
	assertRefused := func(t *testing.T, label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict {
			t.Errorf("%s on replica: want 409, got %d: %s", label, rec.Code, rec.Body.String())
			return
		}
		if !strings.Contains(rec.Body.String(), wantMsg) {
			t.Errorf("%s on replica: body missing the localized replica_use_primary message, got %q", label, rec.Body.String())
		}
	}

	// codes reversed relative to the seeded order: if the gate didn't
	// refuse this, ABC's sort_order would move to 1 and ZZZ's to 0.
	assertRefused(t, "reorder", postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"ZZZ,ABC"}}, nil))
	var abcOrder, zzzOrder int
	if err := d.Db.QueryRow(`SELECT sort_order FROM shortcut_buttons WHERE barcode='ABC'`).Scan(&abcOrder); err != nil || abcOrder != 0 {
		t.Errorf("ABC sort_order must not change on a replica: sort_order=%d err=%v", abcOrder, err)
	}
	if err := d.Db.QueryRow(`SELECT sort_order FROM shortcut_buttons WHERE barcode='ZZZ'`).Scan(&zzzOrder); err != nil || zzzOrder != 1 {
		t.Errorf("ZZZ sort_order must not change on a replica: sort_order=%d err=%v", zzzOrder, err)
	}

	assertRefused(t, "add", postForm(mux, "/api/buttons/add", url.Values{
		"label": {"New"}, "code": {"DEF"}, "itemId": {"itm1"},
	}, nil))
	var addCount int
	if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='DEF'`).Scan(&addCount); err != nil || addCount != 0 {
		t.Errorf("button must not be added on a replica: count=%d err=%v", addCount, err)
	}

	assertRefused(t, "remove", postForm(mux, "/api/buttons/remove", url.Values{"code": {"ABC"}}, nil))
	var removeCount int
	if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='ABC'`).Scan(&removeCount); err != nil || removeCount != 1 {
		t.Errorf("button must not be removed on a replica: count=%d err=%v", removeCount, err)
	}
}

// ut-docs#2210: "a newly-attached option set doesn't reach the sale screen
// until the quick button is deleted and recreated" — product owner report:
// attaching a modifier group ("Toppings") to an item that already has a
// sale-screen quick button never made the tile offer the picker, until the
// button itself was deleted and re-added.
//
// This proves the /ui/buttons render path itself is NOT the bug: the tile's
// hx-get="/ui/pos/modifiers" vs hx-post="/api/pos/scan" choice
// (web/ui/partials/buttons.html's "product-tile" template) is driven by
// ui.Button.HasModifiers, which internal/ui/buttons.go's Store.Load()
// computes fresh on every call via ModifierRepo.ItemIDsWithModifiers — a
// live join against item_modifier_group_links, never a value cached or
// copied onto the shortcut_buttons row at Add time. So attaching a group to
// an item that already has a button, with the button never touched, must
// already flip the SAME rendered tile from a straight scan-and-add to the
// customization picker on the very next render — no delete/recreate
// needed. This test fails loudly (tile still posts to /api/pos/scan) if a
// future change ever reintroduces a creation-time snapshot on the button
// itself.
func TestButtonsUIFragment_ReflectsModifierGroupAttachedAfterButtonExisted(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedPlainItem(t, d.Db, "itm-plain")

	// The quick button is created FIRST, while the item has no customization
	// at all -- exactly the reported order of events (button already on the
	// sale screen, then a modifier group gets attached to its item later).
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('BTN1','Apple Tile','itm-plain')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	assertTileScansDirectly(t, rec.Body.String())

	// Attach a modifier group ("Toppings") to the item -- the PO's "option
	// set" on an item's customization section -- WITHOUT touching the
	// button. This is the exact row shape ModifierRepo.CreateGroup itself
	// produces (a group row plus its first link row), so it's a reachable
	// state even though it bypasses the HTTP handler -- unlike the old
	// detach test this replaces, no UI-unreachable state is asserted
	// against here.
	if _, err := d.Db.Exec(`INSERT INTO item_modifier_groups (id, name,required,min_select,max_select,sort_order,is_active) VALUES ('g1','Toppings',0,0,3,0,1)`); err != nil {
		t.Fatalf("seed modifier group: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO item_modifier_group_links(item_id,group_id,sort_order) VALUES ('itm-plain','g1',0)`); err != nil {
		t.Fatalf("seed modifier group link: %v", err)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons (after attach) = %d (%s)", rec.Code, rec.Body.String())
	}
	after := rec.Body.String()
	if !strings.Contains(after, "Apple Tile") {
		t.Fatalf("the SAME button must still render (never deleted/recreated), got: %.800s", after)
	}
	assertTileOpensModifiers(t, after, "itm-plain", "BTN1")
}

// ut-docs#2210/#2209 interaction: an item that ALREADY has a variant (so
// HasVariants alone already routes its tile to the picker, per buttons.html
// :378's {{ if or .HasModifiers .HasVariants }}) must keep offering the
// picker throughout a modifier group being attached and then detached --
// the OR-gate must never let a modifier-group change flip a
// variant-carrying tile back to a straight scan. itm1 (seedForPages) has
// exactly this shape: a real, active variant ('var1') and no modifier
// groups of its own.
func TestButtonsUIFragment_HasVariantsAloneKeepsPickerAcrossModifierGroupChanges(t *testing.T) {
	mux, d := newButtonsMux(t)

	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('BTN1','Apple Tile','itm1')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileOpensModifiers(t, rec.Body.String(), "itm1", "BTN1")

	// Attach a modifier group too -- both gates now true.
	if _, err := d.Db.Exec(`INSERT INTO item_modifier_groups (id, name,required,min_select,max_select,sort_order,is_active) VALUES ('g1','Toppings',0,0,3,0,1)`); err != nil {
		t.Fatalf("seed modifier group: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO item_modifier_group_links(item_id,group_id,sort_order) VALUES ('itm1','g1',0)`); err != nil {
		t.Fatalf("seed modifier group link: %v", err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileOpensModifiers(t, rec.Body.String(), "itm1", "BTN1")

	// Deactivate the modifier group -- HasModifiers now false, but
	// HasVariants is still true, so the tile must NOT fall back to a plain
	// scan (that would silently drop the variant-selection prompt
	// ut-docs#2209 exists for, shop-wide, on every affected item).
	if _, err := d.Db.Exec(`UPDATE item_modifier_groups SET is_active = 0 WHERE id = 'g1'`); err != nil {
		t.Fatalf("deactivate modifier group: %v", err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileOpensModifiers(t, rec.Body.String(), "itm1", "BTN1")
}

// ut-docs#2210, driven through the REAL merchant flow end to end: the item
// detail panel's "attach an existing option set" picker (ADR-0090 §5,
// ut-docs#2046) posts to /api/catalog/modifier-group/attach, linking an
// EXISTING group to an item -- distinct from creating a brand new one. The
// group is created first (via ModifierRepo, on a throwaway item, exactly
// what CreateGroup itself does — a group + its first link) so that
// attaching it to itm1 is a genuine "existing, unlinked, active group"
// state ListAttachableModifierGroups would actually offer.
//
// This is the true end-to-end regression for the reported bug: before the
// ut-docs#2210 fix (buttons.html's swapped-in root carrying no refresh
// trigger, and this handler never announcing the change), an already-open
// sale screen's Apple Tile would keep scanning straight to the basket
// forever after this attach, in a REAL browser, even though this test's
// own recorder-based /ui/buttons re-fetch (which no real sale screen ever
// issues on its own) would still show it correctly -- see
// TestButtonsPartial_RootCarriesRefreshTrigger and
// TestModifierGroupAttach_FiresModifiersChangedTrigger below for the two
// halves of the actual fix this test can't observe through a bare re-fetch.
func TestButtonsUIFragment_ReflectsModifierGroupAttachedViaRealAttachHandler(t *testing.T) {
	mux, d := newButtonsAndCatalogMux(t)
	seedPlainItem(t, d.Db, "itm-plain")

	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('BTN1','Apple Tile','itm-plain')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itm-decoy','DECOY','Decoy',100,1)`); err != nil {
		t.Fatalf("seed decoy item: %v", err)
	}
	modRepo := data.NewModifierRepo(d.Db)
	groupID, err := modRepo.CreateGroup(t.Context(), "g-toppings", "Toppings", false, 0, 3, 0)
	if err != nil {
		t.Fatalf("create existing group: %v", err)
	}
	if err := modRepo.LinkGroupToItem(t.Context(), "itm-decoy", groupID, 0); err != nil {
		t.Fatalf("link existing group to decoy: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileScansDirectly(t, rec.Body.String())

	attachForm := url.Values{"itemId": {"itm-plain"}, "groupId": {groupID}}
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(attachForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach via real handler: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	after := rec.Body.String()
	if !strings.Contains(after, "Apple Tile") {
		t.Fatalf("the SAME button must still render, got: %.800s", after)
	}
	assertTileOpensModifiers(t, after, "itm-plain", "BTN1")
}

// The flip side, and the "editing" case, of ut-docs#2210's acceptance
// criteria ("attaching (or detaching, or editing)"): a modifier group is
// never hard-deleted from the UI (soft-deactivate convention, handlers.go's
// own comment on /api/catalog/modifier-group) -- a merchant "removes" its
// effect by unchecking the group's Active checkbox, which round-trips
// through the SAME create-or-update handler as an isActive=0 update. This
// drives that real toggle both ways and checks the tile after each flip.
func TestButtonsUIFragment_ReflectsModifierGroupActiveToggleViaRealHandler(t *testing.T) {
	mux, d := newButtonsAndCatalogMux(t)
	seedPlainItem(t, d.Db, "itm-plain")

	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('BTN1','Apple Tile','itm-plain')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}
	modRepo := data.NewModifierRepo(d.Db)
	groupID, err := modRepo.CreateGroup(t.Context(), "g-toppings", "Toppings", false, 0, 3, 0)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := modRepo.LinkGroupToItem(t.Context(), "itm-plain", groupID, 0); err != nil {
		t.Fatalf("link group: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileOpensModifiers(t, rec.Body.String(), "itm-plain", "BTN1")

	toggle := func(active string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{
			"id": {groupID}, "itemId": {"itm-plain"}, "name": {"Toppings"},
			"minSelect": {"0"}, "maxSelect": {"3"}, "isActive": {active},
		}
		req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("toggle isActive=%s: want 200, got %d: %s", active, rec.Code, rec.Body.String())
		}
		return rec
	}

	// Uncheck Active ("edit"/"detach"-equivalent): the tile must stop
	// offering the picker on the very next render, button untouched.
	toggle("0")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileScansDirectly(t, rec.Body.String())

	// Re-check Active ("edit" back): the tile must offer the picker again.
	toggle("1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	assertTileOpensModifiers(t, rec.Body.String(), "itm-plain", "BTN1")
}

// TestButtonsPartial_RootCarriesRefreshTrigger is the client-side half of
// the ut-docs#2210 fix: index.html's placeholder div fetches /ui/buttons
// exactly once (hx-trigger="load") and outerHTML-swaps it away, so nothing
// on an already-open sale screen can ever re-fetch it again UNLESS the
// swapped-in root re-declares its own trigger -- same self-refreshing shape
// as hold_api.go's "#held-sales" strip (hx-trigger="held-changed
// from:body", re-emitted on every one of its own renders). Before this fix,
// buttons.html's root carried no hx-get/hx-trigger at all: a real browser
// sitting on the sale screen would never see any catalog change again, no
// matter how live the server-side data was -- exactly the reported "must
// delete and recreate the button" symptom, which really just forced a full
// page reload. Revert the hx-get/hx-trigger on buttons.html's root div to
// reproduce the original bug against this test (red).
func TestButtonsPartial_RootCarriesRefreshTrigger(t *testing.T) {
	mux, _ := newButtonsMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/ui/buttons"`) {
		t.Fatalf(`swapped-in root must re-declare hx-get="/ui/buttons" so it can refetch itself, got: %.500s`, body)
	}
	// ut-docs#2285: the root also listens for buttons-changed now (emitted
	// by /api/buttons/reorder|remove|add) so a tile reordered/removed/added
	// via the sell screen's own jiggle edit mode (ut-docs#2339; or the
	// Designer, in another tab) refreshes the very same way a
	// modifier-group change already does — same self-refreshing root, one
	// more event name in the same hx-trigger attribute.
	if !strings.Contains(body, `hx-trigger="modifiers-changed from:body, buttons-changed from:body"`) {
		t.Fatalf(`swapped-in root must listen for hx-trigger="modifiers-changed from:body, buttons-changed from:body", got: %.500s`, body)
	}
	if !strings.Contains(body, `hx-swap="outerHTML"`) {
		t.Fatalf(`swapped-in root must keep hx-swap="outerHTML" so it can keep replacing itself, got: %.500s`, body)
	}
}

// TestModifierGroupAttach_FiresModifiersChangedTrigger is the server-side
// half of the ut-docs#2210 fix: every successful modifier-group mutation
// (attach here; create/update/detach are pinned by the sibling tests below)
// must set HX-Trigger: modifiers-changed so any open sale screen's
// buttons.html root (see TestButtonsPartial_RootCarriesRefreshTrigger)
// actually refetches. Revert the HX-Trigger write in
// renderModifierMutationResult (catalog/handlers.go) to reproduce the
// original bug against this test (red).
func TestModifierGroupAttach_FiresModifiersChangedTrigger(t *testing.T) {
	mux, d := newButtonsAndCatalogMux(t)
	modRepo := data.NewModifierRepo(d.Db)
	// Shop-wide, unassigned (ADR-0101) — attachable to itm1 as-is.
	groupID, err := modRepo.CreateGroup(t.Context(), "g-toppings", "Toppings", false, 0, 3, 0)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	attachForm := url.Values{"itemId": {"itm1"}, "groupId": {groupID}}
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(attachForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "modifiers-changed" {
		t.Fatalf("attach response HX-Trigger = %q, want %q", got, "modifiers-changed")
	}
}

// A refused mutation changed nothing and must not tell any open sale screen
// to refetch — a stale attach picker's 409 (attaching an INACTIVE group) is
// the refusal path that answers through renderModifierMutationResult with
// a non-OK status. (The detach-guard's last-link refusal used to be the
// example here; ADR-0101 removed that guard — detaching a last link now
// succeeds and fires the trigger like every other successful mutation.)
func TestModifierGroupAttach_StalePickerRefusalDoesNotFireTrigger(t *testing.T) {
	mux, d := newButtonsAndCatalogMux(t)
	modRepo := data.NewModifierRepo(d.Db)
	groupID, err := modRepo.CreateGroup(t.Context(), "g-toppings", "Toppings", false, 0, 3, 0)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := modRepo.UpdateGroup(t.Context(), groupID, "Toppings", false, 0, 3, 0, false); err != nil {
		t.Fatalf("deactivate group: %v", err)
	}

	attachForm := url.Values{"itemId": {"itm1"}, "groupId": {groupID}}
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(attachForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("attaching an inactive group: want 409 (refused), got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("a refused attach must not fire a refresh trigger, got HX-Trigger %q", got)
	}
}

// Detaching a group's LAST item link is allowed since ADR-0101 (the group
// becomes unassigned, not deleted) and, being a real change to what the
// tile gate resolves, fires the refresh trigger.
func TestModifierGroupDetach_LastLinkSucceedsAndFiresTrigger(t *testing.T) {
	mux, d := newButtonsAndCatalogMux(t)
	modRepo := data.NewModifierRepo(d.Db)
	groupID, err := modRepo.CreateGroup(t.Context(), "g-toppings", "Toppings", false, 0, 3, 0)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := modRepo.LinkGroupToItem(t.Context(), "itm1", groupID, 0); err != nil {
		t.Fatalf("link group: %v", err)
	}

	detachForm := url.Values{"itemId": {"itm1"}, "groupId": {groupID}}
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/detach", strings.NewReader(detachForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detaching a group's last link: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "modifiers-changed" {
		t.Fatalf("detach response HX-Trigger = %q, want %q", got, "modifiers-changed")
	}
	var groups, links int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = ?`, groupID).Scan(&groups); err != nil || groups != 1 {
		t.Fatalf("the group row must survive losing its last link: n=%d err=%v", groups, err)
	}
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = ?`, groupID).Scan(&links); err != nil || links != 0 {
		t.Fatalf("the link must be gone: n=%d err=%v", links, err)
	}
}

// TestButtonsPartial_JiggleModeMarkup (ut-docs#2339): the sell-screen grid
// carries everything app.js's utTileJiggle needs, server-rendered, so
// entering the iOS-style jiggle edit mode is a pure class toggle with zero
// network calls until Done: every tile has its global sort index
// (data-pos — see ui.TestBuildCategoryGroups_PosIsGlobalSortIndex for why
// the DOM order alone isn't enough), sits inside a .tile-cell wrapper that
// also holds its two corner badges (edit = plain link into the catalog,
// remove = the same hx-post/hx-confirm the ut-docs#2285 sheet's Remove
// used), and the grid has a Done control. Revert buttons.html's
// product-tile/jiggle-bar markup to reproduce (red).
func TestButtonsPartial_JiggleModeMarkup(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('J1','First','itm1',0),('J2','Second','itm1',1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-code="J1" data-item-id="itm1" data-pos="0"`,
		`data-code="J2" data-item-id="itm1" data-pos="1"`,
		`class="tile-cell"`,
		`class="tile-badge tile-badge-edit"`,
		`href="/catalog?item=itm1&return=/"`,
		`class="tile-badge tile-badge-remove"`,
		`hx-post="/api/buttons/remove"`,
		`hx-confirm="Remove “First” from the quick buttons? The item stays in the catalog."`,
		`data-testid="jiggle-done"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/ui/buttons missing %q: %.1500s", want, body)
		}
	}
	// The edit badge is a real link, the remove badge a real <button>: both
	// are keyboard-reachable siblings of the tile inside .tile-cell, never
	// nested INSIDE the tile's own <button>. A nested interactive element is
	// invalid HTML the parser actively RESTRUCTURES (it closes the outer
	// button at the nested start tag), so the nesting wouldn't show up as a
	// broken render — it would silently detach the badge from its tile.
	//
	// Asserted against a real parse, deliberately. This check first shipped
	// as a pair of strings.Contains calls, and independent review (2026-09-17)
	// found it could never fire: it looked for `<button class="btn-tile`,
	// while product-tile renders that class on the NEXT line, so the guard
	// short-circuited to false and nesting the badges inside the tile button
	// still passed green. golang.org/x/net/html runs the same WHATWG
	// tree-construction algorithm a browser's parser does — the same reason
	// elevation_test.go reaches for it rather than string matching.
	doc, err := xhtml.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse /ui/buttons: %v", err)
	}
	tiles := nodesWithClass(doc, "btn-tile")
	if len(tiles) != 2 {
		t.Fatalf("want 2 .btn-tile nodes in the parsed tree, got %d", len(tiles))
	}
	for _, tile := range tiles {
		// findAllTag counts the node it's given, and the tile IS a <button> —
		// so walk its CHILDREN and count what's strictly inside it.
		for c := tile.FirstChild; c != nil; c = c.NextSibling {
			for _, tag := range []string{"a", "button"} {
				if nested := findAllTag(c, tag); len(nested) > 0 {
					t.Fatalf("a .btn-tile must contain no nested <%s> (invalid HTML the parser restructures), got %d: %.600s", tag, len(nested), body)
				}
			}
		}
	}
	badges := append(nodesWithClass(doc, "tile-badge-edit"), nodesWithClass(doc, "tile-badge-remove")...)
	if len(badges) != 4 {
		t.Fatalf("want 4 badges (2 tiles x edit+remove), got %d", len(badges))
	}
	for _, b := range badges {
		if isDescendantOfClass(b, "btn-tile") {
			t.Fatalf("badge %q is nested inside the tile's own button; it must be a sibling inside .tile-cell", b.Data)
		}
		if b.Parent == nil || !hasClass(b.Parent, "tile-badges") ||
			b.Parent.Parent == nil || !hasClass(b.Parent.Parent, "tile-cell") {
			t.Fatalf("badge %q must sit in .tile-cell > .tile-badges", b.Data)
		}
	}
	// The ut-docs#2285 sheet is gone: nothing on the sale screen should
	// still reference its route.
	if strings.Contains(body, "/ui/pos/tile-sheet") || strings.Contains(body, "/api/buttons/move") {
		t.Fatalf("buttons fragment still references the retired tile sheet: %.800s", body)
	}
}

// hasClass reports whether n carries the given class token (ut-docs#2339).
// Token-wise, not substring: "tile-badge" must not match "tile-badges".
func hasClass(n *xhtml.Node, class string) bool {
	if n == nil || n.Type != xhtml.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, f := range strings.Fields(a.Val) {
			if f == class {
				return true
			}
		}
	}
	return false
}

// nodesWithClass returns every element at or below n carrying the class.
func nodesWithClass(n *xhtml.Node, class string) []*xhtml.Node {
	var out []*xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if hasClass(n, class) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// isDescendantOfClass reports whether n has an ancestor carrying the class
// (n itself doesn't count) — the sibling-not-child claim the badges make.
func isDescendantOfClass(n *xhtml.Node, class string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if hasClass(p, class) {
			return true
		}
	}
	return false
}
