package pages

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	nethtml "golang.org/x/net/html"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/catalogsync"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#3605: on an additional till whose main till can't be reached,
// every catalogue control that would travel to the main till
// (catalogsync.Routes) renders inside a `data-main-till-lock` element that
// is disabled — the save is refused server-side anyway, so the operator
// should see that before typing a whole edit. base.html's followChip then
// keeps those elements (and the catalogue/inventory banner) in step with
// the status chip while the page stays open.

// mainTillLockTagRe matches the opening tag of any element carrying the
// lock attribute.
var (
	mainTillLockTagRe = regexp.MustCompile(`<[a-z]+\b[^>]*\bdata-main-till-lock\b[^>]*>`)
	disabledAttrRe    = regexp.MustCompile(`\sdisabled(\s|>|=)`)
)

// mainTillLocks returns how many lock elements the body renders and how
// many of them are disabled.
func mainTillLocks(body string) (total, disabled int) {
	for _, tag := range mainTillLockTagRe.FindAllString(body, -1) {
		total++
		if disabledAttrRe.MatchString(tag) {
			disabled++
		}
	}
	return total, disabled
}

func assertAllLocked(t *testing.T, what, body string) {
	t.Helper()
	total, disabled := mainTillLocks(body)
	if total == 0 {
		t.Fatalf("%s: no data-main-till-lock element rendered", what)
	}
	if disabled != total {
		t.Fatalf("%s: %d of %d data-main-till-lock elements are disabled while the main till is unreachable", what, disabled, total)
	}
}

func assertNoneLocked(t *testing.T, what, body string) {
	t.Helper()
	if _, disabled := mainTillLocks(body); disabled != 0 {
		t.Fatalf("%s: %d data-main-till-lock element(s) disabled, want none", what, disabled)
	}
}

func TestMainTillLock_CategoriesPage(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // rendering past the catalog_management gate
	render := func(t *testing.T, mux *http.ServeMux) string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/categories", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /categories = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	t.Run("additional till, main unreachable", func(t *testing.T) {
		mux, d := newCategoriesTestMux(t)
		setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", "")
		d.MainTillUnreachable = func(context.Context) bool { return true }
		body := render(t, mux)
		assertAllLocked(t, "GET /categories", body)
		// The dialog's Save (shared record_dialog.html, a button outside
		// the form) and the deactivate/activate forms are inside a lock.
		for _, marker := range []string{`class="btn primary record-dialog-save"`, `data-record-destructive-form data-record-when="active=1"`, `id="category-form"`} {
			assertInsideLock(t, body, marker)
		}
		away := html.EscapeString(httpx.T("en", "sync.banner_replica_main_till_unreachable"))
		if !strings.Contains(body, away) {
			t.Fatalf("locked category dialog does not say why (%q)", away)
		}
	})
	t.Run("additional till, main reachable", func(t *testing.T) {
		mux, d := newCategoriesTestMux(t)
		setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", "")
		d.MainTillUnreachable = func(context.Context) bool { return false }
		body := render(t, mux)
		if total, _ := mainTillLocks(body); total == 0 {
			t.Fatal("reachable main till: the lock elements must still render (enabled), so the page can lock live")
		}
		assertNoneLocked(t, "GET /categories", body)
	})
	t.Run("standalone till", func(t *testing.T) {
		mux, _ := newCategoriesTestMux(t)
		assertNoneLocked(t, "GET /categories", render(t, mux))
	})
}

func TestMainTillLock_DesignerCategories(t *testing.T) {
	mgr := auth.User{ID: "m1", Role: "manager"}
	t.Run("additional till, main unreachable", func(t *testing.T) {
		mux, d := newButtonsMuxRealSession(t)
		seedOneButton(t, d)
		if _, err := d.Db.Exec(`INSERT INTO categories(id,name,sort_order,is_active) VALUES ('cat-a','Drinks',0,1),('cat-off','Retired',1,0)`); err != nil {
			t.Fatal(err)
		}
		setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", "")
		d.MainTillUnreachable = func(context.Context) bool { return true }
		rec := getWithUser(mux, "/ui/buttons?mode=edit", &mgr)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ui/buttons?mode=edit = %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		assertAllLocked(t, "Designer categories", body)
		for _, marker := range []string{`id="designer-cat-form-cat-a"`, `id="designer-cat-active-cat-a"`, `id="designer-cat-active-cat-off"`, `id="designer-cat-form-new"`} {
			assertInsideLock(t, body, marker)
		}
		// Out of scope (follow-up card): the quick-button editing on the
		// same fragment (/api/buttons/*) is never locked by this card.
		if at := strings.Index(body, `hx-post="/api/buttons/`); at >= 0 && insideLock(body, at) {
			t.Fatal("a /api/buttons/* control sits inside a main-till lock (out of scope for ut-docs#3605)")
		}
	})
	t.Run("additional till, main reachable", func(t *testing.T) {
		mux, d := newButtonsMuxRealSession(t)
		if _, err := d.Db.Exec(`INSERT INTO categories(id,name,sort_order,is_active) VALUES ('cat-a','Drinks',0,1)`); err != nil {
			t.Fatal(err)
		}
		setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", "")
		d.MainTillUnreachable = func(context.Context) bool { return false }
		body := getWithUser(mux, "/ui/buttons?mode=edit", &mgr).Body.String()
		if total, _ := mainTillLocks(body); total == 0 {
			t.Fatal("reachable main till: the Designer's category lock elements must still render")
		}
		assertNoneLocked(t, "Designer categories", body)
	})
}

