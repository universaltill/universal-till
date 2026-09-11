package pages

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/uislot"
	layoutsalon "github.com/universaltill/universal-till/plugins/layout-salon"
)

// railHrefsInOrder returns the hrefs of nav.html's .nav-primary links, in
// document order, from a rendered page.
func railHrefsInOrder(t *testing.T, body string) []string {
	t.Helper()
	start := strings.Index(body, `<div class="nav-primary">`)
	if start < 0 {
		t.Fatalf("no .nav-primary block in page: %s", body)
	}
	block := body[start:]
	block = block[:strings.Index(block, "</div>")]
	var hrefs []string
	for _, part := range strings.Split(block, `<a href="`)[1:] {
		hrefs = append(hrefs, part[:strings.Index(part, `"`)])
	}
	return hrefs
}

// wireRailProvider mirrors the one line internal/pages.Init adds for
// ut-docs#1912 — httpx.RailAmendmentsProvider = dp.RailAmendmentsSnapshot —
// for a test-built Deps, restoring the package default afterwards so no
// other test in this package sees this test's plugin state.
func wireRailProvider(t *testing.T, dp *common.Deps) {
	t.Helper()
	prev := httpx.RailAmendmentsProvider
	httpx.RailAmendmentsProvider = dp.RailAmendmentsSnapshot
	t.Cleanup(func() { httpx.RailAmendmentsProvider = prev })
}

// TestNavRail_ZeroPluginRendersCoreRailOnEveryPage: with no `layout` plugin
// installed, the rail on a real page render (here /menu, through the real
// httpx.Render path) is exactly uislot.CoreRail in declared order, with the
// pre-#1912 data-testid hooks and .nav-rail-only classes intact.
func TestNavRail_ZeroPluginRendersCoreRailOnEveryPage(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	wireRailProvider(t, dp)

	body := getPage(t, mux, "/menu").Body.String()
	got := railHrefsInOrder(t, body)
	want := make([]string, len(uislot.CoreRail))
	for i, e := range uislot.CoreRail {
		want[i] = e.Href
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("zero-plugin rail drifted from CoreRail:\n got %v\nwant %v", got, want)
	}
	for _, tag := range []string{
		`<a href="/" class="nav-toggle" data-testid="nav-till">`,
		`<a href="/menu" class="nav-toggle" data-testid="nav-menu">`,
		`<a href="/inventory" class="nav-toggle nav-rail-only" data-testid="kiosk-inventory-link">`,
		`<a href="/orders" class="nav-toggle nav-rail-only" data-testid="nav-orders">`,
	} {
		if !strings.Contains(body, tag) {
			t.Errorf("expected rail link %s on the rendered page", tag)
		}
	}
}

// TestNavRail_SalonLayoutReordersOrdersAheadOfInventory installs the actual
// plugins/layout-salon/plugin.json bytes (not a fixture) through the real
// PersistManifest/ReloadPlugins path — the same shape as
// TestItemsPage_SalonLayoutRelabelsAndReordersLibraryRow — and asserts the
// rail on a rendered page follows its third, Rail-slot entry: Orders
// (Order 250) now sits between Menu (200) and Inventory (300). Everything
// an amendment cannot change travels with the moved link: its
// data-testid, its .nav-rail-only class, its icon.
func TestNavRail_SalonLayoutReordersOrdersAheadOfInventory(t *testing.T) {
	// Same paths.Init ordering/restore shape as the Items-slot test
	// (independent review of ut-docs#1911, finding 11).
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })

	mux, dp := newMenuPageTestDeps(t, baseMenu)
	wireRailProvider(t, dp)

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

	// The Deps-side pool is slot-filtered: exactly the manifest's rail
	// amendment, nothing from its menu/items entries leaks in.
	rail := dp.RailAmendmentsSnapshot()
	if len(rail) != 1 || rail[0].Slot != uislot.RailSlot || rail[0].Key != "/orders" || rail[0].Order == nil {
		t.Fatalf("expected exactly the salon layout's Rail-slot /orders reorder, got %+v", rail)
	}

	body := getPage(t, mux, "/menu").Body.String()
	got := railHrefsInOrder(t, body)
	want := []string{"/", "/menu", "/orders", "/inventory"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("salon layout must move Orders ahead of Inventory:\n got %v\nwant %v", got, want)
	}
	if !strings.Contains(body, `<a href="/orders" class="nav-toggle nav-rail-only" data-testid="nav-orders">`) {
		t.Fatalf("the moved Orders link must keep its data-testid and .nav-rail-only class, got:\n%s", body)
	}
	if n := strings.Count(body, `data-testid="nav-`); n < 3 {
		t.Fatalf("expected the nav-till/nav-menu/nav-orders hooks to survive the reorder, found %d", n)
	}

	// Sell and Menu — the Decision J protected pair — are untouched, and the
	// Menu slot's own salon amendments (hide /tables etc.) did NOT leak into
	// the rail: still exactly four links.
	if len(got) != len(uislot.CoreRail) {
		t.Fatalf("a reorder must not drop or add rail links, got %d", len(got))
	}
}
