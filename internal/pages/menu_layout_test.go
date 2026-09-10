package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/uislot"
)

// ADR-0088 (ut-docs#1904): the Menu launcher is a declared slot. Core's own
// tiles resolve through exactly the path a `layout` plugin's amendments do.

var menuTileHrefRe = regexp.MustCompile(`<a class="menu-tile" href="([^"]+)"`)

func menuTileHrefs(body string) []string {
	var out []string
	for _, m := range menuTileHrefRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func getMenu(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /menu = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// goldenManagerTiles is the exact tile list and ORDER a zero-plugin till
// renders for a manager (UT_AUTH=off), outside DE/TR. Pinned verbatim, not
// derived from CoreMenu, so this refactor and any future one is proven not
// to move a single tile (ADR-0088 Decision I's behavioural guarantee).
//
// ut-docs#2008: /country-settings, /translations, /locations and /registers
// (and, when visible, the two DE/TR fiscal tiles) no longer render as tiles
// on this flat grid at all — #1959's "Administration" GROUP (a heading
// plus six tiles) is replaced by a single gated "/admin" TILE in the same
// slot (Order 2550, right after /report-issue), which opens a dedicated
// tree page listing those same six destinations instead.
var goldenManagerTiles = []string{
	"/designer", "/shifts", "/journal", "/orders", "/reports", "/settings", "/plugins", "/items",
	"/help",
	"/users", "/kitchen-stations", "/bluetooth-devices", "/tables", "/report-issue", "/admin",
}

func TestMenuPage_GoldenZeroPluginTileOrder(t *testing.T) {
	// baseMenu is production's boot-time list (init.go), not a fixture.
	mux, _ := newMenuPageTestDeps(t, baseMenu)

	// No session, UT_AUTH unset: a cashier sees the nav tiles and Help only
	// — no /admin either, since visibleAdminEntries is empty for a cashier
	// (every one of the six it gates behind is itself settings/fiscal/
	// stock_location_management-gated).
	cashier := menuTileHrefs(getMenu(t, mux))
	wantCashier := goldenManagerTiles[:9]
	if strings.Join(cashier, " ") != strings.Join(wantCashier, " ") {
		t.Fatalf("zero-plugin cashier tiles drifted:\n got %v\nwant %v", cashier, wantCashier)
	}
	if strings.Contains(getMenu(t, mux), "menu-group") {
		t.Fatalf("a cashier (no settings access) must not see any group heading")
	}

	t.Setenv("UT_AUTH", "off")
	body := getMenu(t, mux)
	manager := menuTileHrefs(body)
	if strings.Join(manager, " ") != strings.Join(goldenManagerTiles, " ") {
		t.Fatalf("zero-plugin manager tiles drifted:\n got %v\nwant %v", manager, goldenManagerTiles)
	}
	// ut-docs#2008: the six Group: "menu.group.administration" entries
	// never reach .Tiles any more, so #1959's own group heading (drawn only
	// when a tile's Group changes) can never render on /menu either —
	// there is nothing left, at this slot or any other, to draw one for.
	if strings.Contains(body, "menu-group") {
		t.Fatalf("expected no group heading anywhere on /menu (ut-docs#2008 replaced it with the /admin tile), got: %s", body)
	}
	if !strings.Contains(body, `href="/admin"`) {
		t.Fatalf("expected the /admin tile for a manager, got: %s", body)
	}
	for _, hidden := range []string{"/country-settings", "/translations", "/locations", "/registers"} {
		if strings.Contains(body, `href="`+hidden+`"`) {
			t.Fatalf("expected no direct %s tile on /menu any more (ut-docs#2008: it now lives inside /admin), got: %s", hidden, body)
		}
	}
}

// Dogfooding is a hard AC: every core tile must be amendable, which is
// only true if every one of them resolves through uislot.Resolve.
func TestMenuPage_EveryCoreTileResolvesThroughTheSlot(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	t.Setenv("UT_AUTH", "off")
	for _, key := range goldenManagerTiles {
		t.Run(key, func(t *testing.T) {
			dp.MenuAmendments = []uislot.Amendment{{PluginID: "com.example.layout", Key: key, Hide: true}}
			got := menuTileHrefs(getMenu(t, mux))
			for _, h := range got {
				if h == key {
					t.Fatalf("hiding %s did not remove its tile: %v", key, got)
				}
			}
			if len(got) != len(goldenManagerTiles)-1 {
				t.Fatalf("hiding %s removed more than that one tile: %v", key, got)
			}
		})
	}
}

// Decision D: a hide removes the TILE only; the route stays registered and
// reachable — the fiscalRegisterPluginActive precedent, generalised.
func TestMenuPage_HiddenTileRouteStillServes(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerTables(mux, dp)
	t.Setenv("UT_AUTH", "off")
	dp.MenuAmendments = []uislot.Amendment{{PluginID: "com.example.salon", Key: "/tables", Hide: true}}

	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("expected the /tables tile hidden, got: %s", body)
	}
	req := httptest.NewRequest(http.MethodGet, "/tables", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("hiding a tile must never unregister its route: GET /tables = 404")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tables = %d, want 200 (still a working page): %s", rec.Code, rec.Body.String())
	}
}

// Decision G: label_key resolves through T; an unresolvable key falls back
// to the CORE label, never to the raw key string.
func TestMenuPage_LabelKeyResolvesOrFallsBackToCoreLabel(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)

	dp.MenuAmendments = []uislot.Amendment{{PluginID: "p", Key: "/items", LabelKey: "salon.nonexistent.key"}}
	body := getMenu(t, mux)
	if strings.Contains(body, "salon.nonexistent.key") {
		t.Fatalf("raw key leaked to the merchant: %s", body)
	}
	if !strings.Contains(body, `href="/items"`) || !strings.Contains(body, `class="menu-label">`+httpx.T("en", "nav.items")+`<`) {
		t.Fatalf("expected the core label %q on the /items tile, got: %s", httpx.T("en", "nav.items"), body)
	}

	// A key that DOES resolve replaces the label.
	dp.MenuAmendments = []uislot.Amendment{{PluginID: "p", Key: "/items", LabelKey: "nav.catalog"}}
	body = getMenu(t, mux)
	if !strings.Contains(body, `class="menu-label">`+httpx.T("en", "nav.catalog")+`<`) {
		t.Fatalf("expected the re-label to render, got: %s", body)
	}
	if strings.Contains(body, `class="menu-label">`+httpx.T("en", "nav.items")+`<`) {
		t.Fatalf("core label must not also render once re-labelled, got: %s", body)
	}
}

