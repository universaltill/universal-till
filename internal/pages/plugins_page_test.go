package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// pluginsManagerTestDeps reuses pluginPageTestDeps' schema/deps and widens
// the plugins table with the manager-page columns ListManagedPlugins reads
// (trust_level, install_state) that the plugin-page tests don't need.
func pluginsManagerTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	d, _ := pluginPageTestDeps(t)
	for _, s := range []string{
		`ALTER TABLE plugins ADD COLUMN trust_level TEXT NOT NULL DEFAULT 'trusted';`,
		`ALTER TABLE plugins ADD COLUMN install_state TEXT NOT NULL DEFAULT 'installed';`,
	} {
		if _, err := d.Db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
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
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
	DocsRoute string `json:"docsRoute"`
}

// An installed, enabled plugin with an active page entry using the reserved
// "docs" key (ADR-0037) exposes that entry's route as docsRoute, so the
// manager can show a Docs button pointing at the plugin's own in-till page.
func TestPluginsPage_DocsEntryExposesDocsRoute(t *testing.T) {
	d := pluginsManagerTestDeps(t)

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version) VALUES('com.x.tax','UK VAT','1.0.0')`); err != nil {
		t.Fatal(err)
	}
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

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version) VALUES('com.x.other','Other Plugin','2.0.0')`); err != nil {
		t.Fatal(err)
	}
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

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version) VALUES('com.x.tax','UK VAT','1.0.0')`); err != nil {
		t.Fatal(err)
	}
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

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.x.tax','UK VAT','1.0.0',0)`); err != nil {
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
