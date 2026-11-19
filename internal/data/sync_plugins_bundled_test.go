package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Finding 17: the main till's plugin set carries its ENABLED bundled
// first-party plugins too (they have no marketplace install record), as rows
// with a blank listing id, which an older satellite skips and a current one
// applies. A disabled or non-system plugin is not carried.
func TestDumpActivePluginsWithSystem_CarriesEnabledBundledPlugins(t *testing.T) {
	db := openModifierTestDB(t).DB
	ctx := context.Background()
	for _, r := range []struct {
		id, trust string
		active    int
	}{
		{"com.universaltill.payment-stripe", "system", 1},
		{"com.universaltill.payment-sumup", "system", 0},
		{"com.example.other", "system", 1},
		{"com.universaltill.tax-uk", "trusted", 1},
	} {
		if _, err := db.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
			VALUES (?, '1.5.0', 'x', 'wasm', './p.wasm', 'file://x', 'x', '0.0.1', '1.0', datetime('now'))`, r.id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, trust_level, is_active) VALUES (?, ?, '1.5.0', './p.wasm', 'wasm', ?, ?)`,
			r.id, r.id, r.trust, r.active); err != nil {
			t.Fatal(err)
		}
	}
	repo := data.NewSyncPluginsRepo(db)
	base, err := repo.DumpActivePlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.DumpActivePluginsWithSystem(ctx, []string{"com.universaltill.payment-stripe", "com.universaltill.payment-sumup", "com.universaltill.tax-uk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Plugins) != len(base.Plugins)+1 {
		t.Fatalf("rows = %+v, want exactly the enabled system plugin of the list added", b.Plugins)
	}
	got := b.Plugins[len(b.Plugins)-1]
	if got.PluginID != "com.universaltill.payment-stripe" || got.ListingID != "" || got.Version != "1.5.0" {
		t.Fatalf("bundled row = %+v", got)
	}
	if b.Fingerprint() == base.Fingerprint() {
		t.Fatal("the fingerprint ignores the bundled state, so a satellite would never re-pull it")
	}
}