// Decision H: icon is a name in core's set; an unknown name falls back to
// the core entry's own icon, then to genericFallbackIcon.
func TestMenuPage_IconNameResolvesOrFallsBack(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, append(append([]common.MenuItem{}, baseMenu...), common.MenuItem{Href: "/some-plugin-page", Label: "Custom Plugin"}))

	tileIcon := func(body, href string) string {
		start := strings.Index(body, `href="`+href+`"`)
		if start < 0 {
			t.Fatalf("no tile for %s in: %s", href, body)
		}
		tile := body[start:]
		if end := strings.Index(tile, "</a>"); end >= 0 {
			tile = tile[:end]
		}
		m := regexp.MustCompile(`data-icon="([^"]+)"`).FindStringSubmatch(tile)
		if m == nil {
			t.Fatalf("no icon on %s tile: %s", href, tile)
		}
		return m[1]
	}

	dp.MenuAmendments = []uislot.Amendment{
		{PluginID: "p", Key: "/items", Icon: "scissors"},
		{PluginID: "p", Key: "/orders", Icon: "no-such-icon"},
		{PluginID: "p", Key: "/some-plugin-page", Icon: "no-such-icon"},
	}
	body := getMenu(t, mux)
	if got := tileIcon(body, "/items"); got != "scissors" {
		t.Errorf("known icon name: got %q, want scissors", got)
	}
	if got := tileIcon(body, "/orders"); got != "bell" {
		t.Errorf("unknown icon name on a core tile must fall back to the core icon: got %q, want bell", got)
	}
	if got := tileIcon(body, "/some-plugin-page"); got != genericFallbackIcon {
		t.Errorf("unknown icon name on a plugin tile with no core icon must fall back to the generic icon: got %q", got)
	}
}

func TestMenuPage_ReorderAndGroupHeading(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	fifty := 50
	dp.MenuAmendments = []uislot.Amendment{{PluginID: "p", Key: "/items", Order: &fifty, Group: "nav.catalog"}}
	body := getMenu(t, mux)
	got := menuTileHrefs(body)
	if len(got) == 0 || got[0] != "/items" {
		t.Fatalf("order 50 must put /items before /designer (100), got %v", got)
	}
	heading := `<h2 class="menu-group">` + httpx.T("en", "nav.catalog") + `</h2>`
	if strings.Count(body, heading) != 1 {
		t.Fatalf("expected exactly one group heading %q, got: %s", heading, body)
	}
	if strings.Index(body, heading) > strings.Index(body, `href="/items"`) {
		t.Fatalf("group heading must precede its first tile, got: %s", body)
	}
}

// The whole path, not an injected slice: a layout plugin persisted through
// PersistManifest, loaded by ReloadPlugins, applied by the renderer — and
// disabling the plugin puts the tile back.
func TestMenuPage_LayoutPluginHidesTileEndToEnd(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	t.Setenv("UT_AUTH", "off")
	ctx := t.Context()
	m := &plugins.Manifest{
		ID: "com.example.salon", Name: "Salon layout", Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Salon", Config: map[string]any{
			"slot": "menu", "amendments": []any{map[string]any{"key": "/tables", "hide": true}},
		}}},
	}
	if err := plugins.PersistManifest(ctx, dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(ctx); err != nil {
		t.Fatal(err)
	}
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("installed layout plugin must hide /tables, got: %s", body)
	}
	if err := data.NewPluginRepo(dp.Db).SetPluginActive(ctx, nil, m.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(ctx); err != nil {
		t.Fatal(err)
	}
	if body := getMenu(t, mux); !strings.Contains(body, `href="/tables"`) {
		t.Fatalf("a disabled layout plugin amends nothing; /tables must be back, got: %s", body)
	}
}

// Every VisibleIf name the core table declares must be a registered
// predicate, every core icon must resolve, and iconSVGFor must not shadow
// a declared core key (one source of truth per key).
func TestMenuPage_EveryCoreVisibleIfPredicateIsRegistered(t *testing.T) {
	for _, e := range uislot.CoreMenu {
		if e.VisibleIf != "" {
			if _, ok := menuPredicates[e.VisibleIf]; !ok {
				t.Errorf("core entry %s names unregistered predicate %q", e.Key, e.VisibleIf)
			}
		}
		if httpx.Icon(e.Icon) == "" {
			t.Errorf("core entry %s names unknown icon %q (known: %v)", e.Key, e.Icon, httpx.IconNames())
		}
		if _, dup := iconSVGFor[e.Key]; dup {
			t.Errorf("%s is declared in uislot.CoreMenu AND iconSVGFor — the table is the only source for a core key", e.Key)
		}
	}
	for name := range menuPredicates {
		used := false
		for _, e := range uislot.CoreMenu {
			if e.VisibleIf == name {
				used = true
			}
		}
		if !used {
			t.Errorf("predicate %q is registered but no core entry uses it", name)
		}
	}
}
