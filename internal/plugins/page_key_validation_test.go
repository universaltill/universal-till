package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Install-time guard against ut-docs#472: internal/plugins.Manager.MenuPlugins
// (and the /ext/{key} dispatch it backs) is keyed by bare entry key, so two
// plugins registering a type:"page" entry under the same key silently
// overwrite each other with no error. Mirrors the payment-key collision fix
// (validatePaymentEntryKeys) at the same two call sites — PersistManifest and
// Rollback — except for the reserved "docs" key (ADR-0037), which is
// deliberately exempt: docs entries never enter MenuPlugins (loadMenuEntries
// skips DocsEntryKey) and the Docs-button feature looks them up per plugin ID
// (plugins_page.go's docsRouteByPlugin), so two plugins both using key:"docs"
// never actually collide in any live code path.

func pageManifest(id, key string) *Manifest {
	return pageManifestRoute(id, key, "/"+key)
}

// pageManifestRoute is pageManifest with an explicit route, for tests that
// need the key and route to vary independently — e.g. two plugins legally
// sharing the exempt DocsEntryKey (ADR-0037) must still each declare their
// own distinct route (ut-docs#499), the real-world shape "/plugin/<its-
// usual-route>" asks every docs-entry author to follow.
func pageManifestRoute(id, key, route string) *Manifest {
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

func TestPersistManifest_RejectsPageKeyOwnedByAnotherPlugin(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// Distinct routes (independent review finding, ut-docs#499): the two
	// plugins below share only the key, not the route, so it's the key
	// check under test here that rejects the second install — not the
	// separate route check, which would otherwise fire first (both plugins
	// would collide on route too if pageManifest's default "/"+key route
	// were left as-is, since they share the same key) and mask this test
	// against a regression in validatePageEntryKeys.
	if err := PersistManifest(ctx, d.DB, pageManifestRoute("com.first.page", "shared", "/plugin/first"), InstallOptions{}); err != nil {
		t.Fatalf("first plugin must install cleanly: %v", err)
	}
	err := PersistManifest(ctx, d.DB, pageManifestRoute("com.second.page", "shared", "/plugin/second"), InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a page entry key already owned by another plugin")
	}
	if !strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), "com.first.page") {
		t.Fatalf("collision error should name the key and its owner, got: %v", err)
	}
	// The rejected plugin must not be half-installed.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.page'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
	// The first plugin's entry must be untouched.
	var owner string
	if err := d.DB.QueryRow(`SELECT plugin_id FROM plugin_entries WHERE type='page' AND key='shared'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "com.first.page" {
		t.Fatalf("first plugin's page entry changed owner: %q", owner)
	}
	// Nothing catalog-side left behind either (ut-docs#16-class orphan row).
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_catalog WHERE id = 'com.second.page'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugin_catalog row(s), want 0", n)
	}
}

func TestPersistManifest_PageKeyFormatValidation(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, pageManifest("com.bad.page", ""), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty page key must be rejected with a clear message, got: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, pageManifest("com.bad.page2", " padded"), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("whitespace-padded page key must be rejected with a clear message, got: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, pageManifest("com.bad.page3", "ns:key"), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "':'") {
		t.Fatalf("page key containing ':' must be rejected with a clear message, got: %v", err)
	}
}

func TestPersistManifest_PageKeySelfUpgradeNotAConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, pageManifest("com.self.page", "mine"), InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	upgraded := pageManifest("com.self.page", "mine")
	upgraded.Version = "1.0.1"
	if err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{}); err != nil {
		t.Fatalf("reinstalling/upgrading the same plugin's own page key must not conflict with itself: %v", err)
	}
}

func TestPersistManifest_DocsKeyExemptAcrossPlugins(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// Distinct routes, matching ADR-0037's real convention
	// ("/plugin/<its-usual-route>") — the key exemption below is scoped to
	// MenuPlugins' key namespace only; the route namespace (ut-docs#499)
	// exempts nothing, so two docs entries must still use different routes
	// or this test would be asserting a collision the fix is required to
	// reject, not the key exemption it means to cover.
	if err := PersistManifest(ctx, d.DB, pageManifestRoute("com.first.docs", DocsEntryKey, "/plugin/first-docs"), InstallOptions{}); err != nil {
		t.Fatalf("first plugin's docs entry must install cleanly: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, pageManifestRoute("com.second.docs", DocsEntryKey, "/plugin/second-docs"), InstallOptions{}); err != nil {
		t.Fatalf("a second plugin's docs entry must not be rejected as a collision (ADR-0037 convention): %v", err)
	}
	// Both plugins really do have their own docs row — this isn't passing
	// because the second silently no-oped.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE type='page' AND key=?`, DocsEntryKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 distinct docs entries (one per plugin), got %d", n)
	}
}

func TestRollback_RejectsCollidingPageKeys(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()

	// Plugin currently at 2.0.0 with a clean key; its on-disk 1.0.0
	// manifest (the rollback target) declares key "shared" — legacy
	// versions predating validation can carry colliding keys, and rollback
	// writes entries without going through PersistManifest. Routes below
	// are all distinct (independent review finding, ut-docs#499) so the
	// key check under test here is what rejects the rollback, not the
	// separate route check.
	if err := PersistManifest(ctx, d.DB, pageManifestRoute("com.other.page", "shared", "/plugin/other"), InstallOptions{}); err != nil {
		t.Fatalf("install colliding owner: %v", err)
	}
	v2 := pageManifestRoute("com.rb.page", "rbpage", "/plugin/rb-current")
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.page', '1.0.0', 'RB Page', 'wasm', './main.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`)

	dir := filepath.Join(base, "com.rb.page", "versions", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"com.rb.page","name":"RB Page","version":"1.0.0","entrypoint":"./main.wasm","runtime":"wasm",
"entries":[{"type":"page","key":"shared","label":"Old Shared Page","route":"/plugin/rb-legacy"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(d.DB, base)
	err := rm.Rollback(ctx, "com.rb.page", "1.0.0", "tester")
	if err == nil {
		t.Fatal("rollback restored a page entry key colliding with another installed plugin")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Fatalf("rollback collision error should name the key, got: %v", err)
	}
	// The current version's entries must survive the failed rollback.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = 'com.rb.page' AND key = 'rbpage'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("failed rollback must leave current entries intact, found %d", n)
	}
}
