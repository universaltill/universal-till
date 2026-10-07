package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// Install-time guard against ut-docs#499: internal/pages/plugin_page.go's
// findPageEntry resolves GET /plugin/… by matching route against
// ListPageEntries' rows and returning the first hit — unguarded, so two
// plugins declaring *different* keys but the *same* route both install
// cleanly, and whichever sorts first silently serves every request to that
// route. Distinct namespace from ut-docs#472's key-collision guard
// (page_key_validation_test.go) — same shape, same two call sites
// (PersistManifest, Rollback), no docs exemption (see validatePageEntryRoutes).

// routeManifest is a page-entry manifest with an explicit route and a
// distinct, non-colliding key, isolating the route namespace from the key
// namespace so these tests exercise only the new check.
func routeManifest(id, key, route string) *Manifest {
	return &Manifest{
		ID:         id,
		Name:       "Page " + id,
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Entries: []ManifestEntry{
			{Type: "page", Key: key, Label: "Page " + key, Route: route},
		},
	}
}

func TestPersistManifest_RejectsPageRouteOwnedByAnotherPlugin(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, routeManifest("com.first.route", "akey", "/plugin/docs"), InstallOptions{}); err != nil {
		t.Fatalf("first plugin must install cleanly: %v", err)
	}
	// Distinct key, same route — the key check must not fire; only the
	// route check should reject this.
	err := PersistManifest(ctx, d.DB, routeManifest("com.second.route", "bkey", "/plugin/docs"), InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a page entry route already owned by another plugin")
	}
	if !strings.Contains(err.Error(), "/plugin/docs") || !strings.Contains(err.Error(), "com.first.route") {
		t.Fatalf("collision error should name the route and its owner, got: %v", err)
	}
	// The rejected plugin must not be half-installed.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.route'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_catalog WHERE id = 'com.second.route'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugin_catalog row(s), want 0", n)
	}
	// The first plugin's entry — and therefore GET /plugin/docs' dispatch
	// target (internal/pages/plugin_page.go's findPageEntry, route match,
	// first-row-wins) — must still resolve to the first plugin, unchanged.
	var owner string
	if err := d.DB.QueryRow(`SELECT plugin_id FROM plugin_entries WHERE type='page' AND route='/plugin/docs'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "com.first.route" {
		t.Fatalf("first plugin's page route changed owner: %q", owner)
	}
	var routeCount int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE type='page' AND route='/plugin/docs'`).Scan(&routeCount); err != nil {
		t.Fatal(err)
	}
	if routeCount != 1 {
		t.Fatalf("want exactly 1 entry at the contested route (first-row-wins dispatch depends on this), got %d", routeCount)
	}
}

func TestPersistManifest_PageRouteSelfUpgradeNotAConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, routeManifest("com.self.route", "mykey", "/plugin/mine"), InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	upgraded := routeManifest("com.self.route", "mykey", "/plugin/mine")
	upgraded.Version = "1.0.1"
	if err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{}); err != nil {
		t.Fatalf("reinstalling/upgrading the same plugin's own page route must not conflict with itself: %v", err)
	}
}

