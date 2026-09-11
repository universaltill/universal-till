package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newMenuPageTestDeps(t *testing.T, menu []common.MenuItem) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	// Mirrors internal/pages/init.go's real wiring: without this, a test
	// that installs a plugin shipping its own locale overlay (a `layout` or
	// `language` plugin's re-label) can never observe it resolve — T would
	// silently keep returning the raw key forever, no matter what the
	// plugin's manifest/locale files declare. A no-op for every existing
	// test here, since none has plugin locale files on disk to sync.
	pm.SetLocalizer(i18n)
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     menu,
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	// Mirrors init.go's httpx.InitRailAmendments wiring (ut-docs#1912) so a
	// test that installs a `layout` plugin amending the rail slot sees
	// nav.html re-render with it; reset afterwards so the process-global
	// source never leaks one test's deps into another's.
	httpx.InitRailAmendments(dp.RailAmendmentsSnapshot)
	t.Cleanup(func() { httpx.InitRailAmendments(nil) })
	mux := http.NewServeMux()
	registerMenu(mux, dp)
	return mux, dp
}

func TestMenuPage_RendersConfiguredTilesWithMappedIcons(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{
		{Href: "/inventory", Label: "nav.inventory"},
		{Href: "/reports", Label: "nav.reports"},
	})
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/inventory"`) || !strings.Contains(body, `data-icon="package"`) {
		t.Fatalf("expected the inventory tile with its mapped icon, got: %s", body)
	}
	if !strings.Contains(body, `href="/reports"`) || !strings.Contains(body, `data-icon="chart-column"`) {
		t.Fatalf("expected the reports tile with its mapped icon, got: %s", body)
	}
	// Tile order follows d.Menu order -- it's the actual on-screen layout,
	// so a reordering regression should fail this.
	if i, r := strings.Index(body, `href="/inventory"`), strings.Index(body, `href="/reports"`); i > r {
		t.Fatalf("expected /inventory tile before /reports tile (d.Menu order), got: %s", body)
	}
	if !strings.Contains(body, "/menu?lang=") {
		t.Fatalf("expected the language-switcher links, got: %s", body)
	}
}

// ut-docs#1371: /orders had no iconFor entry, so its tile fell through to
// the "▪️" no-icon fallback -- a plain black square on-screen, live-reported
// on v0.8.2. Pinned the same way every other mapped route is (see
// TestMenuPage_RendersConfiguredTilesWithMappedIcons) so a future icon-map
// edit can't silently drop it again.
func TestMenuPage_OrdersTileHasAMappedIcon(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{
		{Href: "/orders", Label: "nav.orders"},
	})
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/orders"`) {
		t.Fatalf("expected the orders tile rendered, got: %s", body)
	}
	if strings.Contains(body, `▪️`) {
		t.Fatalf("expected the orders tile NOT to use the no-icon fallback, got: %s", body)
	}
	if !strings.Contains(body, `data-icon="bell"`) {
		t.Fatalf("expected the orders tile's mapped icon, got: %s", body)
	}
}

// ut-docs#1722: a plugin-contributed tile (core cannot enumerate plugin
// routes, so it can never appear in iconFor/iconSVGFor) used to fall through
// to the bare "▪️" no-icon square — the exact "plain black square" symptom
// ut-docs#1371 fixed for /orders, live in the shipped manual on the
// Help/FAQ tile. Now falls back to a deliberate generic drawn glyph
// instead.
func TestMenuPage_UnmappedRouteGetsFallbackIcon(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{
		{Href: "/some-plugin-page", Label: "Custom Plugin"},
	})
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/some-plugin-page"`) {
		t.Fatalf("expected the plugin tile rendered, got: %s", body)
	}
	if strings.Contains(body, "▪️") {
		t.Fatalf("expected the old no-icon square gone entirely, got: %s", body)
	}
	if !strings.Contains(body, `data-icon="puzzle"`) {
		t.Fatalf("expected the generic drawn fallback icon for an unmapped route, got: %s", body)
	}
}

func TestMenuPage_AlwaysAddsHelpTile(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `href="/help"`) {
		t.Fatalf("expected the /help tile even with an empty configured menu, got: %s", rec.Body.String())
	}
}

