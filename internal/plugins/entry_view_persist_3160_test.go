package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3160: a page entry's `view` (and `slot`) used to be validated but
// dropped at install. They are folded into config_json (no migration) and
// the view comes back on data.PageEntryRow.
func TestPersistManifest_PageEntryViewRoundtrip_3160(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m, err := ParseManifest(strings.NewReader(`{
		"id": "com.test.views", "name": "Views", "version": "1.0.0",
		"entrypoint": "./plugin.wasm", "runtime": "wasm",
		"permissions": ["ui:page"],
		"entries": [
			{"type": "page", "key": "home", "label": "Home", "route": "/plugin/views",
			 "view": "views.home", "slot": "reports.panels", "config": {"x": 1}},
			{"type": "page", "key": "plain", "label": "Plain", "route": "/plugin/views/plain"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := PersistManifest(context.Background(), db, m, InstallOptions{}); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
	rows, err := data.NewPluginRepo(db).ListPageEntries(context.Background())
	if err != nil {
		t.Fatalf("ListPageEntries: %v", err)
	}
	got := map[string]data.PageEntryRow{}
	for _, r := range rows {
		got[r.EntryKey] = r
	}
	if got["home"].View != "views.home" {
		t.Fatalf("home view = %q, want views.home (row %+v)", got["home"].View, got["home"])
	}
	if got["plain"].View != "" {
		t.Fatalf("plain entry must have no view, got %q", got["plain"].View)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(got["home"].ConfigJSON), &cfg); err != nil {
		t.Fatalf("config_json %q: %v", got["home"].ConfigJSON, err)
	}
	// "slot" is a layout entry's config key (uislot), so the content slot is
	// folded under its own key and can never collide with it.
	if cfg["content_slot"] != "reports.panels" || cfg["x"] != float64(1) {
		t.Fatalf("config_json lost view/slot/author keys: %s", got["home"].ConfigJSON)
	}
	if _, has := cfg["slot"]; has {
		t.Fatalf("content slot must not be folded under the layout key \"slot\": %s", got["home"].ConfigJSON)
	}
}