// A single manifest declaring two page entries with distinct keys but the
// same route must be rejected too (independent review finding, ut-docs#499):
// unlike page entry keys, plugin_entries has no unique constraint on route,
// so nothing else catches this — the cross-plugin check above (via
// FindPageRouteConflicts) never even runs, since both rows belong to the
// SAME pluginID it deliberately excludes.
func TestPersistManifest_RejectsDuplicateRouteWithinManifest(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := &Manifest{
		ID: "com.dup.route", Name: "Dup Route", Version: "1.0.0", Entrypoint: "./main.wasm",
		Entries: []ManifestEntry{
			{Type: "page", Key: "first", Label: "First", Route: "/plugin/dup"},
			{Type: "page", Key: "second", Label: "Second", Route: "/plugin/dup"},
		},
	}
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted two page entries in the same manifest sharing a route")
	}
	if !strings.Contains(err.Error(), "/plugin/dup") {
		t.Fatalf("collision error should name the route, got: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.dup.route'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
}

func TestPersistManifest_EmptyPageRouteNeverChecked(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// A page entry with no route isn't dispatchable via findPageEntry
	// (requires Route != ""), so two plugins both omitting it must not be
	// treated as a collision.
	if err := PersistManifest(ctx, d.DB, routeManifest("com.first.noroute", "akey", ""), InstallOptions{}); err != nil {
		t.Fatalf("first plugin with no route must install cleanly: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, routeManifest("com.second.noroute", "bkey", ""), InstallOptions{}); err != nil {
		t.Fatalf("a second plugin with no route must not be rejected as a route collision: %v", err)
	}
}

func TestRollback_RejectsCollidingPageRoutes(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()

	// Plugin currently at 2.0.0 with a clean route; its on-disk 1.0.0
	// manifest (the rollback target) declares a route that now collides —
	// legacy versions predating this validation could carry colliding
	// routes, and rollback writes entries without going through
	// PersistManifest.
	if err := PersistManifest(ctx, d.DB, routeManifest("com.other.route", "otherkey", "/plugin/shared-route"), InstallOptions{}); err != nil {
		t.Fatalf("install colliding owner: %v", err)
	}
	v2 := routeManifest("com.rb.route", "rbkey", "/plugin/rb-route")
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.route', '1.0.0', 'RB Route', 'wasm', './main.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`)

	dir := filepath.Join(base, "com.rb.route", "versions", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Distinct key from the owner's ("rbkeylegacy" vs "otherkey") so only
	// the route check can be what rejects this.
	manifest := `{"id":"com.rb.route","name":"RB Route","version":"1.0.0","entrypoint":"./main.wasm","runtime":"wasm",
"entries":[{"type":"page","key":"rbkeylegacy","label":"Old RB Page","route":"/plugin/shared-route"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(d.DB, base)
	err := rm.Rollback(ctx, "com.rb.route", "1.0.0", "tester")
	if err == nil {
		t.Fatal("rollback restored a page entry route colliding with another installed plugin")
	}
	if !strings.Contains(err.Error(), "/plugin/shared-route") {
		t.Fatalf("rollback collision error should name the route, got: %v", err)
	}
	// The current version's entries must survive the failed rollback.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = 'com.rb.route' AND key = 'rbkey'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("failed rollback must leave current entries intact, found %d", n)
	}
}

// ut-docs#3786: a plugin page may not claim a route under a subtree
// namespace core owns — the `/` catch-all would render it where clients
// expect a core API/asset response.
func TestPersistManifest_RejectsReservedPageRoutes(t *testing.T) {
	for _, route := range []string{
		"/api/x", "/api/plugins/marketplace", "/ui/foo", "/v1/bar",
		"/public/x.js", "/ext/y", "/plugin-icons/a/b/c", "/api",
		"/self-order/promo", "/themes/a/b", "/o/x/y",
		// ut-docs#3791: core namespaces with {wildcard} children.
		"/help/a/b", "/orders/a/b", "/journal/a/b", "/plugins/a/b",
		"/refund/a/b", "/invoice/a/b", "/kitchen-display/a/b",
		// ut-docs#3818: core namespaces with only literal multi-segment
		// children (no {wildcard}, no subtree catch-all).
		"/settings/vendor-x", "/catalog/zz", "/users/zz", "/open-orders/zz", "/recovery/zz",
	} {
		t.Run(route, func(t *testing.T) {
			d := openRealDB(t)
			err := PersistManifest(context.Background(), d.DB, routeManifest("com.reserved.route", "rkey", route), InstallOptions{})
			if err == nil {
				t.Fatalf("PersistManifest accepted a page entry on core-reserved route %q", route)
			}
			if !strings.Contains(err.Error(), route) || !strings.Contains(err.Error(), "core-reserved") {
				t.Fatalf("error should name the route and say core-reserved, got: %v", err)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.reserved.route'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
			}
		})
	}
}

func TestPersistManifest_AcceptsOrdinaryPageRoutes(t *testing.T) {
	for i, route := range []string{"/faq", "/plugin/docs", "/plugin/faq", "/apix", "/uix/z", "/helpdesk", "/plugin/x/y"} {
		d := openRealDB(t)
		id := "com.ordinary.route"
		if err := PersistManifest(context.Background(), d.DB, routeManifest(id, "okey", route), InstallOptions{}); err != nil {
			t.Fatalf("case %d: ordinary route %q must install: %v", i, route, err)
		}
	}
}

func TestReservedPageRoutePrefix(t *testing.T) {
	cases := []struct {
		route  string
		prefix string
		ok     bool
	}{
		{"/api", "/api", true},
		{"/api/x", "/api", true},
		{"/plugin-icons/a/b", "/plugin-icons", true},
		{"/self-order", "/self-order", true},
		{"/self-order/promo", "/self-order", true},
		{"/o/x/y", "/o", true},
		{"/themes/a/b", "/themes", true},
		{"/apix", "", false},
		{"/help/a/b", "/help", true},
		{"/orders", "/orders", true},
		{"/plugins/x/settings", "/plugins", true},
		{"/settings/vendor-x", "/settings", true},
		{"/catalog/zz", "/catalog", true},
		{"/users/zz", "/users", true},
		{"/open-orders/zz", "/open-orders", true},
		{"/recovery/zz", "/recovery", true},
		{"/settingsx", "", false},
		{"/plugin/faq", "", false},
		{"/helpdesk", "", false},
		{"/o-not-really", "", false},
		{"/API/x", "", false},
		{"/faq", "", false},
		{"/plugin/docs", "", false},
		{"/", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		p, ok := ReservedPageRoutePrefix(c.route)
		if p != c.prefix || ok != c.ok {
			t.Errorf("ReservedPageRoutePrefix(%q) = (%q, %v), want (%q, %v)", c.route, p, ok, c.prefix, c.ok)
		}
	}
}

// TestReservedPageRoutesCoverAuthExemptNamespaces pins the auth-exempt half
// of reservedPageRoutePrefixes (ut-docs#3786 review): a plugin page under a
// namespace auth.Middleware lets through without a session would render the
// POS chrome to anonymous customers (and on the self-order kiosk). Each
// probe is a child path no core pattern claims, so it would reach the "/"
// catch-all; if the real middleware exempts it, it must be reserved. A new
// exempt prefix needs its probe added here.
func TestReservedPageRoutesCoverAuthExemptNamespaces(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mw := auth.Middleware(next, nil)
	for _, ns := range []string{
		"/api", "/api/auth", "/api/self-order", "/api/sync", "/ui", "/v1", "/public", "/ext",
		"/plugin-icons", "/self-order", "/o", "/themes", "/plugin", "/help", "/orders",
		"/settings", "/setup", "/login", "/healthz", "/kitchen-display", "/journal", "/recovery",
	} {
		probe := ns + "/zz-plugin-probe/x"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, probe, nil))
		if rec.Code != http.StatusTeapot {
			continue // session-gated: not anonymous-reachable
		}
		if _, ok := ReservedPageRoutePrefix(probe); !ok {
			t.Errorf("%s is auth-exempt but not a reserved page route — add its namespace to reservedPageRoutePrefixes", probe)
		}
	}
}

// handlePatternRe matches a literal ServeMux pattern registered with
// Handle/HandleFunc, with or without a method: `mux.HandleFunc("GET /a/{b}", …)`.
var handlePatternRe = regexp.MustCompile(`\.Handle(?:Func)?\(\s*"(?:[A-Z]+ +)?(/[^"]*)"`)

// TestReservedPageRoutesCoverWildcardNamespaces pins reservedPageRoutePrefixes
// against core's real mux registrations (ut-docs#3791): a pattern with a
// {wildcard} child (GET /help/{topic}) leaves deeper paths (/help/a/b)
// unclaimed, so they fall through to the "/" catch-all that renders plugin
// pages — a plugin route there would shadow a core namespace. Every
// non-test file under internal/ is scanned, so a new core {wildcard} route
// fails here until its first segment is reserved.
//
// Literal patterns only (independent review, ut-docs#3791): a pattern built
// from a variable (`"POST "+discovery.ProofPath`) is invisible here. Today
// every such registration resolves under /api, already reserved — but a new
// one under a fresh root would not be caught, so check it by hand.
func TestReservedPageRoutesCoverWildcardNamespaces(t *testing.T) {
	root := ".." // internal/
	seen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range handlePatternRe.FindAllStringSubmatch(string(src), -1) {
			pattern := m[1]
			if !strings.Contains(pattern, "{") {
				continue
			}
			seg, _, _ := strings.Cut(strings.TrimPrefix(pattern, "/"), "/")
			if seg == "" || strings.HasPrefix(seg, "{") {
				continue // root-level wildcard (/{$}): no namespace to reserve
			}
			seen++
			probe := "/" + seg + "/zz-plugin-probe/x"
			if _, ok := ReservedPageRoutePrefix(probe); !ok {
				t.Errorf("%s registers %q, but /%s is not in reservedPageRoutePrefixes — a plugin page at %s would shadow it", path, pattern, seg, probe)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("found no {wildcard} mux patterns under internal/ — the scan is broken, not the list")
	}
}

// TestReservedPageRoutesCoverLiteralChildNamespaces is the literal sibling
// of TestReservedPageRoutesCoverWildcardNamespaces (ut-docs#3818): a root
// that registers only literal multi-segment children (/settings/menu) and
// an exact-match root (/settings) — no {wildcard}, no subtree "/settings/"
// — leaves every other child (/settings/vendor-x) unclaimed, so it falls
// through to the "/" catch-all that renders plugin pages inside core's
// namespace. Every non-test file under internal/ is scanned, so a new
// literal child under a fresh root fails here until that root is reserved.
//
// Skipped: patterns with { (the wildcard test's job); "/" and patterns
// ending in "/" (a ServeMux subtree match already claims everything below
// it); single-segment patterns (/admin — an exact core route beats the
// catch-all for its own path and claims nothing below). Same literal-only
// limit as the wildcard test: a pattern built from a variable is invisible.
func TestReservedPageRoutesCoverLiteralChildNamespaces(t *testing.T) {
	root := ".." // internal/
	seen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range handlePatternRe.FindAllStringSubmatch(string(src), -1) {
			pattern := m[1]
			if strings.Contains(pattern, "{") || strings.HasSuffix(pattern, "/") {
				continue
			}
			seg, rest, multi := strings.Cut(strings.TrimPrefix(pattern, "/"), "/")
			if seg == "" || !multi || rest == "" {
				continue // single-segment exact route: claims nothing below it
			}
			seen++
			probe := "/" + seg + "/zz-plugin-probe/x"
			if _, ok := ReservedPageRoutePrefix(probe); !ok {
				t.Errorf("%s registers %q, but /%s is not in reservedPageRoutePrefixes — a plugin page at %s would shadow it", path, pattern, seg, probe)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("found no literal multi-segment mux patterns under internal/ — the scan is broken, not the list")
	}
}