func TestMenuPage_ManagerOnlyTilesGatedByRole(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)

	// UT_AUTH is unset (not "off") and no session user is attached to the
	// request, so isManagerOrAuthOff must be false here.
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/users"`) {
		t.Fatalf("expected no /users tile for a non-manager request, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/report-issue"`) {
		t.Fatalf("expected no /report-issue tile for a non-manager request, got: %s", rec.Body.String())
	}

	t.Setenv("UT_AUTH", "off")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	body := rec2.Body.String()
	if !strings.Contains(body, `href="/users"`) {
		t.Fatalf("expected the manager-only /users tile with UT_AUTH=off, got: %s", body)
	}
	if !strings.Contains(body, `href="/report-issue"`) || !strings.Contains(body, `data-icon="bug"`) {
		t.Fatalf("expected the report-issue tile with its icon, reachable from the menu with UT_AUTH=off, got: %s", body)
	}
	// ut-docs#2008: /translations and /locations (Group:
	// "menu.group.administration" in uislot.CoreMenu) no longer get their
	// own tiles on /menu at all, manager or not — they now live inside
	// /admin, reached through the single gated "/admin" tile below.
	if strings.Contains(body, `href="/translations"`) {
		t.Fatalf("expected no direct /translations tile on /menu any more (ut-docs#2008: it now lives inside /admin), got: %s", body)
	}
	if strings.Contains(body, `href="/locations"`) {
		t.Fatalf("expected no direct /locations tile on /menu any more (ut-docs#2008: it now lives inside /admin), got: %s", body)
	}
	if !strings.Contains(body, `href="/admin"`) || !strings.Contains(body, `data-icon="lock"`) {
		t.Fatalf("expected the /admin tile with its icon, reachable from the menu with UT_AUTH=off, got: %s", body)
	}
	if strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Fatalf("expected no /admin tile for a non-manager request, got: %s", rec.Body.String())
	}
}

// ut-docs#2008: fiscal-register/fiscal-device (like the other four Group:
// "menu.group.administration" entries) no longer get their own tile on
// /menu in ANY state -- even DE + the German tax plugin active, which used
// to be exactly the state that unlocked this tile directly (ut-docs#1084).
// The full DE/TR + plugin-state visibility matrix that used to live here as
// four separate tests (TestMenuPage_FiscalRegisterTileRequiresPluginNot-
// JustCountry/...HiddenWhenPluginDisabled/...HiddenOutsideGermanyEvenWith-
// Plugin/...HiddenForTurkeyEvenWithGermanPluginActive) moved to
// admin_page_test.go, which is the surface that matrix actually governs
// now (GET /admin's Fiscal cluster) -- this is the regression check that
// the old surface stays empty.
func TestMenuPage_FiscalTilesNeverRenderDirectlyOnMenu(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedActiveTaxDePlugin(t, dp.Db)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected no direct /fiscal-register tile on /menu even with DE + plugin active (ut-docs#2008: it now lives inside /admin), got: %s", body)
	}
	// Guard against the test passing for the wrong reason: the /admin tile
	// itself must still be visible, since at least one admin destination
	// (fiscal-register, here) is.
	if !strings.Contains(body, `href="/admin"`) {
		t.Fatalf("expected the /admin tile once at least one admin destination is visible, got: %s", body)
	}
}

// ut-docs#1125: the menu page's language row is the third render site for
// httpx.AvailableLocales() (after the setup wizard's language step and
// settings' default-locale picker) and must show each locale's native name,
// never the bare two-letter code — a non-technical operator doesn't know "fa"
// is فارسی. Pinned by a test rather than left to the manual's screenshots:
// `make docs-shots` captures /menu above the fold, so this row (which sits
// below the tile grid) never appears in menu.png and a regression here would
// be invisible to both CI and the manual.
func TestMenuPageLanguageRowShowsNativeNamesNotBareCodes(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{{Href: "/inventory", Label: "nav.inventory"}})
	t.Setenv("UT_AUTH", "off")

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{"العربية", "English", "فارسی", "Türkçe"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /menu language row missing native language name %q", want)
		}
	}
	// Anchored on the href this row actually renders, so the codes' other
	// legitimate uses on the page (the ?lang= values themselves) cannot
	// false-positive.
	for _, code := range []string{"ar", "en", "fa", "tr"} {
		if strings.Contains(body, `/menu?lang=`+code+`">`+code+`</a>`) {
			t.Errorf("GET /menu still renders bare locale code %q as a button label", code)
		}
	}
}

