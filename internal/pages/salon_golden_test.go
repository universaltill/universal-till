package pages

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/shoptemplate"
)

// The salon fold-in (ADR-0190, ut-docs#6332 S5) must not change one byte a
// service shop sees. The goldens under testdata/salon_golden were captured
// from the build that still shipped the real plugins/layout-salon plugin;
// salonSurfaces renders the four surfaces the plugin amends (Menu launcher,
// Items rail, nav rail, Settings sidebar) for a service shop. Regenerate
// ONLY from a build that still has the plugin, with UPDATE_GOLDEN=1.

const salonGoldenDir = "internal/pages/testdata/salon_golden"

// salonVolatileRE matches the per-process build stamps in the page head
// (shell hash, asset cache-busters), which change between runs.
var salonVolatileRE = regexp.MustCompile(`(ut-shell" content=")[0-9a-f]+|(\?v=)\d+`)

func salonStable(s string) string { return salonVolatileRE.ReplaceAllString(s, "${1}${2}X") }

func salonBetween(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("%q not rendered in:\n%s", start, body)
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		t.Fatalf("%q not terminated by %q", start, end)
	}
	return body[i : i+j]
}

// salonSurfaces renders the surfaces for a service shop. setup runs after
// the till is built and shop.type=service is stored (no ledger), and must
// leave the layout in force (plugin installed, or the template state).
func salonSurfaces(t *testing.T, setup func(t *testing.T, d *common.Deps)) map[string]string {
	t.Helper()
	out := map[string]string{}
	t.Setenv("UT_AUTH", "off")

	isolatePluginDir(t)
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)
	registerAdmin(mux, dp)
	if err := dp.Settings.Set(t.Context(), common.KeyShopType, "service"); err != nil {
		t.Fatal(err)
	}
	// The status bar shows an "update available" chip from a process-global
	// updater state other tests leave behind; the golden is about the layout.
	prevAvail := httpx.UpdateAvailable
	httpx.UpdateAvailable = func() bool { return false }
	t.Cleanup(func() { httpx.UpdateAvailable = prevAvail })
	dp.BaseMenu = baseMenu // production wiring (init.go): ReloadPlugins rebuilds Menu from it
	setup(t, dp)
	getAsManager := func(path string) string {
		req := auth.WithUser(httptest.NewRequest("GET", path, nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	out["menu.html"] = getAsManager("/menu")
	out["admin.html"] = getAsManager("/admin")
	items := getPage(t, mux, "/items").Body.String()
	out["items_rail.html"] = salonBetween(t, items, `id="items-rail"`, "</nav>")
	out["nav_rail.html"] = salonBetween(t, items, `<div class="nav-primary">`, `<div class="nav-right">`)

	mux2, _, d2 := newFullAuthDeps(t)
	pm, err := plugins.Init(t.Context(), d2.Cfg, d2.Db)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	d2.Pm = pm
	if err := d2.Settings.Set(t.Context(), common.KeyShopType, "service"); err != nil {
		t.Fatal(err)
	}
	if d2.BaseMenu == nil {
		d2.BaseMenu = baseMenu
	}
	setup(t, d2)
	req := httptest.NewRequest("GET", "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux2.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	out["settings_nav.html"] = salonBetween(t, body, `<ul id="settings-nav-index"`, "</ul>") + "\n" +
		salonBetween(t, body, `<nav class="settings-home"`, "</nav>") + "\n"
	for k, v := range out {
		out[k] = salonStable(v)
	}
	return out
}

func checkSalonGolden(t *testing.T, got map[string]string, update bool) {
	t.Helper()
	for name, body := range got {
		p := filepath.Join(salonGoldenDir, name)
		if update {
			if err := os.MkdirAll(salonGoldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read golden %s: %v", p, err)
		}
		if body != string(want) {
			gotDir, _ := os.MkdirTemp("", "salon-got-")
			gotPath := filepath.Join(gotDir, name)
			_ = os.WriteFile(gotPath, []byte(body), 0o644)
			t.Errorf("%s differs from the salon-plugin golden (a service shop's layout changed); got saved to %s", name, gotPath)
		}
	}
}

// TestServiceTemplateReplaysSalonByteIdentical is the S5 proof (ADR-0190,
// ut-docs#6332): a service shop that predates templates (legacy mode: type
// set, no ledger) renders the Menu launcher, the admin tree, the Items rail,
// the nav rail and the Settings sidebar byte-for-byte like the build that
// still shipped the salon layout plugin, with NO plugin installed. The
// goldens were captured from that build before the plugin was deleted.
func TestServiceTemplateReplaysSalonByteIdentical(t *testing.T) {
	got := salonSurfaces(t, serviceLegacySetup)
	checkSalonGolden(t, got, false)
}

// TestLegacyServiceShopKeepsSalonLayout: the same state with the plugin
// tables genuinely empty — no plugin row anywhere — still hides Tables and
// Kitchen stations and relabels Items to Services.
func TestLegacyServiceShopKeepsSalonLayout(t *testing.T) {
	got := salonSurfaces(t, serviceLegacySetup)
	if strings.Contains(got["menu.html"], `href="/tables"`) || strings.Contains(got["menu.html"], `href="/kitchen-stations"`) {
		t.Error("a legacy service shop's Menu must hide Tables and Kitchen stations")
	}
	if !strings.Contains(got["menu.html"], ">Services<") {
		t.Error("a legacy service shop's Menu must show Items as Services")
	}
	if strings.Contains(got["menu.html"], "menu-template-tucked") {
		t.Error("legacy mode must not show the template's tucked-away line: the menu is byte-identical to before")
	}
}

// serviceLegacySetup loads the real templates and rebuilds the layout the way
// a boot does (ReloadPlugins also rebuilds the plugin-page tiles, as the
// captured golden's build did); shop.type=service is already stored, with no
// ledger, so the shop is in legacy mode.
func serviceLegacySetup(t *testing.T, d *common.Deps) {
	t.Helper()
	useRealShopTemplates(t, d)
	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestServiceLedgerShopGetsHardwareTileDescription: a service shop that has
// accepted a template (ledger set) gets the #7205 Hardware tile description;
// the legacy replay shop (golden) does not (ADR-0190 byte identity).
func TestServiceLedgerShopGetsHardwareTileDescription(t *testing.T) {
	const desc = "Receipt printer and the tills linked to this shop."
	ledger := salonSurfaces(t, func(t *testing.T, d *common.Deps) {
		serviceLegacySetup(t, d)
		if err := d.Settings.Set(t.Context(), shoptemplate.LedgerKey, `{"id":"service","version":1,"seeded":{},"at":"`+time.Now().UTC().Format(time.RFC3339)+`"}`); err != nil {
			t.Fatal(err)
		}
		d.ReloadTemplate(t.Context())
	})
	if !strings.Contains(ledger["settings_nav.html"], desc) {
		t.Error("a template-ledger service shop's Hardware tile must carry its description")
	}
	legacy := salonSurfaces(t, serviceLegacySetup)
	if strings.Contains(legacy["settings_nav.html"], desc) {
		t.Error("a legacy service shop's Hardware tile must stay plain")
	}
}
