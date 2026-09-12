package pages

// /admin tree page (ut-docs#2008): the Menu launcher's single gated
// "Administration" tile opens this page, replacing ut-docs#1959's group
// heading on the flat /menu grid with a dedicated page grouping the same
// six destinations into three domain clusters. These tests cover the
// handler's own 403 gate (independent of whether the tile that leads here
// is itself hidden), the grouped-and-filtered render for partial and full
// access, and visibleAdminEntries/the "administration" predicate directly.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/uislot"
)

// installLayoutAmendments installs a throwaway `layout` plugin carrying the
// given Menu-slot amendments and reloads dp's plugin manager so
// d.MenuAmendmentsSnapshot() (and so uislot.Resolve, and so
// visibleAdminEntries/registerMenu) actually picks them up — same
// mechanism as menu_layout_settings_test.go's installSalonLayout, just
// generic over the amendments rather than one hardcoded pair.
func installLayoutAmendments(t *testing.T, dp *common.Deps, amendments ...map[string]any) {
	t.Helper()
	amendmentsAny := make([]any, len(amendments))
	for i, a := range amendments {
		amendmentsAny[i] = a
	}
	m := &plugins.Manifest{
		ID: "com.example.test-layout-2008", Name: "Test layout", Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Test", Config: map[string]any{
			"slot": "menu", "amendments": amendmentsAny,
		}}},
	}
	if err := plugins.PersistManifest(t.Context(), dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// newAdminPageTestDeps mirrors newMenuPageTestDeps (menu_page_test.go) but
// also sets AuthSvc (real role_permissions lookups via canPerform, not just
// the UT_AUTH=off escape hatch) and registers registerAdmin alongside
// registerMenu — several tests here check the two surfaces stay in step.
func newAdminPageTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerMenu(mux, dp)
	registerAdmin(mux, dp)
	return mux, dp
}