// ut-docs#1720 (product owner, on the real tablet): the Bluetooth Devices
// tile rendered 📶 (ANTENNA BARS) — the mobile-reception glyph every phone
// puts in its status bar, which says "signal strength", not "Bluetooth".
// ut-docs#76 chose it knowingly because Unicode has no Bluetooth codepoint;
// the runic approximation (U+16D2) depends on font coverage Android WebView
// does not reliably have, so the standard mark can only be a drawn glyph.
// It comes from the same {{ icon }} set the nav rail already uses
// (ut-docs#1423) rather than a second icon mechanism.
func TestMenuPage_BluetoothTileUsesTheBluetoothSymbolNotSignalBars(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off") // the tile is manager-gated

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	start := strings.Index(body, `href="/bluetooth-devices"`)
	if start < 0 {
		t.Fatalf("expected the bluetooth-devices tile rendered, got: %s", body)
	}
	// Assertions about which glyph this tile got are scoped to this tile's
	// own markup, not page-wide: a page-wide "no puzzle icon anywhere" check
	// would be wrong on the real product, since a plugin-contributed route
	// with no mapped icon legitimately gets the generic fallback elsewhere
	// on this same page (ut-docs#1722, live on the Help/FAQ tile) — that's
	// a different tile's correct behaviour, not a regression on this one.
	tile := body[start:]
	if end := strings.Index(tile, "</a>"); end >= 0 {
		tile = tile[:end]
	}
	if !strings.Contains(tile, `data-icon="bluetooth"`) {
		t.Errorf("expected the drawn Bluetooth glyph on the tile, got: %s", tile)
	}
	// The fallback must not have been taken either: a missing IconSVG plus
	// the removed iconFor entry would silently render the generic fallback
	// icon (ut-docs#1722) instead of the specific Bluetooth glyph.
	if strings.Contains(tile, `data-icon="puzzle"`) {
		t.Errorf("expected no generic fallback icon on the bluetooth tile, got: %s", tile)
	}
	// This one IS page-wide on purpose: 📶 was this tile's only use anywhere
	// in the menu, so it should now be gone from the page entirely.
	if strings.Contains(body, "📶") {
		t.Errorf("the antenna-bars emoji must not appear anywhere on the menu any more, got: %s", body)
	}
}

// ut-docs#1845 (product owner + German pilot merchant, comparing against a
// competitor POS on the pilot tablet): the Menu screen's tiles (iconFor),
// the Pfand-refund tile, and the language-switcher label all carried
// emoji. TestMenuPage_BluetoothTileUsesTheBluetoothSymbolNotSignalBars
// already pins one glyph (📶) gone for good; this is the guard that every
// other one that ever appeared here stays gone too, covering the tile
// grid, the Pfand tile, and the language row in one render.
//
// Named for what it actually checks (independent review, ut-docs#1845):
// a fixed denylist of the specific glyphs this page used to render, NOT a
// full-page Unicode emoji scan — this page also renders shared layout
// chrome (web/ui/layouts/base.html) that carried its own, unrelated
// glyphs (📷 the bugreport screenshot button, ✦/⬆/✕ the update banner/
// close controls), out of scope for ut-docs#1845 ("no emoji left in
// menu/nav markup" — that chrome isn't menu/nav) and deliberately not
// asserted against here. Those were fixed separately by ut-docs#1859
// (drawn icons, same pattern as this page's own tiles) — this comment
// stays as history for why THIS test never covered them, not as a
// still-open list.
func TestMenuPage_NoRetiredTileEmoji(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{
		{Href: "/", Label: "nav.till"},
		{Href: "/designer", Label: "nav.designer"},
		{Href: "/inventory", Label: "nav.inventory"},
		{Href: "/shifts", Label: "nav.shifts"},
		{Href: "/journal", Label: "nav.journal"},
		{Href: "/reports", Label: "nav.reports"},
		{Href: "/settings", Label: "nav.settings"},
		{Href: "/plugins", Label: "nav.plugins"},
		{Href: "/catalog", Label: "nav.catalog"},
		{Href: "/orders", Label: "nav.orders"},
	})
	t.Setenv("UT_AUTH", "off") // also renders every manager-gated tile below

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Every glyph the old iconFor map (menu tiles) and menu.html's two
	// standalone spans (Pfand, language) ever used, plus the "no icon"
	// fallback square ut-docs#1371 replaced. Deliberately not "/fiscal-
	// register" or "/fiscal-device"'s 📋/🧾 here — those tiles need
	// country+plugin setup this render doesn't do, so they're covered by
	// their own dedicated tests (TestMenuPage_FiscalRegisterTile...)
	// instead; 🧾 is still checked below via the always-rendered "/" tile.
	oldEmoji := []string{
		"🧾", "🎨", "📦", "🕒", "📒", "📊", "⚙️", "🧩", "🏷️", "❓", "👤",
		"📍", "🧮", "🍳", "🪑", "🌍", "🌐", "🖥️", "🐞", "🛎️", "♻️", "▪️", "📶",
	}
	for _, e := range oldEmoji {
		if strings.Contains(body, e) {
			t.Errorf("expected no emoji left on the menu page (ut-docs#1845), found %q in: %s", e, body)
		}
	}
}