func TestMainTillLock_InventoryBannerCarriesBothStates(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	away := html.EscapeString(httpx.T("en", "sync.banner_replica_main_till_unreachable"))
	here := html.EscapeString(httpx.T("en", "sync.banner_replica_write_through"))
	for _, unreachable := range []bool{true, false} {
		d := &common.Deps{Db: db, Settings: settings.NewStore(db), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
		setReplicaSettings(t, d.Settings, "http://primary.till.local:8080", "")
		u := unreachable
		d.MainTillUnreachable = func(context.Context) bool { return u }
		mux := http.NewServeMux()
		registerInventoryPage(mux, d)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
		body := rec.Body.String()
		awaySpan := regexp.MustCompile(`<span data-main-till-away( hidden)?>` + regexp.QuoteMeta(away) + `</span>`).FindStringSubmatch(body)
		hereSpan := regexp.MustCompile(`<span data-main-till-here( hidden)?>` + regexp.QuoteMeta(here) + `</span>`).FindStringSubmatch(body)
		if awaySpan == nil || hereSpan == nil {
			t.Fatalf("unreachable=%v: the banner must carry both texts (data-main-till-away / data-main-till-here) so it can follow the chip", unreachable)
		}
		if (awaySpan[1] == "") != unreachable || (hereSpan[1] == "") == unreachable {
			t.Fatalf("unreachable=%v: wrong banner text visible (away hidden=%q, here hidden=%q)", unreachable, awaySpan[1], hereSpan[1])
		}
		// Inventory has no write-through control (a stock receipt is not a
		// catalogsync route), so nothing on it is locked.
		assertNoLockElements(t, body)
	}
}

func assertNoLockElements(t *testing.T, body string) {
	t.Helper()
	if total, _ := mainTillLocks(body); total != 0 {
		t.Fatalf("/inventory renders %d data-main-till-lock element(s); its stock receipt must stay usable", total)
	}
}

// insideLock reports whether the byte offset at sits inside an element
// carrying data-main-till-lock in body (counted by open/close fieldsets and
// buttons around it — the only two lock shapes the templates use).
func insideLock(body string, at int) bool {
	before := body[:at]
	open := strings.LastIndex(before, "data-main-till-lock")
	if open < 0 {
		return false
	}
	// The lock tag's own element name.
	tagStart := strings.LastIndex(before[:open], "<")
	name := strings.Fields(before[tagStart+1 : open])[0]
	return !strings.Contains(before[open:], "</"+name+">")
}

func assertInsideLock(t *testing.T, body, marker string) {
	t.Helper()
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("marker %q not rendered", marker)
	}
	if !insideLock(body, at) {
		t.Fatalf("%q is not inside a data-main-till-lock element", marker)
	}
}

// ---- the guard: every write-through control in these templates is lockable

// mainTillLockTemplates are the catalogue pages and the partials they
// render or load (ut-docs#3605). A new catalogue screen or partial that
// posts to a catalogsync route belongs here.
var mainTillLockTemplates = []string{
	"web/ui/pages/catalog.html",
	"web/ui/pages/inventory.html",
	"web/ui/pages/categories.html",
	"web/ui/pages/designer.html",
	"web/ui/partials/catalog_table.html",
	"web/ui/partials/catalog_row.html",
	"web/ui/partials/catalog_variants.html",
	"web/ui/partials/catalog_barcode_backfill.html",
	"web/ui/partials/modifier_group_admin.html",
	"web/ui/partials/photo_cutout.html",
	"web/ui/partials/stock_table.html",
	"web/ui/partials/record_dialog.html",
	"web/ui/partials/buttons.html",
	"web/ui/partials/buttons_admin.html",
}

