package data_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6666: the registry carries each marketplace listing's enabled state
// (plugins.is_active), and a disabled plugin is still listed.
func TestDumpActivePlugins_CarriesEnabledState(t *testing.T) {
	db := openModifierTestDB(t).DB
	ctx := context.Background()
	for _, r := range []struct {
		listing, id string
		active      int
	}{
		{"lst-a", "com.test.a", 1},
		{"lst-b", "com.test.b", 0},
	} {
		if _, err := db.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
			VALUES (?, '1.0.0', 'x', 'wasm', './p.wasm', 'file://x', 'x', '0.0.1', '1.0', datetime('now'))`, r.id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, ?, '1.0.0', './p.wasm', 'wasm', ?)`,
			r.id, r.id, r.active); err != nil {
			t.Fatal(err)
		}
		if err := plugins.NewInstallStatusStore(db).Save(ctx, plugins.InstallStatusRecord{
			ListingID: r.listing, PluginID: r.id, PluginName: r.id, CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
		}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := data.NewSyncPluginsRepo(db).DumpActivePlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Plugins) != 2 {
		t.Fatalf("rows = %+v, want both listings (a disabled plugin stays listed)", b.Plugins)
	}
	if e := b.Plugins[0].Enabled; e == nil || !*e {
		t.Fatalf("row a Enabled = %v, want &true", e)
	}
	if e := b.Plugins[1].Enabled; e == nil || *e {
		t.Fatalf("row b Enabled = %v, want &false", e)
	}
}

func TestPluginsBundle_FingerprintChangesWithEnabled(t *testing.T) {
	mk := func(v bool) data.PluginsBundle {
		return data.PluginsBundle{Plugins: []data.PluginSyncRow{{ListingID: "l", PluginID: "p", PluginName: "n", Version: "1", Enabled: &v}}}
	}
	if mk(true).Fingerprint() == mk(false).Fingerprint() {
		t.Fatal("flipping enabled must change the fingerprint")
	}
	if mk(true).Fingerprint() != mk(true).Fingerprint() {
		t.Fatal("equal state must hash equal")
	}
}

func TestPluginSyncRow_EnabledWireCompat(t *testing.T) {
	var row data.PluginSyncRow
	if err := json.Unmarshal([]byte(`{"listing_id":"l","plugin_id":"p","plugin_name":"n","version":"1"}`), &row); err != nil {
		t.Fatal(err)
	}
	if row.Enabled != nil {
		t.Fatalf("an old primary's row must decode Enabled=nil, got %v", *row.Enabled)
	}
	f := false
	raw, _ := json.Marshal(data.PluginSyncRow{ListingID: "l", Enabled: &f})
	if !strings.Contains(string(raw), `"enabled":false`) {
		t.Fatalf("false must be serialised: %s", raw)
	}
	raw, _ = json.Marshal(data.PluginSyncRow{ListingID: "l"})
	if strings.Contains(string(raw), "enabled") {
		t.Fatalf("nil must be omitted (bundled rows' bytes unchanged): %s", raw)
	}
}
