package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#3329 through the real marketplace install path (download, extract,
// VerifyManifest, install), not a direct ParseManifest/validateABI3Fields
// call: a schedule event must start with the plugin's own manifest id, and
// an id may not start with a core event root.
func TestMarketplaceInstallerEnforcesEventNamespace(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		event   string
		wantErr string // "" = install must succeed
	}{
		{"own id prefix installs", "com.test.sched-own", "com.test.sched-own.retry.tick", ""},
		{"another plugin's id prefix refused", "com.test.sched-own", "com.other.plugin.retry.tick", `must start with this plugin's own id ("com.test.sched-own.")`},
		{"core event root id refused", "sale.sched", "sale.sched.retry.tick", `"sale" is a core event namespace`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate keypair: %v", err)
			}
			manifest := &Manifest{
				ID:            tc.id,
				Name:          "Schedule Plugin",
				Version:       "1.0.0",
				Entrypoint:    "./plugin-bin",
				Executable:    "plugin-bin",
				Runtime:       "go",
				CanonicalType: "page",
				DeviceArch:    "linux/amd64",
				Schedules:     []ManifestSchedule{{Event: tc.event, EveryS: 300}},
			}
			artifact := signedMarketplaceArtifactWithManifest(t, privateKey, manifest)
			server := marketplaceInstallTestServer(t, artifact, map[string]any{
				"data": map[string]any{
					"token":               "tok-1",
					"bundle_url":          "",
					"release_id":          "release-1",
					"version":             manifest.Version,
					"checksum_sha256":     checksumSHA256Hex(t, artifact),
					"signature":           manifest.Signature,
					"expires_at":          "2026-03-16T16:00:00Z",
					"resumable_supported": true,
				},
				"error": nil,
			})
			defer server.Close()

			db := openMarketplaceInstallerDB(t)
			defer db.Close()
			cfg := &config.Config{Marketplace: config.MarketplaceConfig{
				EndpointURL: server.URL, APIVersion: "1.0.0", ClientID: "merchant-1", ClientSecret: "secret-1",
				StoreID: "store-1", DeviceID: "device-1", PublicKey: hex.EncodeToString(publicKey), RequestTimeoutSec: 30,
			}}

			_, err = newTestMarketplaceInstaller(t, cfg, db).Install(context.Background(), MarketplaceInstallRequest{
				ListingID: "listing-1", Version: manifest.Version, TrustTier: "verified", MerchantID: "merchant-1",
				StoreID: "store-1", DeviceID: "device-1", DeviceArch: "linux/amd64",
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("own-id schedule install refused: %v", err)
				}
				var n int
				if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugins WHERE id = ?`, tc.id).Scan(&n); err != nil || n != 1 {
					t.Fatalf("plugin not installed: count=%d err=%v", n, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected install refused with %q, got %v", tc.wantErr, err)
			}
			assertPluginNotInstalled(t, db, tc.id)
		})
	}
}
