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
// declared permissions except the AI write/tool ones and ai.send.* on a
// plugin without ai.agent; a permission the plugin already declared before
// is never granted, so an admin's revoke survives an update (ut-docs#4145).
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
		{"ordinary permission, already declared (revoke survives, #4145)", agent, "view:sales", true, false},
		{"ai.agent itself", agent, "ai.agent", false, true},
		{"ai.agent itself, already declared", agent, "ai.agent", true, false},
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
	if err := tryInstallAIManifest(t, db, publicKey, privateKey, version, perms); err != nil {
		t.Fatalf("Install %s: %v", version, err)
	}
}

func tryInstallAIManifest(t *testing.T, db *sql.DB, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey, version string, perms []string) error {
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
	_, err := newTestMarketplaceInstaller(t, cfg, db).Install(context.Background(), MarketplaceInstallRequest{
		ListingID: "listing-ai", Version: version, TrustTier: "verified", MerchantID: "merchant-1",
		StoreID: "store-1", DeviceID: "device-1", DeviceArch: "linux/amd64",
	})
	return err
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

// ut-docs#4145: a marketplace update keeps the operator's grant/revoke state
// for every permission the plugin already declared; only a permission the
// update declares for the first time is granted (per installGrantsPermission).
func TestMarketplaceInstall_UpdateKeepsRevokedPermissions(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	const id = "com.test.aiagent"

	installAIManifest(t, db, publicKey, privateKey, "1.0.0", []string{"view:sales", "storage"})
	if err := RevokePermission(context.Background(), db, id, "view:sales"); err != nil {
		t.Fatal(err)
	}

	installAIManifest(t, db, publicKey, privateKey, "1.0.1", []string{"view:sales", "storage", "events:receive"})
	got := grantedPerms(t, db, id)
	for perm, want := range map[string]bool{"view:sales": false, "storage": true, "events:receive": true} {
		if g, ok := got[perm]; !ok || g != want {
			t.Errorf("after update %s granted=%v (declared %v), want %v", perm, g, ok, want)
		}
	}

	// Re-installing the same version is an update too.
	installAIManifest(t, db, publicKey, privateKey, "1.0.1", []string{"view:sales", "storage", "events:receive"})
	if got := grantedPerms(t, db, id); got["view:sales"] {
		t.Errorf("reinstall of 1.0.1 re-granted the revoked view:sales")
	}
}

// ut-docs#4145 review M1: a fresh install that fails after PersistManifest
// must leave no permission rows, or the retry would see every permission as
// already declared and grant nothing.
func TestMarketplaceInstall_FailedFreshInstallLeavesNoRows(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	const id = "com.test.aiagent"

	// The catalog upsert after PersistManifest fails; PersistManifest's own
	// EnsureCatalogEntry (INSERT … DO NOTHING) is not an UPDATE, so it passes.
	if _, err := db.Exec(`CREATE TRIGGER fail_catalog_upsert BEFORE UPDATE ON plugin_catalog BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := tryInstallAIManifest(t, db, publicKey, privateKey, "1.0.0", []string{"view:sales"}); err == nil {
		t.Fatal("install with a failing catalog upsert succeeded")
	}
	if got := grantedPerms(t, db, id); len(got) != 0 {
		t.Fatalf("failed fresh install left permission rows %v", got)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_catalog_upsert`); err != nil {
		t.Fatal(err)
	}

	installAIManifest(t, db, publicKey, privateKey, "1.0.0", []string{"view:sales"})
	if got := grantedPerms(t, db, id); !got["view:sales"] {
		t.Fatalf("retry after a failed install did not grant view:sales: %v", got)
	}
}
