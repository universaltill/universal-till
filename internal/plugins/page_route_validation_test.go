package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
