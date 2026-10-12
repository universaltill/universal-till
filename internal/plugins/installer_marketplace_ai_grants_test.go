package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ADR-0153 Decision 5 (ut-docs#2837): a marketplace install grants the
// declared permissions except the AI write/tool ones, ai.send.* on a
// plugin without ai.agent, and an ai.send.* the plugin already declared
// before (so an admin's revoke survives an update).
func TestInstallGrantsPermission(t *testing.T) {
	agent := &Manifest{Permissions: []string{"ai.agent", "ai.send.sales"}}
	tool := &Manifest{Permissions: []string{"ai.tool.provide", "ai.send.sales"}}
	cases := []struct {
		name     string
		m        *Manifest
		perm     string
		previous bool
		want     bool
	}{
		{"ordinary permission", agent, "view:sales", false, true},
		{"ordinary permission, re-declared (unchanged behaviour, #4145)", agent, "view:sales", true, true},
		{"ai.agent itself", agent, "ai.agent", false, true},
		{"ai.agent itself, re-declared", agent, "ai.agent", true, true},
		{"ai.send on an agent, first declaration", agent, "ai.send.sales", false, true},
		{"ai.send on an agent, already declared (revoke survives)", agent, "ai.send.sales", true, false},
		{"ai.send on a tool plugin", tool, "ai.send.sales", false, false},
		{"ai.send on a tool plugin, re-declared", tool, "ai.send.sales", true, false},
		{"ai.tools.write on an agent", agent, "ai.tools.write:catalog", false, false},
		{"ai.tools.write:plugins", agent, "ai.tools.write:plugins", false, false},
		{"ai.tool.provide", tool, "ai.tool.provide", false, false},
		{"ai.tool.provide.write", tool, "ai.tool.provide.write", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := installGrantsPermission(c.m, c.perm, c.previous); got != c.want {
				t.Fatalf("installGrantsPermission(%v, %q, %v) = %v, want %v", c.m.Permissions, c.perm, c.previous, got, c.want)
			}
		})
	}
}

func installAIManifest(t *testing.T, db *sql.DB, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey, version string, perms []string) {
	t.Helper()
	manifest := &Manifest{
		ID:            "com.test.aiagent",
		Name:          "AI Agent",
		Version:       version,
		Entrypoint:    "./plugin-bin",
		Executable:    "plugin-bin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "linux/amd64",
		Permissions:   perms,
	}
	artifact := signedMarketplaceArtifactWithManifest(t, privateKey, manifest)
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":               "tok-1",
			"bundle_url":          "",
			"release_id":          "release-" + version,
			"version":             version,
			"checksum_sha256":     checksumSHA256Hex(t, artifact),
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: server.URL, APIVersion: "1.0.0", ClientID: "merchant-1", ClientSecret: "secret-1",
		StoreID: "store-1", DeviceID: "device-1", PublicKey: hex.EncodeToString(publicKey), RequestTimeoutSec: 30,
	}}
	if _, err := newTestMarketplaceInstaller(t, cfg, db).Install(context.Background(), MarketplaceInstallRequest{
		ListingID: "listing-ai", Version: version, TrustTier: "verified", MerchantID: "merchant-1",
		StoreID: "store-1", DeviceID: "device-1", DeviceArch: "linux/amd64",
	}); err != nil {
		t.Fatalf("Install %s: %v", version, err)
	}
}

func grantedPerms(t *testing.T, db *sql.DB, pluginID string) map[string]bool {
	t.Helper()
	perms, err := ListPluginPermissions(context.Background(), db, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range perms {
		out[p.Name] = p.Granted
	}
	return out
}

func TestMarketplaceInstall_AIGrantsAndRevokeSurvivesUpdate(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	const id = "com.test.aiagent"

	installAIManifest(t, db, publicKey, privateKey, "1.0.0",
		[]string{"ai.agent", "view:sales", "ai.send.sales", "ai.tools.write:catalog"})
	got := grantedPerms(t, db, id)
	for perm, want := range map[string]bool{"ai.agent": true, "view:sales": true, "ai.send.sales": true, "ai.tools.write:catalog": false} {
		if g, ok := got[perm]; !ok || g != want {
			t.Errorf("after install %s granted=%v (declared %v), want %v", perm, g, ok, want)
		}
	}

	if err := RevokePermission(context.Background(), db, id, "ai.send.sales"); err != nil {
		t.Fatal(err)
	}

	installAIManifest(t, db, publicKey, privateKey, "1.0.1",
		[]string{"ai.agent", "view:sales", "ai.send.sales", "ai.send.staff", "ai.tools.write:catalog"})
	got = grantedPerms(t, db, id)
	for perm, want := range map[string]bool{"ai.send.sales": false, "ai.send.staff": true, "ai.tools.write:catalog": false, "ai.agent": true} {
		if g, ok := got[perm]; !ok || g != want {
			t.Errorf("after update %s granted=%v (declared %v), want %v", perm, g, ok, want)
		}
	}
}

func TestMarketplaceInstall_ToolPluginAISendInstallsUngranted(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installAIManifest(t, db, publicKey, privateKey, "1.0.0",
		[]string{"ai.tool.provide", "ai.tool.provide.write", "ai.send.sales", "storage"})
	got := grantedPerms(t, db, "com.test.aiagent")
	for perm, want := range map[string]bool{"ai.tool.provide": false, "ai.tool.provide.write": false, "ai.send.sales": false, "storage": true} {
		if g, ok := got[perm]; !ok || g != want {
			t.Errorf("%s granted=%v (declared %v), want %v", perm, g, ok, want)
		}
	}
}
