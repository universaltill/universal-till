package catalog

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#3605: on an additional till whose main till can't be reached,
// the item editor's write-through controls (details, Save/Delete,
// barcodes, cost/lead time/reorder level, variants, modifier attach/detach,
// keypad mapping) render inside disabled `data-main-till-lock` elements;
// with the main till reachable, or on a standalone till, they render
// enabled. The partials loaded into the dialog later (variants panel,
// modifiers tab) carry the same state themselves.

var lockTagRe = regexp.MustCompile(`<[a-z]+\b[^>]*\bdata-main-till-lock\b[^>]*>`)
var disabledAttrRe = regexp.MustCompile(`\sdisabled(\s|>|=)`)

func countLocks(body string) (total, disabled int) {
	for _, tag := range lockTagRe.FindAllString(body, -1) {
		total++
		if disabledAttrRe.MatchString(tag) {
			disabled++
		}
	}
	return total, disabled
}

// lockedAround reports whether marker renders inside a data-main-till-lock
// element (a fieldset — the only lock shape the catalogue templates use).
func lockedAround(t *testing.T, body, marker string) bool {
	t.Helper()
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("marker %q not rendered", marker)
	}
	before := body[:at]
	open := strings.LastIndex(before, "<fieldset data-main-till-lock")
	return open >= 0 && !strings.Contains(before[open:], "</fieldset>")
}

// newLockCatalogMux is a migrated catalogue with one item that has one
// own modifier group and one attachable group, on a till that follows a
// main till (unless standalone), with the main till's reachability under
// the test's control.
func newLockCatalogMux(t *testing.T, standalone bool, unreachable *bool) *http.ServeMux {
	t.Helper()
	chdirToRepoRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "lock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	if _, err := dbase.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbase.DB.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('var1','itm1','SKU1-L','Large',380,1)`); err != nil {
		t.Fatal(err)
	}
	modRepo := data.NewModifierRepo(dbase.DB)
	for _, g := range []struct{ id, name string }{{"gOwn", "Extras"}, {"gOther", "Milk"}} {
		if _, err := modRepo.CreateGroup(t.Context(), g.id, g.name, false, 0, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := modRepo.LinkGroupToItem(t.Context(), "itm1", "gOwn", 0); err != nil {
		t.Fatal(err)
	}
	store := settings.NewStore(dbase.DB)
	if !standalone {
		if err := store.Set(t.Context(), "sync.primary_url", "http://primary.till.local:8080"); err != nil {
			t.Fatal(err)
		}
	}
	dp := &common.Deps{Db: dbase.DB, Settings: store, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}}
	if unreachable != nil {
		dp.MainTillUnreachable = func(context.Context) bool { return *unreachable }
	}
	mux := http.NewServeMux()
	Register(mux, dp)
	return mux
}