var (
	tplCommentRe = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	tplActionRe  = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// tplPlaceholder stands in for a template action: in a URL it is one
// unknown path segment (or part of one), so it matches any segment.
const tplPlaceholder = "TPLX"

// lockRoutePatterns is catalogsync.Routes' paths, minus the quick-button
// routes (the sale screen's tile editing is a follow-up card).
func lockRoutePatterns() []string {
	var out []string
	for _, r := range catalogsync.Routes {
		p := strings.TrimSpace(strings.TrimPrefix(r.Pattern, "POST "))
		if strings.HasPrefix(p, "/api/buttons/") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// routeMatch reports whether a rendered URL (template actions replaced by
// tplPlaceholder) can be the route pattern: same number of segments, a
// literal matches itself, {name} matches any segment, and a segment built
// by a template action matches anything (conservative).
func routeMatch(pattern, value string) bool {
	if i := strings.IndexAny(value, "?#"); i >= 0 {
		value = value[:i]
	}
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	vs := strings.Split(strings.Trim(value, "/"), "/")
	if len(ps) != len(vs) {
		return false
	}
	for i := range ps {
		switch {
		case strings.HasPrefix(ps[i], "{"):
			if vs[i] == "" {
				return false
			}
		case strings.Contains(vs[i], tplPlaceholder):
		case ps[i] != vs[i]:
			return false
		}
	}
	return true
}

type lockFinding struct {
	file, route, tag string
	locked           bool
}

// scanTemplateForWriteThrough tokenizes one template (actions replaced by
// a placeholder) and reports every element whose hx-post (or a form's
// action) can be a write-through route, with whether it sits inside — or
// itself is — a data-main-till-lock element.
func scanTemplateForWriteThrough(t *testing.T, file string, patterns []string) []lockFinding {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	s := tplCommentRe.ReplaceAllString(string(src), "")
	s = tplActionRe.ReplaceAllString(s, " "+tplPlaceholder+" ")

	type open struct {
		name   string
		locked bool
	}
	var stack []open
	lockedNow := func() bool {
		for _, o := range stack {
			if o.locked {
				return true
			}
		}
		return false
	}
	var out []lockFinding
	z := nethtml.NewTokenizer(strings.NewReader(s))
	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			return out
		}
		tok := z.Token()
		switch tt {
		case nethtml.StartTagToken, nethtml.SelfClosingTagToken:
			self := false
			var post, action string
			for _, a := range tok.Attr {
				switch a.Key {
				case "data-main-till-lock":
					self = true
				case "hx-post":
					post = a.Val
				case "action":
					if tok.Data == "form" {
						action = a.Val
					}
				}
			}
			for _, v := range []string{post, action} {
				if v == "" {
					continue
				}
				for _, p := range patterns {
					if routeMatch(p, v) {
						out = append(out, lockFinding{file: file, route: p, tag: tok.String(), locked: self || lockedNow()})
					}
				}
			}
			if tt == nethtml.StartTagToken && !voidElement[tok.Data] {
				stack = append(stack, open{name: tok.Data, locked: self})
			}
		case nethtml.EndTagToken:
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == tok.Data {
					stack = stack[:i]
					break
				}
			}
		}
	}
}

var voidElement = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

// TestMainTillLock_EveryWriteThroughControlIsLockable sees only hx-post and
// <form action>. A post made from JS (fetch / htmx.ajax) is covered only
// when its inputs or trigger sit in a lock. The category lists'
// data-reorder-url drag/Alt+Arrow reorder (list-reorder.js) is NOT locked
// yet and is invisible here; it is tracked on ut-docs#4418.
func TestMainTillLock_EveryWriteThroughControlIsLockable(t *testing.T) {
	chdirRoot(t)
	patterns := lockRoutePatterns()
	covered := map[string]bool{}
	for _, f := range mainTillLockTemplates {
		for _, fd := range scanTemplateForWriteThrough(t, filepath.FromSlash(f), patterns) {
			covered[fd.route] = true
			if !fd.locked {
				t.Errorf("%s: %s posts to write-through route %s but is not inside a data-main-till-lock element (ut-docs#3605)", fd.file, fd.tag, fd.route)
			}
		}
	}
	// Not vacuous: these routes are posted from these templates today. If
	// one disappears, the scan (or the templates) changed — check which.
	for _, want := range []string{
		"/api/catalog/item/deactivate",
		"/api/catalog/barcode",
		"/api/catalog/barcode/delete",
		"/api/catalog/item-cost",
		"/api/catalog/item-lead-time",
		"/api/catalog/item-reorder-level",
		"/api/catalog/variant",
		"/api/catalog/modifier-group/attach",
		"/api/catalog/modifier-group/detach",
		"/api/categories",
		"/api/designer/categories",
		"/api/designer/categories/{id}",
		"/api/designer/categories/{id}/active",
	} {
		if !covered[want] {
			t.Errorf("the scan found no control posting to %s in the catalogue templates", want)
		}
	}
}

func TestMainTillLock_RouteMatch(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"/api/categories", "/api/categories", true},
		{"/api/categories/{id}", "/api/categories/" + tplPlaceholder, true},
		{"/api/categories/{id}/active", "/api/categories/c1/active", true},
		{"/api/categories/{id}", "/api/categories", false},
		{"/api/catalog/barcode", "/api/catalog/barcode-backfill", false},
		{"/api/catalog/barcode", "/api/catalog/barcode?x=1", true},
		{"/api/catalog/item", "/api/" + tplPlaceholder + "/item", true},
	}
	for _, c := range cases {
		if got := routeMatch(c.pattern, c.value); got != c.want {
			t.Errorf("routeMatch(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}