func getAdmin(t *testing.T, mux *http.ServeMux, u *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// adminGroupHeading and adminPageTitle wrap the tag around the translated
// text, not just the bare translated string: a bare
// strings.Contains(body, httpx.T("en", key)) is a tautology whenever a
// SIBLING element's own text happens to contain the same word (found in
// review, ut-docs#2008) -- "admin.group.fiscal"="Fiscal" also matches
// fiscalregister.title's row text "Fiscal register (Germany)", and
// "admin.group.locations"="Locations" is IDENTICAL to locations.title's own
// row text. Both would leave a deleted <h2> heading undetected. Requiring
// the exact tag wrapper only matches the actual heading element.
func adminGroupHeading(key string) string {
	return "<h2>" + httpx.T("en", key) + "</h2>"
}

func adminPageTitle() string {
	return "<h1>" + httpx.T("en", "menu.group.administration")
}

// A direct hit on /admin with no session at all must 403 -- this is the
// "reaching /admin directly by URL" case the AC calls out explicitly, not
// just "the tile is hidden".
func TestAdminPage_ForbiddenWithNoSession(t *testing.T) {
	mux, _ := newAdminPageTestDeps(t)
	rec := getAdmin(t, mux, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /admin with no session = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// ut-docs#1458's fix (country_settings_page_test.go's own precedent):
	// the 403 must still render the full layout, not a bare rail-less body.
	if !strings.Contains(rec.Body.String(), `class="nav"`) {
		t.Fatalf("403 on GET /admin has no nav rail:\n%s", rec.Body.String())
	}
}

// A cashier role (a real session, none of the six actions granted) must
// also 403 -- not just "no session at all".
func TestAdminPage_ForbiddenForCashierRole(t *testing.T) {
	mux, _ := newAdminPageTestDeps(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	rec := getAdmin(t, mux, &cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /admin = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// A cashier on a FULLY configured DE till with the German tax plugin
// active must still 403 on /admin directly -- the exact "a §146a surface
// must not become reachable to an unprivileged operator" shape
// menu_page_test.go's TestMenuPage_FiscalRegisterTileStaysManagerGatedFor-
// Cashier already pins at the tile-visibility layer; this pins it at the
// route itself, which is the layer that actually matters for a direct hit.
func TestAdminPage_ForbiddenForCashierEvenOnFullyConfiguredDETill(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedActiveTaxDePlugin(t, dp.Db)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	rec := getAdmin(t, mux, &cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /admin on a fully-configured DE till = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// Same UT_AUTH=off escape hatch every other admin page gets (canPerform's
// auth.Disabled branch) -- dev/CI tooling must reach /admin with no session.
func TestAdminPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newAdminPageTestDeps(t)
	rec := getAdmin(t, mux, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A viewer with a REAL partial grant (manager role, "settings" granted but
// "stock_location_management" revoked, no country configured) sees only
// the Localization cluster -- Locations is skipped (nothing visible in it),
// Fiscal is skipped (nothing visible in it either, no country set). Proves
// adminGroupsFor's "skip an empty cluster" rule and visibleAdminEntries'
// per-entry gating with a REAL role_permissions row, not the UT_AUTH=off
// escape hatch every other test here otherwise leans on.
func TestAdminPage_PartialPermissionsRendersOnlyTheVisibleCluster(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	if err := data.NewAuthRepo(dp.Db).SetRolePermission(t.Context(), nil, "manager", "stock_location_management", false); err != nil {
		t.Fatalf("revoke stock_location_management: %v", err)
	}
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := getAdmin(t, mux, &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET /admin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/translations"`) || !strings.Contains(body, `href="/country-settings"`) {
		t.Fatalf("expected the Localization cluster's two entries, got: %s", body)
	}
	if strings.Contains(body, `href="/locations"`) || strings.Contains(body, `href="/registers"`) {
		t.Fatalf("expected the Locations cluster entries hidden (stock_location_management revoked), got: %s", body)
	}
	if strings.Contains(body, `href="/fiscal-register"`) || strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected the Fiscal cluster hidden (no country configured), got: %s", body)
	}
	if !strings.Contains(body, adminGroupHeading("admin.group.localization")) {
		t.Errorf("expected the Localization heading rendered through T, got: %s", body)
	}
	if strings.Contains(body, adminGroupHeading("admin.group.locations")) {
		t.Errorf("expected no Locations heading when that cluster is empty, got: %s", body)
	}
	if strings.Contains(body, adminGroupHeading("admin.group.fiscal")) {
		t.Errorf("expected no Fiscal heading when that cluster is empty, got: %s", body)
	}
}

// ut-docs#1084's plugin-not-just-country requirement, now proven at the
// surface that actually renders this destination.
func TestAdminPage_FiscalClusterRequiresPluginNotJustCountry(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })

	body := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register entry for DE with no plugin installed, got: %s", body)
	}
	if strings.Contains(body, adminGroupHeading("admin.group.fiscal")) {
		t.Fatalf("expected no Fiscal heading for DE with no plugin installed, got: %s", body)
	}

	seedActiveTaxDePlugin(t, dp.Db)
	body = getAdmin(t, mux, nil).Body.String()
	if !strings.Contains(body, adminGroupHeading("admin.group.fiscal")) {
		t.Fatalf("expected the Fiscal heading once DE + plugin active, got: %s", body)
	}
	if !strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected the fiscal-register entry once DE + plugin active, got: %s", body)
	}
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected no fiscal-device entry on a DE-only till, got: %s", body)
	}
}

// The gate checks is_active, not merely row existence (ut-docs#531).
func TestAdminPage_FiscalClusterHiddenWhenPluginDisabled(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedDisabledTaxDePlugin(t, dp.Db)

	body := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register entry for DE with the plugin installed but disabled, got: %s", body)
	}
}

// A non-DE shop must never see the entry even with the plugin installed.
func TestAdminPage_FiscalClusterHiddenOutsideGermanyEvenWithPlugin(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxDePlugin(t, dp.Db)

	body := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register entry outside Germany even with the plugin active, got: %s", body)
	}
	// The other two clusters are still visible under UT_AUTH=off with no
	// country set -- confirms the page rendered for real, not a false pass
	// from an unrelated 403/empty page.
	if !strings.Contains(body, `href="/translations"`) {
		t.Fatalf("expected a real rendered page (Localization cluster present), got: %s", body)
	}
}

// ut-docs#1208 widened fiscal.RequiresHardGate to also cover Turkey -- this
// entry's gate must stay an explicit country=="DE" check, or a TR shop
// would wrongly see Germany's fiscal-register entry just because it has
// the (unrelated) German tax plugin active.
func TestAdminPage_FiscalClusterHiddenForTurkeyEvenWithGermanPluginActive(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxDePlugin(t, dp.Db)
	if err := settings.NewStore(dp.Db).Set(t.Context(), "store.country", "TR"); err != nil {
		t.Fatalf("set store.country: %v", err)
	}
	dp.State = common.LoadState(t.Context(), settings.NewStore(dp.Db), dp.Cfg)

	body := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register entry for TR even with the German plugin active, got: %s", body)
	}
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected no fiscal-device entry either (the Turkish plugin was never installed), got: %s", body)
	}
}

