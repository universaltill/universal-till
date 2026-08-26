package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestMissingFiscalSigner covers missingFiscalSigner's decision cases,
// including the gate's own truthy parsing of system_of_record ("1"/"on"/
// mixed-case/padded, not just the literal "true") and a broken-but-active
// signer plugin (Settings-page banner detection only — never touches
// EvaluateGate, KeyTSEFailingSince, or override state, ADR-0048 untouched).
func TestMissingFiscalSigner(t *testing.T) {
	cases := []struct {
		name           string
		country        string
		systemOfRecord string // "" means "leave the key unset"
		seedSigner     bool
		seedBroken     bool // seed an active signer, then mark it install_state='broken'
		want           bool
	}{
		{
			name:           "non-DE country ignored regardless of other settings",
			country:        "GB",
			systemOfRecord: "true",
			seedSigner:     false,
			want:           false,
		},
		{
			name:           "DE but not system of record",
			country:        "DE",
			systemOfRecord: "false",
			seedSigner:     false,
			want:           false,
		},
		{
			name:           "DE, system of record, no active fiscal.sign.ask plugin",
			country:        "DE",
			systemOfRecord: "true",
			seedSigner:     false,
			want:           true,
		},
		{
			name:           "DE, system of record, an active plugin holds fiscal.sign.ask",
			country:        "DE",
			systemOfRecord: "true",
			seedSigner:     true,
			want:           false,
		},
		// BLOCKER 1 (independent review): system_of_record is written through
		// a generic raw key/value settings editor with no normalization, so
		// real stored values are not limited to the literal string "true" --
		// EvaluateGate's own boolSetting/parseBoolSetting (fiscal.go) accepts
		// "1"/"on" case-insensitively after trimming whitespace. This banner
		// must parse the same setting exactly the way the gate does, or a shop
		// the gate treats as system-of-record could silently keep the banner
		// hidden.
		{
			name:           "DE, system_of_record='1' (gate-truthy, not literal true)",
			country:        "DE",
			systemOfRecord: "1",
			seedSigner:     false,
			want:           true,
		},
		{
			name:           "DE, system_of_record='on' (gate-truthy, not literal true)",
			country:        "DE",
			systemOfRecord: "on",
			seedSigner:     false,
			want:           true,
		},
		{
			name:           "DE, system_of_record='True' (gate-truthy, mixed case)",
			country:        "DE",
			systemOfRecord: "True",
			seedSigner:     false,
			want:           true,
		},
		{
			name:           "DE, system_of_record=' true ' (gate-truthy, padded)",
			country:        "DE",
			systemOfRecord: " true ",
			seedSigner:     false,
			want:           true,
		},
		// BLOCKER 2 (independent review, ut-docs#368-shaped): an installed
		// signer plugin whose wasm binary is broken still has is_active=1 on
		// itself and its hook row -- UpdatePluginInstallState deliberately
		// leaves is_active untouched when WasmRuntime.Sync flips
		// install_state='broken'. ActiveHookOwner alone can't see this, so a
		// broken-but-installed signer must still surface the banner (the
		// highest-value case this card exists to catch: a shop that DID
		// install the plugin, but it isn't actually running).
		{
			name:           "DE, system of record, active signer plugin is install_state=broken",
			country:        "DE",
			systemOfRecord: "true",
			seedSigner:     true,
			seedBroken:     true,
			want:           true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, dp := newFiscalTestDeps(t)
			ctx := context.Background()
			if err := dp.Settings.Set(ctx, common.KeyCountry, c.country); err != nil {
				t.Fatal(err)
			}
			if c.systemOfRecord != "" {
				if err := dp.Settings.Set(ctx, fiscal.KeySystemOfRecord, c.systemOfRecord); err != nil {
					t.Fatal(err)
				}
			}
			if c.seedSigner {
				seedFiscalSignPluginRows(t, dp, "signer1", true)
			}
			if c.seedBroken {
				markPluginBroken(t, dp.Db, "signer1")
			}

			got, err := missingFiscalSigner(ctx, dp, c.country)
			if err != nil {
				t.Fatalf("missingFiscalSigner: %v", err)
			}
			if got != c.want {
				t.Fatalf("missingFiscalSigner() = %v, want %v", got, c.want)
			}
		})
	}
}