// Every drawn tile icon must name an icon that actually exists.
// icons_test.go's TestRailIconsReferencedByTemplatesExist scans templates for
// a literal {{ icon "name" }} and so cannot see these — the menu resolves its
// icon name in Go, from a map. This is that guard for this call path.
func TestMenuPage_EveryDrawnTileIconNameResolves(t *testing.T) {
	if len(iconSVGFor) == 0 {
		t.Fatal("iconSVGFor is empty — the residual non-core routes (/inventory, /catalog, …) should be in it; core keys live in uislot.CoreMenu since ADR-0088")
	}
	for href, name := range iconSVGFor {
		if httpx.Icon(name) == "" {
			t.Errorf("%s maps to unknown icon %q (known: %v)", href, name, httpx.IconNames())
		}
	}
	if httpx.Icon(genericFallbackIcon) == "" {
		t.Errorf("genericFallbackIcon %q is not a known icon (known: %v)", genericFallbackIcon, httpx.IconNames())
	}
}

// The two fiscal tiles are nested INSIDE the manager gate, and always have
// been: on main that nesting was structural (`if canPerform { … if DE {
// add } }`), so it could not be dropped by accident. ADR-0088 turned it
// into a composable expression — `v.visible("settings") && …` inside the
// predicate — which made the nesting a one-token deletion, at exactly the
// moment nothing covered it: every other fiscal-tile test above sets
// UT_AUTH=off, so all of them pass with the manager gate removed
// (independent review of ut-docs#1904, F3).
//
// These two pin the cashier case. A cashier on a fully-configured German
// or Turkish till must not see a tile whose page would only bounce them,
// and — more to the point — a §146a/YN ÖKC surface must not become
// visible to an unprivileged operator because a refactor lost a
// conjunct.
func TestMenuPage_FiscalRegisterTileStaysManagerGatedForCashier(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	// Deliberately NO t.Setenv("UT_AUTH", "off") — this is the cashier
	// path (no session user attached), which is the half every other
	// fiscal-register test skips.
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedActiveTaxDePlugin(t, dp.Db)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("a cashier must not see the fiscal-register tile even on a DE till with the German tax plugin active, got: %s", body)
	}
	// Guard against the test passing for the wrong reason: the page must
	// have rendered a real menu, just without this tile.
	if !strings.Contains(body, `href="/help"`) {
		t.Fatalf("expected a rendered menu with the help tile, got: %s", body)
	}
}

func TestMenuPage_FiscalDeviceTileStaysManagerGatedForCashier(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	// No UT_AUTH=off — the cashier path, as above.
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "TR" })
	seedActiveTaxTrPlugin(t, dp.Db, true)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("a cashier must not see the fiscal-device tile even on a TR till with the Turkish fiscal-device plugin active, got: %s", body)
	}
	if !strings.Contains(body, `href="/help"`) {
		t.Fatalf("expected a rendered menu with the help tile, got: %s", body)
	}
}

// Found in second-round review, ut-docs#2008 (see ADR-0088 Decision E's
// 2026-09-10 amendment): /admin is Protected and stays re-groupable by
// design (only hide/relabel/re-icon are refused on a protected key), so a
// `layout` plugin amendment regrouping /admin INTO
// "menu.group.administration" -- the very group it exists to replace on
// this flat grid -- installs cleanly. Without registerMenu's own
// Key!="/admin" exemption on its group-skip, that amendment would make
// /admin's own resolved entry satisfy the same skip that removes the six
// destinations it leads to, vanishing the ONLY menu path to
// /fiscal-register and /fiscal-device with no hide amendment involved at
// all -- reproducing the exact unreachability Decision E exists to refuse,
// through regroup instead of hide.
func TestMenuPage_AdminTileRendersRegardlessOfGroupAmendment(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	installLayoutAmendments(t, dp, map[string]any{"key": "/admin", "group": "menu.group.administration"})

	body := getMenu(t, mux)
	if !strings.Contains(body, `href="/admin"`) {
		t.Fatalf("expected /admin to still render on /menu even when regrouped into its own group, got: %s", body)
	}
}
