package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
)

// pluginsManagerTestDeps reuses pluginPageTestDeps' schema/deps. The real
// plugins table (from openPagesTestDB's real migrations, ut-docs#1657/#1677)
// already has the manager-page columns ListManagedPlugins reads
// (trust_level, install_state) -- this used to ALTER TABLE them in by hand.
func pluginsManagerTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	d, _ := pluginPageTestDeps(t)
	return d
}

// pluginsManagerJSON GETs /plugins and decodes the pluginsJSON payload the
// page embeds for Alpine (the <script id="plugins-data"> block) — the same
// data the per-plugin action buttons, including Docs, are driven by.
func pluginsManagerJSON(t *testing.T, mux *http.ServeMux) map[string]pluginsManagerItem {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/plugins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins = %d (%s)", rec.Code, body[:min(300, len(body))])
	}

	const open = `<script id="plugins-data" type="application/json">`
	start := strings.Index(body, open)
	if start < 0 {
		t.Fatalf("plugins page missing embedded plugins-data JSON, body=%s", body[:min(600, len(body))])
	}
	rest := body[start+len(open):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		t.Fatal("plugins-data script tag not closed")
	}

	var payload struct {
		Items []pluginsManagerItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(rest[:end]), &payload); err != nil {
		t.Fatalf("decode plugins-data JSON: %v (raw=%s)", err, rest[:end])
	}
	byID := make(map[string]pluginsManagerItem, len(payload.Items))
	for _, it := range payload.Items {
		byID[it.ID] = it
	}
	return byID
}

type pluginsManagerItem struct {
	ID             string `json:"id"`
	Enabled        bool   `json:"enabled"`
	DocsRoute      string `json:"docsRoute"`
	Latest         string `json:"latest"`
	HasUpdate      bool   `json:"hasUpdate"`
	VersionUnknown bool   `json:"versionUnknown"`
}

// An installed, enabled plugin with an active page entry using the reserved
// "docs" key (ADR-0037) exposes that entry's route as docsRoute, so the
// manager can show a Docs button pointing at the plugin's own in-till page.
func TestPluginsPage_DocsEntryExposesDocsRoute(t *testing.T) {
	d := pluginsManagerTestDeps(t)

	seedTestPlugin(t, d.Db, "com.x.tax", "UK VAT", "1.0.0")
	// A non-docs page entry must NOT be mistaken for documentation…
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.tax','page','dashboard','/plugin/tax-uk/dashboard','Dashboard')`); err != nil {
		t.Fatal(err)
	}
	// …only the reserved "docs" key is.
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e2','com.x.tax','page','docs','/plugin/tax-uk/docs','How this works')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.tax"]
	if !ok {
		t.Fatalf("plugin com.x.tax missing from manager payload: %+v", items)
	}
	if got.DocsRoute != "/plugin/tax-uk/docs" {
		t.Errorf("docsRoute = %q, want %q", got.DocsRoute, "/plugin/tax-uk/docs")
	}
}

// A plugin with no "docs" page entry (even if it has other page entries)
// gets an empty docsRoute — the template hides the Docs button, so there is
// never a button that opens an empty page.
func TestPluginsPage_NoDocsEntryMeansEmptyDocsRoute(t *testing.T) {
	d := pluginsManagerTestDeps(t)

	seedTestPlugin(t, d.Db, "com.x.other", "Other Plugin", "2.0.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.other','page','settings-page','/plugin/other/settings','Settings')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.other"]
	if !ok {
		t.Fatalf("plugin com.x.other missing from manager payload: %+v", items)
	}
	if got.DocsRoute != "" {
		t.Errorf("docsRoute = %q, want empty for a plugin with no docs entry", got.DocsRoute)
	}
}

// An INACTIVE "docs" entry must not surface a route: ListPageEntries filters
// inactive entries server-side (plugin_entries.is_active = 1), and this
// asserts the manager page actually inherits that filtering.
func TestPluginsPage_InactiveDocsEntryHidesDocsRoute(t *testing.T) {
	d := pluginsManagerTestDeps(t)

	seedTestPlugin(t, d.Db, "com.x.tax", "UK VAT", "1.0.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,is_active) VALUES('e1','com.x.tax','page','docs','/plugin/tax-uk/docs','How this works',0)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.tax"]
	if !ok {
		t.Fatalf("plugin com.x.tax missing from manager payload: %+v", items)
	}
	if got.DocsRoute != "" {
		t.Errorf("docsRoute = %q, want empty for an inactive docs entry", got.DocsRoute)
	}
}

// A DISABLED plugin's docs entry is likewise filtered out (plugins.is_active
// = 1 in ListPageEntries): the plugin still appears in the manager (so it
// can be re-enabled) but with no docs route, since its /plugin/... route
// would 404 while disabled.
func TestPluginsPage_DisabledPluginHidesDocsRoute(t *testing.T) {
	d := pluginsManagerTestDeps(t)

	seedTestPlugin(t, d.Db, "com.x.tax", "UK VAT", "1.0.0")
	if _, err := d.Db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = 'com.x.tax'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.tax','page','docs','/plugin/tax-uk/docs','How this works')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.tax"]
	if !ok {
		t.Fatalf("disabled plugin com.x.tax should still be listed by the manager: %+v", items)
	}
	if got.Enabled {
		t.Fatal("fixture plugin should be disabled")
	}
	if got.DocsRoute != "" {
		t.Errorf("docsRoute = %q, want empty for a disabled plugin", got.DocsRoute)
	}
}

