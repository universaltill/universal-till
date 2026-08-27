package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// seedTaxRatePluginRows plants the DB rows for an installed wasm tax-rate
// plugin (registry, hook on tax.rate.ask, events:receive grant) — mirrors
// seedFiscalSignPluginRows (fiscal_sign_hook_test.go) field-for-field, just
// against the tax.rate.ask event instead of fiscal.sign.ask.
func seedTaxRatePluginRows(t *testing.T, dp *common.Deps, pluginID string, active bool) {
	t.Helper()
	ctx := context.Background()
	activeInt := 0
	if active {
		activeInt = 1
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', './plugin.wasm', 'wasm', ?, 'trusted')`,
		pluginID, "Tax Rate "+pluginID, activeInt); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES (?, ?, 'tax.rate.ask', 'tax.rate', 1)`, "hook-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_hooks: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES (?, ?, 'events:receive', 1)`, "perm-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_permissions: %v", err)
	}
}

// TestMissingMandatedTaxPlugin covers missingMandatedTaxPlugin's decision
// cases — mirrors TestMissingFiscalSigner's shape (fiscal_signer_banner_test.go)
// including the broken-but-installed case (ut-docs#368: install_state='broken'
// leaves is_active untouched, so ActiveHookOwner alone can't see it).
func TestMissingMandatedTaxPlugin(t *testing.T) {
	cases := []struct {
		name       string
		country    string
		seedPlugin bool
		seedBroken bool
		want       bool
	}{
		{name: "non-mandated country ignored regardless of installed plugins", country: "GB", seedPlugin: false, want: false},
		{name: "DE (mandated), no active tax.rate.ask plugin", country: "DE", seedPlugin: false, want: true},
		{name: "DE (mandated), an active plugin holds tax.rate.ask", country: "DE", seedPlugin: true, want: false},
		{name: "DE (mandated), active plugin is install_state=broken", country: "DE", seedPlugin: true, seedBroken: true, want: true},
		{name: "DE (mandated), no plugin, GB also has an unrelated active one — DE's own state still governs", country: "DE", seedPlugin: false, want: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, dp := newFiscalTestDeps(t)
			ctx := context.Background()
			if err := dp.Settings.Set(ctx, common.KeyCountry, c.country); err != nil {
				t.Fatal(err)
			}
			if c.seedPlugin {
				seedTaxRatePluginRows(t, dp, "taxplugin1", true)
			}
			if c.seedBroken {
				markPluginBroken(t, dp.Db, "taxplugin1")
			}

			got, err := missingMandatedTaxPlugin(ctx, dp, c.country)
			if err != nil {
				t.Fatalf("missingMandatedTaxPlugin: %v", err)
			}
			if got != c.want {
				t.Fatalf("missingMandatedTaxPlugin() = %v, want %v", got, c.want)
			}
		})
	}
}
