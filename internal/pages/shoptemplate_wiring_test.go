package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/uislot"
)

// useRealShopTemplates loads the six shipped templates against this build's
// real catalog (uislot rows, settings registry, capability set, en.json)
// exactly as pages.Init does, and hands them to dp.
func useRealShopTemplates(t *testing.T, dp *common.Deps) {
	t.Helper()
	// No chdirRoot here: it resets process globals (rail source etc.) the
	// caller already wired. Locate web/locales from this file instead.
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	i18n, err := config.NewI18n(filepath.Join(root, "web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	set, err := loadShopTemplates(i18n)
	if err != nil {
		t.Fatalf("shipped templates must load against the real catalog: %v", err)
	}
	dp.ShopTemplates = set
}

var allShopTypes = []string{"retail", "cafe", "hospitality", "service", "market_stall", "other"}

// The six shipped templates load against this build's real catalog (the same
// validation pages.Init runs): a template naming a row that does not exist, a
// missing en.json key, a seed off the whitelist would fail the boot's load and
// leave every shop untemplated, so it is a test failure here instead.
func TestShippedTemplatesLoadWithRuntimeCatalog(t *testing.T) {
	var d common.Deps
	useRealShopTemplates(t, &d)
	for _, st := range allShopTypes {
		if d.ShopTemplates.ForType(st) == nil {
			t.Errorf("no template for shop type %q", st)
		}
	}
}

// No template, in any mode, hides a protected Menu or rail key.
func TestNoTemplateHidesProtectedKey(t *testing.T) {
	var d common.Deps
	useRealShopTemplates(t, &d)
	for _, st := range allShopTypes {
		r := d.ShopTemplates.ForType(st)
		for _, slot := range []string{uislot.MenuSlot, uislot.RailSlot, uislot.ItemsSlot, uislot.SettingsSlot} {
			for _, a := range append(append([]uislot.Amendment(nil), r.Amendments(slot)...), r.LegacyAmendments(slot)...) {
				if !a.Hide {
					continue
				}
				if uislot.IsProtectedMenuKey(a.Key) || uislot.IsProtectedRailKey(a.Key) || uislot.IsProtectedSettingsKey(a.Key) || uislot.FixedClassFor(slot, a.Key) {
					t.Errorf("%s template hides protected %s key %q", st, slot, a.Key)
				}
				if slot != uislot.MenuSlot {
					t.Errorf("%s template hides a %s row %q; only Menu tiles may be hidden", st, slot, a.Key)
				}
			}
		}
	}
}

// menuHrefs renders /menu as a manager and returns the tile hrefs.
func menuHrefs(t *testing.T, mux *http.ServeMux, user *auth.User) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /menu = %d", rec.Code)
	}
	var hrefs []string
	for _, m := range regexp.MustCompile(`class="menu-tile" href="([^"]+)"`).FindAllStringSubmatch(rec.Body.String(), -1) {
		hrefs = append(hrefs, m[1])
	}
	return hrefs
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// activeRetailMenuDeps is a till whose shop was set up as retail with the
// template applied (ledger present): Tables, Kitchen stations, Bluetooth
// devices and Open orders are tucked away.
func activeRetailMenuDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newMenuLayoutSettingsDeps(t)
	dp.BaseMenu = baseMenu
	useRealShopTemplates(t, dp)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, common.KeyShopType, "retail"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, shoptemplate.LedgerKey, fmt.Sprintf(`{"id":"retail","version":1,"seeded":{},"at":%q}`, time.Now().UTC().Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(ctx); err != nil {
		t.Fatal(err)
	}
	return mux, dp
}

// A template-hidden tile is one tap from coming back: it is listed on
// Settings > Hidden menu tiles (naming the shop type), restoring it puts the
// tile back, the route was never removed, and the restore survives a change
// of shop type.
func TestTemplateHiddenTileRestorable(t *testing.T) {
	mux, dp := activeRetailMenuDeps(t)

	if hrefs := menuHrefs(t, mux, nil); contains(hrefs, "/tables") {
		t.Fatalf("active retail Menu must tuck Tables away, got %v", hrefs)
	}
	page := getPage(t, mux, "/settings/menu").Body.String()
	for _, want := range []string{`value="/tables"`, "Shop type: Retail shop"} {
		if !strings.Contains(page, want) {
			t.Errorf("Hidden menu tiles must list the template-hidden Tables tile naming the shop type; missing %q", want)
		}
	}

	rec := postMenuLayoutForm(t, mux, "/api/settings/menu/restore", url.Values{"key": {"/tables"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore /tables = %d", rec.Code)
	}
	if hrefs := menuHrefs(t, mux, nil); !contains(hrefs, "/tables") {
		t.Fatalf("one restore must bring Tables back to the Menu, got %v", hrefs)
	}

	// Change type to cafe (also tucks Tables away): the restore still holds.
	if err := dp.Settings.Set(t.Context(), common.KeyShopType, "cafe"); err != nil {
		t.Fatal(err)
	}
	dp.ReloadTemplate(t.Context())
	if hrefs := menuHrefs(t, mux, nil); !contains(hrefs, "/tables") {
		t.Fatalf("a restored tile must survive a shop type change, got %v", hrefs)
	}
}

// The Menu shows one quiet line (with the way back) to a manager while the
// template tucks tiles away, and nothing for legacy shops, restored-all or
// cashiers.
func TestMenuTemplateTuckedLine(t *testing.T) {
	mux, dp := activeRetailMenuDeps(t)
	body := func() string { return getPage(t, mux, "/menu").Body.String() }
	if b := body(); !strings.Contains(b, `data-testid="menu-template-tucked"`) || !strings.Contains(b, `href="/settings/menu"`) {
		t.Fatal("an active template that hides tiles must show the tucked-away line with its link")
	}

	// Legacy mode (ledger gone): the menu is as before templates, no line.
	if err := dp.Settings.Set(t.Context(), shoptemplate.LedgerKey, ""); err != nil {
		t.Fatal(err)
	}
	dp.ReloadTemplate(t.Context())
	if strings.Contains(body(), "menu-template-tucked") {
		t.Error("legacy mode must not show the tucked-away line")
	}
}

// The wizard's type save is the one path that applies a template: the real
// shipped content seeds retail's order-type default, writes the ledger (the
// shop is now ACTIVE, not legacy) and rebuilds the layout, so the menu and
// Settings follow at once. The seams default to the real set.
func TestWizardShopTypeSaveActivatesRealTemplate(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	ctx := t.Context()

	if r := settingsTemplateResolved(ctx, d); r != nil {
		t.Fatal("before a type is set there is no template in effect")
	}
	if err := saveSetupShopType(ctx, d, "retail"); err != nil {
		t.Fatal(err)
	}
	if st := d.TemplateState(ctx); st.Mode != common.TemplateActive || st.Resolved.ID() != "retail" {
		t.Fatalf("after the wizard save the retail template is active, got mode %v", st.Mode)
	}
	if r := settingsTemplateResolved(ctx, d); r == nil || r.ID() != "retail" {
		t.Errorf("the Settings seam must return the active retail template, got %v", r)
	}
	if v, _, _ := d.Settings.Get(ctx, "sale.order_type_prompt"); v != "off" {
		t.Errorf("retail seeds sale.order_type_prompt=off, got %q", v)
	}
	if !hidesTile(d.MenuAmendmentsSnapshot(), "/tables") {
		t.Errorf("the retail layout hides Tables straight after the save, got %+v", d.MenuAmendmentsSnapshot())
	}
}

// A shop that predates templates is left alone: type set, no ledger = legacy,
// and the Settings seam reports no template (nothing is demoted).
func TestLegacyShopHasNoSettingsTemplate(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	if err := d.Settings.Set(t.Context(), common.KeyShopType, "retail"); err != nil {
		t.Fatal(err)
	}
	if r := settingsTemplateResolved(t.Context(), d); r != nil {
		t.Errorf("legacy shop must have no effective template, got %q", r.ID())
	}
	if n := len(d.TemplateLayoutAmendments()); n != 0 {
		t.Errorf("legacy retail has no legacy_layout: no amendments, got %d", n)
	}
}

func hidesTile(list []uislot.Amendment, key string) bool {
	for _, a := range list {
		if a.Key == key && a.Hide {
			return true
		}
	}
	return false
}

// The goldens are not vacuous: the same service shop with NO layout (no
// templates loaded) renders differently on every surface the salon amended.
func TestSalonGoldenDetectsMissingLayout(t *testing.T) {
	got := salonSurfaces(t, func(*testing.T, *common.Deps) {})
	for _, name := range []string{"menu.html", "items_rail.html", "nav_rail.html", "settings_nav.html"} {
		want, err := os.ReadFile(filepath.Join(salonGoldenDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got[name] == string(want) {
			t.Errorf("%s: an untemplated service shop must NOT match the salon golden", name)
		}
	}
}
