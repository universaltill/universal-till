package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
)

// raceInstallAgainstRollback fires install at the rollbackAfterTargetStat
// hook of a Rollback of pluginID (installed at 2.0.0, snapshot 1.0.0) and
// fails if install finished while the rollback was still running, or if the
// install did not end up as the active version. Without the per-plugin lock
// in the install path, install replaces the live dir and persists its
// version inside the hook; the rollback then commits over it.
func raceInstallAgainstRollback(t *testing.T, db *sql.DB, base, pluginID, installVersion string, install func() error) {
	t.Helper()
	ctx := context.Background()
	rm := NewRollbackManager(db, base)

	writeVersionDir(t, base, pluginID, "1.0.0", true)
	seedInstalledPlugin(t, db, pluginID, "RACE", "2.0.0", "none", true)
	seedCatalogRow(t, db, pluginID, "RACE", "1.0.0", "")

	installDone := make(chan error, 1)
	finishedDuringRollback := false
	rollbackAfterTargetStat = func() {
		go func() { installDone <- install() }()
		// Give the install every chance to run before Rollback goes on;
		// holding the per-plugin lock, it blocks until Rollback returns.
		select {
		case err := <-installDone:
			finishedDuringRollback = true
			installDone <- err
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Cleanup(func() { rollbackAfterTargetStat = nil })

	if err := rm.Rollback(ctx, pluginID, "1.0.0", "tester"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := <-installDone; err != nil {
		t.Fatalf("concurrent install: %v", err)
	}
	if finishedDuringRollback {
		t.Fatalf("install of %s finished while a Rollback of it held the plugin tree; it must wait for the per-plugin lock", pluginID)
	}
	// The install ran last, so it wins.
	got, ok, err := data.NewPluginRepo(db).GetActivePluginVersion(ctx, pluginID)
	if err != nil || !ok {
		t.Fatalf("active version: ok=%v err=%v", ok, err)
	}
	if got != installVersion {
		t.Fatalf("active version = %q, want %q (the install that ran after the rollback)", got, installVersion)
	}
}

// ut-docs#3273: a manual import of plugin X that fires while a Rollback of
// X is between its target stat and its commit waits for the rollback.
func TestImport_WaitsForConcurrentRollback(t *testing.T) {
	imp, base := newTestImporter(t)
	pluginID := "com.test.importrace"
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	manifest := []byte(`{
		"id": "` + pluginID + `",
		"name": "Import Race",
		"version": "3.0.0",
		"entrypoint": "./run",
		"runtime": "none",
		"canonical_type": "page",
		"device_arch": "any"
	}`)
	writeZipArchive(t, archive, map[string][]byte{"manifest.json": manifest})

	raceInstallAgainstRollback(t, imp.db, base, pluginID, "3.0.0", func() error {
		_, err := imp.Import(context.Background(), &ImportRequest{
			FilePath:      archive,
			TrustLevel:    "untrusted",
			Uploader:      "tester",
			SkipSignature: true,
		})
		return err
	})
}

// ut-docs#3273: the same for a marketplace install (installBundleFile, shared
// by the direct and the staged download->install flows).
func TestMarketplaceInstall_WaitsForConcurrentRollback(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, checksum := signedMarketplaceArtifact(t, privateKey)

	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":               "tok-1",
			"bundle_url":          "",
			"release_id":          "release-1",
			"version":             manifest.Version,
			"checksum_sha256":     checksum,
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()

	db := managerTestDB(t)
	cfg := &config.Config{
		Marketplace: config.MarketplaceConfig{
			EndpointURL:       server.URL,
			APIVersion:        "1.0.0",
			ClientID:          "merchant-1",
			ClientSecret:      "secret-1",
			StoreID:           "store-1",
			DeviceID:          "device-1",
			PublicKey:         hex.EncodeToString(publicKey),
			RequestTimeoutSec: 30,
		},
	}
	installer := newTestMarketplaceInstaller(t, cfg, db)

	raceInstallAgainstRollback(t, db, installer.pluginBaseDir, manifest.ID, manifest.Version, func() error {
		_, err := installer.Install(context.Background(), MarketplaceInstallRequest{
			ListingID:  "listing-1",
			Version:    manifest.Version,
			TrustTier:  "verified",
			MerchantID: "merchant-1",
			StoreID:    "store-1",
			DeviceID:   "device-1",
			DeviceArch: "linux/amd64",
		})
		return err
	})
}