// withCatalog points d.CatalogRepo at a fake marketplace server returning the
// given catalog body, mirroring plugins_store_render_test.go's own wiring.
func withCatalog(t *testing.T, d *common.Deps, catalogBody string) {
	t.Helper()
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(catalogBody))
	}))
	t.Cleanup(mp.Close)
	cfg := &config.MarketplaceConfig{EndpointURL: mp.URL}
	client := marketplace.NewClient(cfg, oauth.NewTokenClient(cfg))
	repo, err := marketplace.NewCatalogRepository(client, t.TempDir())
	if err != nil {
		t.Fatalf("catalog repo: %v", err)
	}
	d.CatalogRepo = repo
}

// ut-docs#2131 AC: a plugin installed via "Import from file" writes no
// plugin_install_status row (internal/data/sync_plugins_repo.go), so it can
// never resolve a catalog match through the listing mapping alone -- the
// management page used to leave latest empty forever for exactly this case,
// indistinguishable from "up to date". It must resolve via the same
// author+name fallback UpdateChecker already used.
func TestPluginsPage_FileImportedPluginResolvesUpdateViaAuthorName(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.faq", "FAQ Plugin", "1.0.0")
	if _, err := d.Db.Exec(`UPDATE plugins SET author = 'Acme Inc' WHERE id = 'com.x.faq'`); err != nil {
		t.Fatal(err)
	}
	// Deliberately no plugin_install_status row -- this is the file-import case.
	withCatalog(t, d, `{"plugins":[{"id":"listing-xyz","name":"FAQ Plugin","developer_id":"Acme Inc","version":"1.2.0"}]}`)

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.faq"]
	if !ok {
		t.Fatalf("plugin com.x.faq missing from manager payload: %+v", items)
	}
	if !got.HasUpdate {
		t.Errorf("hasUpdate = false, want true (catalog has 1.2.0 > installed 1.0.0)")
	}
	if got.Latest != "1.2.0" {
		t.Errorf("latest = %q, want %q", got.Latest, "1.2.0")
	}
	if got.VersionUnknown {
		t.Error("versionUnknown = true, want false: the author+name fallback found a real match")
	}
}

// ut-docs#2131 AC: a plugin absent from the resolved catalog data entirely
// must render as "unknown", never silently as "current" -- before this fix
// it was indistinguishable from up to date (empty latest, hasUpdate false,
// no UI signal either way).
func TestPluginsPage_UnresolvedPluginReportsVersionUnknownNotCurrent(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.ghost", "Ghost Plugin", "1.0.0")
	if _, err := d.Db.Exec(`UPDATE plugins SET author = 'Nobody' WHERE id = 'com.x.ghost'`); err != nil {
		t.Fatal(err)
	}
	// Catalog is reachable but has nothing matching this plugin by listing or
	// by author+name -- e.g. it was never published, or the snapshot's locale
	// filter excludes it.
	withCatalog(t, d, `{"plugins":[{"id":"listing-other","name":"Unrelated Plugin","developer_id":"Someone Else","version":"3.0.0"}]}`)

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.ghost"]
	if !ok {
		t.Fatalf("plugin com.x.ghost missing from manager payload: %+v", items)
	}
	if got.HasUpdate {
		t.Error("hasUpdate = true, want false: nothing in the catalog matched")
	}
	if got.Latest != "" {
		t.Errorf("latest = %q, want empty: no catalog match", got.Latest)
	}
	if !got.VersionUnknown {
		t.Error("versionUnknown = false, want true: an unresolved plugin must not look like it's current")
	}
}

// ut-docs#2131 review (Finding 2): a catalog match was found, but its
// Version is empty -- e.g. a listing the marketplace hasn't populated a
// version for yet. This must still render as "unknown", not "current":
// before this fix, hasUpdate stayed false (VersionNewer rejects an empty
// candidate) AND versionUnknown stayed false (a match was found), which is
// exactly the "latest:” presented as current" bug the issue is named
// after, reached by a second route than the one AC2's own regression test
// (above) covers.
func TestPluginsPage_MatchWithEmptyVersionReportsVersionUnknownNotCurrent(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.faq", "FAQ Plugin", "1.0.0")
	if _, err := d.Db.Exec(`UPDATE plugins SET author = 'Acme Inc' WHERE id = 'com.x.faq'`); err != nil {
		t.Fatal(err)
	}
	withCatalog(t, d, `{"plugins":[{"id":"listing-xyz","name":"FAQ Plugin","developer_id":"Acme Inc","version":""}]}`)

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	items := pluginsManagerJSON(t, mux)
	got, ok := items["com.x.faq"]
	if !ok {
		t.Fatalf("plugin com.x.faq missing from manager payload: %+v", items)
	}
	if got.HasUpdate {
		t.Error("hasUpdate = true, want false: an empty catalog version is never a real update")
	}
	if !got.VersionUnknown {
		t.Error("versionUnknown = false, want true: a match with an empty version must not look current")
	}
}
