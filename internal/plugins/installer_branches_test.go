package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFileWithParents(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// installerForBundleTests builds an installer with a real verifier keyed to
// the given public key, without any marketplace server (installBundleFile
// never talks to the network).
func installerForBundleTests(t *testing.T, publicKey ed25519.PublicKey) *MarketplaceInstaller {
	t.Helper()
	db := openMarketplaceInstallerDB(t)
	t.Cleanup(func() { db.Close() })
	cfg := storeTestConfig("http://127.0.0.1:0", hex.EncodeToString(publicKey))
	return newTestMarketplaceInstaller(t, cfg, db)
}

func stageBundle(t *testing.T, installer *MarketplaceInstaller, listingID string, artifact []byte) string {
	t.Helper()
	bundlePath, _ := installer.storeDownloadPaths(listingID)
	if err := writeFileWithParents(bundlePath, artifact); err != nil {
		t.Fatalf("stage bundle: %v", err)
	}
	return bundlePath
}

func TestInstallBundleFileRejectsMetadataSignatureMismatch(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	installer := installerForBundleTests(t, publicKey)

	manifest := &Manifest{
		ID: "com.test.sigmm", Name: "SigMM", Version: "1.0.0",
		Entrypoint: "./plugin-bin", Executable: "plugin-bin",
		Runtime: "go", CanonicalType: "page", DeviceArch: "any",
	}
	artifact := signedMarketplaceArtifactWithManifest(t, privateKey, manifest)
	bundlePath := stageBundle(t, installer, "sigmm", artifact)

	// The manifest's own signature is valid, but the marketplace metadata
	// claims a DIFFERENT signature — must be rejected.
	_, err := installer.installBundleFile(context.Background(), bundleInstallSpec{
		BundlePath: bundlePath,
		ListingID:  "sigmm",
		Checksum:   checksumSHA256Hex(t, artifact),
		Signature:  strings.Repeat("ab", 64),
		SourceURL:  "https://x/bundle.tar.gz",
		TrustTier:  "verified",
		DeviceArch: "any",
	})
	if err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("metadata signature mismatch accepted: %v", err)
	}
}

func TestInstallBundleFileRejectsWrongArchAndMissingWasm(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)

	// Wrong architecture.
	installer := installerForBundleTests(t, publicKey)
	m1 := &Manifest{
		ID: "com.test.wrongarch", Name: "WrongArch", Version: "1.0.0",
		Entrypoint: "./plugin-bin", Executable: "plugin-bin",
		Runtime: "go", CanonicalType: "page", DeviceArch: "linux/s390x",
	}
	a1 := signedMarketplaceArtifactWithManifest(t, privateKey, m1)
	p1 := stageBundle(t, installer, "wrongarch", a1)
	_, err := installer.installBundleFile(context.Background(), bundleInstallSpec{
		BundlePath: p1, ListingID: "wrongarch",
		Checksum: checksumSHA256Hex(t, a1), Signature: m1.Signature,
		SourceURL: "https://x", TrustTier: "verified", DeviceArch: "linux/amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "incompatible architecture") {
		t.Fatalf("wrong arch accepted: %v", err)
	}

	// runtime "wasm" whose module file is absent from the bundle.
	installer2 := installerForBundleTests(t, publicKey)
	m2 := &Manifest{
		ID: "com.test.nowasm", Name: "NoWasm", Version: "1.0.0",
		Entrypoint: "./plugin.wasm",
		Runtime:    "wasm", CanonicalType: "page", DeviceArch: "any",
	}
	a2 := signedMarketplaceArtifactWithManifest(t, privateKey, m2)
	p2 := stageBundle(t, installer2, "nowasm", a2)
	_, err = installer2.installBundleFile(context.Background(), bundleInstallSpec{
		BundlePath: p2, ListingID: "nowasm",
		Checksum: checksumSHA256Hex(t, a2), Signature: m2.Signature,
		SourceURL: "https://x", TrustTier: "verified", DeviceArch: "any",
	})
	if err == nil || !strings.Contains(err.Error(), "wasm module not found") {
		t.Fatalf("missing wasm module accepted: %v", err)
	}
}

func TestInstallBundleFileRejectsCorruptArchive(t *testing.T) {
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	installer := installerForBundleTests(t, publicKey)
	p := stageBundle(t, installer, "corrupt", []byte("this is not a gzip stream"))
	_, err := installer.installBundleFile(context.Background(), bundleInstallSpec{
		BundlePath: p, ListingID: "corrupt",
		Checksum: "x", Signature: "y", SourceURL: "https://x",
		TrustTier: "verified", DeviceArch: "any",
	})
	if err == nil || !strings.Contains(err.Error(), "extract plugin bundle") {
		t.Fatalf("corrupt archive accepted: %v", err)
	}
}

// TestInstallEmitsLifecycleStates drives the full Install path and asserts the
// OnStateChange callback sees the downloading→installing progression
// (terminal states are the caller's concern, per the F2 note in Install).
func TestInstallEmitsLifecycleStates(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	manifest := &Manifest{
		ID: "com.test.states", Name: "States", Version: "1.0.0",
		Entrypoint: "./plugin-bin", Executable: "plugin-bin",
		Runtime: "go", CanonicalType: "page", DeviceArch: "any",
	}
	artifact := signedMarketplaceArtifactWithManifest(t, privateKey, manifest)
	checksum := checksumSHA256Hex(t, artifact)

	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token": "tok-1", "bundle_url": "", "release_id": "r1",
			"version": manifest.Version, "checksum_sha256": checksum,
			"signature": manifest.Signature, "expires_at": "2026-03-16T16:00:00Z",
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig(server.URL, hex.EncodeToString(publicKey)), db)

	var states []InstallLifecycleState
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = installer.Install(ctx, MarketplaceInstallRequest{
		ListingID: "listing-states", MerchantID: "m", StoreID: "s", DeviceID: "d",
		DeviceArch:    "any",
		TrustTier:     "verified",
		IntentID:      "intent-1", // exercises the reporter path (no-op client)
		OnStateChange: func(s InstallLifecycleState) { states = append(states, s) },
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(states) != 2 || states[0] != InstallStateDownloading || states[1] != InstallStateInstalling {
		t.Fatalf("states = %v", states)
	}
}