// The Turkish fiscal-device entry follows the German one's rule: country
// AND plugin installed+active, never country alone -- moved here from
// fiscal_device_page_test.go's own TestMenuPage_FiscalDeviceTileRequires-
// TurkeyAndPlugin (ut-docs#2008: this is the surface that governs it now).
func TestAdminPage_FiscalClusterRequiresTurkeyAndPlugin(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "TR" })

	body := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected no fiscal-device entry for TR with no plugin, got: %s", body)
	}

	seedActiveTaxTrPlugin(t, dp.Db, true)
	body = getAdmin(t, mux, nil).Body.String()
	if !strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected the fiscal-device entry once TR + plugin active, got: %s", body)
	}
	if strings.Contains(body, `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register entry on a TR-only till, got: %s", body)
	}

	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	body = getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatal("fiscal-device entry must be Turkey-only")
	}
}

// Full access (manager, DE + active German plugin, TR settings ALSO
// simulated is impossible on one shop -- so this proves the TR half
// separately from the DE half above) renders the Locations and
// Localization clusters plus Fiscal with exactly the one entry the
// configured country/plugin pair unlocks -- i.e. all three clusters that
// end up non-empty for this exact configuration, none skipped.
func TestAdminPage_AllNonEmptyClustersRenderForFullAccessDETill(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedActiveTaxDePlugin(t, dp.Db)

	body := getAdmin(t, mux, nil).Body.String()
	for _, want := range []string{"admin.group.fiscal", "admin.group.locations", "admin.group.localization"} {
		if !strings.Contains(body, adminGroupHeading(want)) {
			t.Errorf("expected the %s heading rendered, got: %s", want, body)
		}
	}
	for _, href := range []string{"/fiscal-register", "/locations", "/registers", "/translations", "/country-settings"} {
		if !strings.Contains(body, `href="`+href+`"`) {
			t.Errorf("expected an entry for %s, got: %s", href, body)
		}
	}
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Errorf("expected no fiscal-device entry on a DE-only till, got: %s", body)
	}
	// Back-to-menu affordance and page title, both through T -- not a
	// hardcoded literal. Tag-wrapped (adminPageTitle), not a bare
	// substring: admin_page.go also passes "title": "Administration" as a
	// hardcoded literal consumed by <title> in the base layout, so a bare
	// Contains(body, "Administration") passes even with the <h1> deleted.
	if !strings.Contains(body, adminPageTitle()) {
		t.Errorf("expected the page title rendered through T, got: %s", body)
	}
	if !strings.Contains(body, `href="/menu"`) {
		t.Errorf("expected a back-to-menu link to /menu, got: %s", body)
	}
}

// ut-docs#2008's explicit tile<->page parity requirement: the Menu
// launcher's own "administration" predicate and this handler's 403 gate
// must agree -- if one says visible the other must say reachable. Two
// separate tests, not one with t.Setenv("UT_AUTH", "off") toggled
// mid-test: UT_AUTH is read once by canPerform's os.Getenv call per
// request and t.Setenv cannot be "unset" partway through a single test, so
// the manager-parity and cashier-parity halves need their own deps/test
// functions to each get a clean environment.
func TestAdminPage_TileVisibilityMatchesPageReachabilityUnderAuthOff(t *testing.T) {
	mux, _ := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")

	menuBody := getMenu(t, mux)
	adminCode := getAdmin(t, mux, nil).Code
	if strings.Contains(menuBody, `href="/admin"`) != (adminCode == http.StatusOK) {
		t.Fatalf("tile-visible=%v but /admin status=%d — must agree", strings.Contains(menuBody, `href="/admin"`), adminCode)
	}
}

func TestAdminPage_TileVisibilityMatchesPageReachabilityForCashier(t *testing.T) {
	mux, _ := newAdminPageTestDeps(t)
	// Deliberately NO t.Setenv("UT_AUTH", "off") -- canPerform's
	// auth.Disabled branch would short-circuit every check to true
	// regardless of the session below, defeating the point of this test.
	cashier := auth.User{ID: "c1", Role: "cashier"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/menu", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	menuBody := rec.Body.String()
	adminCode := getAdmin(t, mux, &cashier).Code
	if strings.Contains(menuBody, `href="/admin"`) {
		t.Fatalf("expected the /admin tile hidden for a cashier, got: %s", menuBody)
	}
	if adminCode != http.StatusForbidden {
		t.Fatalf("expected /admin to 403 for the same cashier, got %d", adminCode)
	}
}

// ut-docs#2116: /admin itself is now the two-pane shell's empty landing
// state (no destination selected, PanelHTML nil) rather than its own
// distinct page -- the tree renders, no row is marked is-current (since
// /admin is never itself a member of the tree -- visibleAdminEntries
// excludes it), and the empty-state message key renders through T.
func TestAdminPage_BareGETIsShellEmptyState(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedActiveTaxDePlugin(t, dp.Db)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	body := getAdmin(t, mux, &mgr).Body.String()
	if !strings.Contains(body, `id="admin-tree"`) {
		t.Fatalf("expected the admin tree rendered, got: %s", body)
	}
	if !strings.Contains(body, `id="admin-panel"`) {
		t.Fatalf("expected the admin panel wrapper, got: %s", body)
	}
	if strings.Contains(body, "is-current") {
		t.Fatalf("expected no tree row marked selected on the bare /admin landing, got: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "admin.select_section")) {
		t.Fatalf("expected the empty-state message rendered through T, got: %s", body)
	}
}

// ut-docs#2116, found in review: every other test in this file (and the
// e2e spec's original draft) asserted only the URL/is-current/aria-current
// outcome of a tree click -- which this card's own bare-GET shell
// rendering makes IDENTICAL whether the click ran as a real htmx in-panel
// swap or as a plain full-page navigation to the same href. Deleting the
// tree row's hx-get/hx-target/hx-push-url attributes entirely (i.e.
// reverting to the old plain-<a>-only behaviour this card exists to fix)
// left every one of those assertions green. This test pins the actual
// mechanism -- the attributes themselves -- so that regression can't
// recur silently.
func TestAdminPage_TreeRowsCarryHtmxSwapAttributes(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedActiveTaxDePlugin(t, dp.Db)

	body := getAdmin(t, mux, nil).Body.String()
	for _, href := range []string{"/locations", "/registers", "/fiscal-register", "/translations", "/country-settings"} {
		row := findRowByHref(t, body, href)
		if !strings.Contains(row, `hx-get="`+href+`"`) {
			t.Errorf("expected %s's tree row to carry hx-get=%q for the in-panel swap, got row: %s", href, href, row)
		}
		if !strings.Contains(row, `hx-target="#admin-panel"`) {
			t.Errorf("expected %s's tree row to target #admin-panel, got row: %s", href, row)
		}
		if !strings.Contains(row, `hx-push-url="true"`) {
			t.Errorf("expected %s's tree row to push its own URL (so a swap stays deep-linkable/back-button-safe), got row: %s", href, row)
		}
	}
}

// findRowByHref extracts the single <a ...>...</a> element whose href
// matches, so the hx-* assertions above are scoped to that one row rather
// than matching an hx-get/hx-target that happens to appear ANYWHERE in the
// page body (e.g. a different row, or an unrelated htmx element).
func findRowByHref(t *testing.T, body, href string) string {
	t.Helper()
	marker := `href="` + href + `"`
	idx := strings.Index(body, marker)
	if idx == -1 {
		t.Fatalf("no row found for href=%s in body: %s", href, body)
	}
	start := strings.LastIndex(body[:idx], "<a ")
	end := strings.Index(body[idx:], "</a>")
	if start == -1 || end == -1 {
		t.Fatalf("could not isolate the <a> element for href=%s", href)
	}
	return body[start : idx+end+len("</a>")]
}

// Unit-level coverage for visibleAdminEntries and the "administration"
// predicate directly, independent of HTTP rendering.
func TestVisibleAdminEntries_NoPermissions(t *testing.T) {
	_, dp := newAdminPageTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil) // no session, UT_AUTH unset
	got := visibleAdminEntries(dp, req)
	if len(got) != 0 {
		t.Fatalf("expected zero visible admin entries for an unauthenticated request, got %v", got)
	}
	vis := &menuVisibility{d: dp, r: req}
	if vis.visible("administration") {
		t.Fatalf("expected the administration predicate false when nothing is visible")
	}
}

func TestVisibleAdminEntries_PartialPermissionsSubset(t *testing.T) {
	_, dp := newAdminPageTestDeps(t)
	if err := data.NewAuthRepo(dp.Db).SetRolePermission(t.Context(), nil, "manager", "stock_location_management", false); err != nil {
		t.Fatalf("revoke stock_location_management: %v", err)
	}
	mgr := auth.User{ID: "m1", Role: "manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/admin", nil), mgr)

	got := visibleAdminEntries(dp, req)
	gotKeys := make(map[string]bool, len(got))
	for _, e := range got {
		gotKeys[e.Key] = true
	}
	wantVisible := []string{"/translations", "/country-settings"}
	wantHidden := []string{"/locations", "/registers", "/fiscal-register", "/fiscal-device"}
	for _, k := range wantVisible {
		if !gotKeys[k] {
			t.Errorf("expected %s visible for a manager with settings granted, got %v", k, gotKeys)
		}
	}
	for _, k := range wantHidden {
		if gotKeys[k] {
			t.Errorf("expected %s hidden (stock_location_management revoked / no fiscal country configured), got %v", k, gotKeys)
		}
	}
	if len(got) != len(wantVisible) {
		t.Errorf("expected exactly %d visible entries, got %d: %v", len(wantVisible), len(got), got)
	}
	vis := &menuVisibility{d: dp, r: req}
	if !vis.visible("administration") {
		t.Fatalf("expected the administration predicate true when a subset is visible")
	}
}

// Static-coverage counterpart to adminGroupsFor's own catch-all: today's
// six core entries should each land in exactly one of the three NAMED
// clusters, not the "Other" fallback — this test would catch a typo'd key
// in adminGroupOrder (e.g. a copy-paste leaving "/locaions") that would
// otherwise silently dump an entry into "Other" instead of its intended
// cluster, with every other test in this file still green (they check
// href presence, not which cluster a row landed under).
func TestAdminGroupsFor_EveryCoreAdministrationEntryClaimedByExactlyOneNamedCluster(t *testing.T) {
	claimed := make(map[string]int)
	for _, g := range adminGroupOrder {
		for _, k := range g.keys {
			claimed[k]++
		}
	}
	for _, e := range uislot.CoreMenu {
		if e.Group != "menu.group.administration" {
			continue
		}
		if claimed[e.Key] != 1 {
			t.Errorf("CoreMenu entry %s carries Group %q but adminGroupOrder's named clusters claim it %d times (want exactly 1)", e.Key, e.Group, claimed[e.Key])
		}
	}
}

// ut-docs#2008, found in review: visibleAdminEntries used to read raw
// uislot.CoreMenu instead of the plugin-amended uislot.Resolve output
// registerMenu itself builds, so a `layout` plugin's hide amendment on one
// of the six became a no-op on /admin (it still listed the entry) even
// though it correctly disappeared from /menu.
func TestAdminPage_HideAmendmentRemovesEntryFromAdminTreeToo(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	installLayoutAmendments(t, dp, map[string]any{"key": "/locations", "hide": true})

	body := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(body, `href="/locations"`) {
		t.Fatalf("expected /locations hidden by the layout plugin to also be absent from /admin, got: %s", body)
	}
	if !strings.Contains(body, `href="/registers"`) {
		t.Fatalf("expected the sibling /registers entry still visible (only /locations was hidden), got: %s", body)
	}
}

// ut-docs#2008, found in review: regrouping some OTHER core entry INTO
// "menu.group.administration" (a supported, validated amendment — ADR-0088
// Decision F) used to vanish from BOTH surfaces — menu_page.go's /menu
// skip filter keys on the RESOLVED Group so it correctly left the flat
// grid, but the raw-CoreMenu read in visibleAdminEntries never saw the
// amended Group so it never entered the tree either. Also exercises
// adminGroupsFor's "Other" catch-all: /users is not one of the three named
// clusters' keys.
func TestAdminPage_RegroupIntoAdministrationMovesEntryFromMenuToAdminTree(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	installLayoutAmendments(t, dp, map[string]any{"key": "/users", "group": "menu.group.administration"})

	menuBody := getMenu(t, mux)
	if strings.Contains(menuBody, `href="/users"`) {
		t.Fatalf("expected /users regrouped into Administration to leave the flat /menu grid, got: %s", menuBody)
	}
	adminBody := getAdmin(t, mux, nil).Body.String()
	if !strings.Contains(adminBody, `href="/users"`) {
		t.Fatalf("expected /users regrouped into Administration to appear inside /admin (via the 'Other' catch-all), got: %s", adminBody)
	}
	if !strings.Contains(adminBody, adminGroupHeading("admin.group.other")) {
		t.Fatalf("expected the 'Other' cluster heading for an entry no named cluster claims, got: %s", adminBody)
	}
}

// ut-docs#2008, found in review: regrouping one of the six OUT of
// "menu.group.administration" used to duplicate it — back on the flat grid
// (resolved Group no longer matched registerMenu's skip) AND still inside
// /admin (the raw-CoreMenu read's original Group still matched there).
func TestAdminPage_RegroupOutOfAdministrationMovesEntryToMenuGridNotBoth(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	// ParseMenuAmendments refuses an empty-string Group ("field group must
	// be a non-empty string" -- there is no amendment-level "clear the
	// group" value), so "regroup OUT of Administration" means "regroup
	// into some OTHER real group", not "back to no group" -- any
	// non-empty, non-"menu.group.administration" value exercises the same
	// code path.
	installLayoutAmendments(t, dp, map[string]any{"key": "/translations", "group": "menu.group.custom-test-group"})

	menuBody := getMenu(t, mux)
	if !strings.Contains(menuBody, `href="/translations"`) {
		t.Fatalf("expected /translations regrouped OUT of Administration to appear on the flat /menu grid, got: %s", menuBody)
	}
	adminBody := getAdmin(t, mux, nil).Body.String()
	if strings.Contains(adminBody, `href="/translations"`) {
		t.Fatalf("expected /translations regrouped OUT of Administration to no longer also appear inside /admin, got: %s", adminBody)
	}
	// /country-settings (untouched) must still be there -- proves the page
	// rendered for real, not a false pass from an empty/403 page.
	if !strings.Contains(adminBody, `href="/country-settings"`) {
		t.Fatalf("expected the untouched Localization sibling still visible, got: %s", adminBody)
	}
}

// ut-docs#2008, found in review: /admin is uislot.ProtectedMenuKeys, and
// protected keys stay re-groupable by design (only hide/relabel/re-icon
// are refused for them) -- so a plugin amending {"key":"/admin",
// "group":"menu.group.administration"} is otherwise valid. Without the
// explicit Key=="/admin" guard in visibleAdminEntries, that amendment would
// make /admin a member of the very set the "administration" VisibleIf
// predicate computes from calling visibleAdminEntries again -- unbounded
// recursion, a stack-overflow crash on every /menu and /admin request, not
// a mere display glitch. This test would crash the test binary (not just
// fail cleanly) if that guard were ever removed.
func TestVisibleAdminEntries_ExcludesAdminItselfEvenIfRegroupedIntoItsOwnGroup(t *testing.T) {
	_, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	installLayoutAmendments(t, dp, map[string]any{"key": "/admin", "group": "menu.group.administration"})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	got := visibleAdminEntries(dp, req)
	for _, e := range got {
		if e.Key == "/admin" {
			t.Fatalf("visibleAdminEntries must never include /admin itself, even when regrouped into its own group; got %v", got)
		}
	}
	if len(got) == 0 {
		t.Fatalf("expected the normal admin entries still visible under UT_AUTH=off, got none")
	}
}