func getOK(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

var lockPaths = []string{
	"/catalog",
	"/api/catalog/item-variants?item_id=itm1",
	"/api/catalog/modifier-groups-panel?item_id=itm1",
}

func TestCatalogMainTillLock_LockedWhileMainTillUnreachable(t *testing.T) {
	unreachable := true
	mux := newLockCatalogMux(t, false, &unreachable)
	for _, p := range lockPaths {
		total, disabled := countLocks(getOK(t, mux, p))
		if total == 0 || disabled != total {
			t.Errorf("GET %s: %d of %d data-main-till-lock elements disabled, want all (and at least one)", p, disabled, total)
		}
	}
	page := getOK(t, mux, "/catalog")
	for _, marker := range []string{`id="item-form-submit"`, `id="item-form-delete"`, `id="item-name"`, `id="keypad-capture"`} {
		if !lockedAround(t, page, marker) {
			t.Errorf("/catalog: %s is not inside a main-till lock", marker)
		}
	}
	// Kept usable: Close, the tabs, search, label printing.
	for _, marker := range []string{`id="item-form-close-btn"`, `id="item-form-tab-details"`, `id="catalog-search"`, `hx-post="/api/print/labels"`} {
		if lockedAround(t, page, marker) {
			t.Errorf("/catalog: %s must stay usable but sits inside a main-till lock", marker)
		}
	}
	// The dialog says why (it covers the page banner).
	away := html.EscapeString(httpx.T("en", "sync.banner_replica_main_till_unreachable"))
	if strings.Count(page, away) < 2 {
		t.Errorf("/catalog: expected the 'can't reach the main till' text in both the banner and the item dialog")
	}
	variants := getOK(t, mux, "/api/catalog/item-variants?item_id=itm1")
	for _, marker := range []string{`hx-post="/api/catalog/item-cost"`, `hx-post="/api/catalog/barcode"`, `form="vf-new" type="text" name="name"`, `form="vf-new" class="btn" type="submit"`, `form="vf-var1" type="text" name="name"`, `form="vf-var1" class="btn primary" type="submit"`} {
		if !lockedAround(t, variants, marker) {
			t.Errorf("variants panel: %s is not inside a main-till lock", marker)
		}
	}
	// A variant's photo writes only image files on this till (never
	// requirePrimary, no write-through): it stays usable (#3605 review).
	if lockedAround(t, variants, `hx-post="/api/catalog/variant/image"`) {
		t.Errorf("variants panel: the variant photo upload must stay usable but sits inside a main-till lock")
	}
	mods := getOK(t, mux, "/api/catalog/modifier-groups-panel?item_id=itm1")
	for _, marker := range []string{`hx-post="/api/catalog/modifier-group/detach"`, `hx-post="/api/catalog/modifier-group/attach"`} {
		if !lockedAround(t, mods, marker) {
			t.Errorf("modifiers tab: %s is not inside a main-till lock", marker)
		}
	}
}

func TestCatalogMainTillLock_EnabledWhenReachableOrStandalone(t *testing.T) {
	reachable := false
	for name, mux := range map[string]*http.ServeMux{
		"additional till, main reachable": newLockCatalogMux(t, false, &reachable),
		"standalone till":                 newLockCatalogMux(t, true, nil),
	} {
		for _, p := range lockPaths {
			total, disabled := countLocks(getOK(t, mux, p))
			if total == 0 {
				t.Errorf("%s: GET %s renders no data-main-till-lock element (it must render enabled, so the page can lock live)", name, p)
			}
			if disabled != 0 {
				t.Errorf("%s: GET %s: %d data-main-till-lock element(s) disabled, want none", name, p, disabled)
			}
		}
	}
}

// The catalogue banner renders both texts, the inactive one hidden, so
// base.html's chip follower can swap them on an open page.
func TestCatalogMainTillLock_BannerCarriesBothStates(t *testing.T) {
	away := html.EscapeString(httpx.T("en", "sync.banner_replica_main_till_unreachable"))
	here := html.EscapeString(httpx.T("en", "sync.banner_replica_write_through"))
	for _, unreachable := range []bool{true, false} {
		u := unreachable
		body := getOK(t, newLockCatalogMux(t, false, &u), "/catalog")
		a := regexp.MustCompile(`<span data-main-till-away( hidden)?>` + regexp.QuoteMeta(away) + `</span>`).FindStringSubmatch(body)
		h := regexp.MustCompile(`<span data-main-till-here( hidden)?>` + regexp.QuoteMeta(here) + `</span>`).FindStringSubmatch(body)
		if a == nil || h == nil {
			t.Fatalf("unreachable=%v: the banner must carry both texts in data-main-till-away/-here spans", unreachable)
		}
		if (a[1] == "") != unreachable || (h[1] == "") == unreachable {
			t.Fatalf("unreachable=%v: wrong banner text visible (away hidden=%q, here hidden=%q)", unreachable, a[1], h[1])
		}
	}
}
