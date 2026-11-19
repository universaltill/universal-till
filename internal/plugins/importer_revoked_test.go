package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// A refused re-import of a revoked version that is still installed must
// leave the installed files intact (ut-docs#5658 review M1).
func TestImport_RevokedVersionRefusedBeforeTouchingFiles(t *testing.T) {
	imp, base := newTestImporter(t)
	const id = "com.test.importrev"
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	writeZipArchive(t, archive, map[string][]byte{
		"manifest.json": validImportManifest(id),
		"assets/a.txt":  []byte("hello"),
	})
	req := &ImportRequest{FilePath: archive, TrustLevel: "untrusted", Uploader: "tester", SkipSignature: true}
	if _, err := imp.Import(context.Background(), req); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	recordRevoked(t, imp.db, id, "1.0.0")

	_, err := imp.Import(context.Background(), req)
	if !errors.Is(err, ErrRevokedRelease) {
		t.Fatalf("want ErrRevokedRelease, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, id, "1.0.0", "assets", "a.txt")); err != nil {
		t.Fatalf("installed files were removed by the refused re-import: %v", err)
	}
}

func TestMarketplaceInstall_RevokedVersionRefusedBeforeTouchingFiles(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, checksum := signedMarketplaceArtifact(t, privateKey)
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token": "tok-1", "bundle_url": "", "release_id": "release-1",
			"version": manifest.Version, "checksum_sha256": checksum,
			"signature": manifest.Signature, "expires_at": "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: server.URL, APIVersion: "1.0.0", ClientID: "merchant-1",
		StoreID: "store-1", DeviceID: "device-1",
		PublicKey: hex.EncodeToString(publicKey), RequestTimeoutSec: 30,
	}}
	installer := newTestMarketplaceInstaller(t, cfg, db)
	req := MarketplaceInstallRequest{
		ListingID: "listing-1", Version: manifest.Version, TrustTier: "verified",
		MerchantID: "merchant-1", StoreID: "store-1", DeviceID: "device-1", DeviceArch: "linux/amd64",
	}
	if _, err := installer.Install(context.Background(), req); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	liveDir := filepath.Join(installer.pluginBaseDir, manifest.ID, manifest.Version)
	if _, err := os.Stat(liveDir); err != nil {
		t.Fatalf("installed dir missing: %v", err)
	}
	recordRevoked(t, db, manifest.ID, manifest.Version)

	_, err = installer.Install(context.Background(), req)
	if !errors.Is(err, ErrRevokedRelease) {
		t.Fatalf("want ErrRevokedRelease, got %v", err)
	}
	if _, err := os.Stat(liveDir); err != nil {
		t.Fatalf("installed dir removed by the refused re-install: %v", err)
	}
}
