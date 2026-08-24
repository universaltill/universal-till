package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func storeTestConfig(serverURL, publicKeyHex string) *config.Config {
	return &config.Config{
		Marketplace: config.MarketplaceConfig{
			EndpointURL:       serverURL,
			APIVersion:        "1.0.0",
			ClientID:          "merchant-1",
			ClientSecret:      "secret-1",
			StoreID:           "store-1",
			DeviceID:          "device-1",
			PublicKey:         publicKeyHex,
			RequestTimeoutSec: 30,
		},
	}
}

// TestStoreDownloadThenInstallLifecycle drives the REAL two-stage store flow:
// a signed bundle is downloaded (token issue + checksum-verified fetch) and
// staged on disk without installing anything, listed, then installed from the
// stage through the same Ed25519/compatibility/executable verification as a
// direct install, and finally the staged bundle is consumed.
func TestStoreDownloadThenInstallLifecycle(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	// arch "any": InstallFromStore verifies against the REAL host arch
	// (runtime.GOOS/GOARCH), so a pinned arch would only pass on one platform.
	manifest := &Manifest{
		ID:            "com.test.storeflow",
		Name:          "Store Flow Plugin",
		Version:       "2.0.0",
		Entrypoint:    "./plugin-bin",
		Executable:    "plugin-bin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "any",
	}
	artifact := signedMarketplaceArtifactWithManifest(t, privateKey, manifest)
	checksum := checksumSHA256Hex(t, artifact)

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

	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	cfg := storeTestConfig(server.URL, hex.EncodeToString(publicKey))
	installer := newTestMarketplaceInstaller(t, cfg, db)

	// Stage the download.
	sd, err := installer.DownloadToStore(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err != nil {
		t.Fatalf("DownloadToStore: %v", err)
	}
	if sd.Version != manifest.Version || sd.Checksum != checksum {
		t.Fatalf("unexpected staged record: %+v", sd)
	}
	if _, err := os.Stat(sd.BundlePath); err != nil {
		t.Fatalf("staged bundle missing: %v", err)
	}
	// Nothing installed yet.
	assertPluginNotInstalled(t, db, manifest.ID)

	// Staged download is listed and loadable.
	if got := installer.ListStoreDownloads(); len(got) != 1 || got["listing-1"].ListingID != "listing-1" {
		t.Fatalf("ListStoreDownloads = %+v", got)
	}
	loaded, err := installer.GetStoreDownload("listing-1")
	if err != nil {
		t.Fatalf("GetStoreDownload: %v", err)
	}
	if loaded.Signature != manifest.Signature {
		t.Fatalf("staged signature mismatch")
	}

	// Install from the stage — the real verification path.
	result, err := installer.InstallFromStore(context.Background(), "listing-1", "verified")
	if err != nil {
		t.Fatalf("InstallFromStore: %v", err)
	}
	if result.PluginID != manifest.ID || result.Version != manifest.Version {
		t.Fatalf("unexpected install result: %+v", result)
	}

	// Installed row exists, trust mapped from tier.
	var trust string
	if err := db.QueryRow(`SELECT trust_level FROM plugins WHERE id = ?`, manifest.ID).Scan(&trust); err != nil {
		t.Fatalf("query plugin: %v", err)
	}
	if trust != "trusted" {
		t.Fatalf("trust_level = %q, want trusted", trust)
	}

	// The staged bundle was consumed.
	if got := installer.ListStoreDownloads(); len(got) != 0 {
		t.Fatalf("staged download not consumed: %+v", got)
	}
	if _, err := installer.GetStoreDownload("listing-1"); err == nil {
		t.Fatalf("GetStoreDownload succeeded after consumption")
	}
}

func TestDownloadToStoreValidation(t *testing.T) {
	ctx := context.Background()

	var nilInstaller *MarketplaceInstaller
	if _, err := nilInstaller.DownloadToStore(ctx, MarketplaceInstallRequest{ListingID: "x"}); err == nil {
		t.Fatalf("nil installer accepted")
	}

	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)
	if _, err := installer.DownloadToStore(ctx, MarketplaceInstallRequest{}); err == nil || !strings.Contains(err.Error(), "listing_id is required") {
		t.Fatalf("empty listing accepted: %v", err)
	}
}

func TestDownloadToStoreRejectsIncompleteMetadata(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, checksum := signedMarketplaceArtifact(t, privateKey)

	// Token response missing the signature — must be rejected before any
	// bundle bytes are trusted.
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":           "tok-1",
			"bundle_url":      "",
			"version":         manifest.Version,
			"checksum_sha256": checksum,
			"signature":       "",
			"expires_at":      "2026-03-16T16:00:00Z",
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig(server.URL, hex.EncodeToString(publicKey)), db)

	_, err = installer.DownloadToStore(context.Background(), MarketplaceInstallRequest{
		ListingID: "listing-1", MerchantID: "m", StoreID: "s", DeviceID: "d",
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete metadata accepted: %v", err)
	}
}

func TestInstallFromStoreGuards(t *testing.T) {
	ctx := context.Background()

	var nilInstaller *MarketplaceInstaller
	if _, err := nilInstaller.InstallFromStore(ctx, "listing-1", "verified"); err == nil {
		t.Fatalf("nil installer accepted")
	}

	// No public key configured → refuse to install anything from the store.
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)
	if _, err := installer.InstallFromStore(ctx, "listing-1", "verified"); err == nil || !strings.Contains(err.Error(), "public key not configured") {
		t.Fatalf("keyless install allowed: %v", err)
	}

	// Key configured but nothing staged.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	installer2 := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", hex.EncodeToString(pub)), db)
	if _, err := installer2.InstallFromStore(ctx, "listing-missing", "verified"); err == nil || !strings.Contains(err.Error(), "no downloaded bundle") {
		t.Fatalf("missing stage accepted: %v", err)
	}
}

func TestGetStoreDownloadCorruptAndOrphanRecords(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)

	dir := installer.storeDownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Corrupt metadata JSON.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := installer.GetStoreDownload("bad"); err == nil || !strings.Contains(err.Error(), "decode download metadata") {
		t.Fatalf("corrupt metadata accepted: %v", err)
	}

	// Metadata present but bundle file missing (orphan).
	meta, _ := json.Marshal(StoreDownload{ListingID: "orphan", Version: "1.0.0"})
	if err := os.WriteFile(filepath.Join(dir, "orphan.json"), meta, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := installer.GetStoreDownload("orphan"); err == nil || !strings.Contains(err.Error(), "bundle missing") {
		t.Fatalf("orphan metadata accepted: %v", err)
	}

	// ListStoreDownloads skips both broken records plus stray entries.
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := installer.ListStoreDownloads(); len(got) != 0 {
		t.Fatalf("broken records listed: %+v", got)
	}

	// And an empty/missing downloads dir lists empty.
	installer2 := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)
	if got := installer2.ListStoreDownloads(); len(got) != 0 {
		t.Fatalf("expected empty list, got %+v", got)
	}
}

func TestDeleteStoreDownload(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)

	// Deleting a non-existent stage is a no-op, not an error.
	if err := installer.DeleteStoreDownload("nothing-there"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}

	bundle, meta := installer.storeDownloadPaths("listing-x")
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, p := range []string{bundle, meta} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := installer.DeleteStoreDownload("listing-x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, p := range []string{bundle, meta} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", p)
		}
	}

	// Path separators in listing IDs are sanitized, not treated as paths.
	b2, _ := installer.storeDownloadPaths("evil/../../listing")
	if !strings.HasPrefix(b2, installer.storeDownloadsDir()) {
		t.Fatalf("sanitized path escapes downloads dir: %s", b2)
	}
}

// TestSweepStoreDownloadsRemovesStaleFiles covers ut-docs#749: staged
// downloads accumulate unbounded disk usage with no automatic recovery. A
// stale pair (past storeDownloadTTL) must be swept; a fresh pair must not.
func TestSweepStoreDownloadsRemovesStaleFiles(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)

	dir := installer.storeDownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	staleBundle, staleMeta := installer.storeDownloadPaths("stale-listing")
	freshBundle, freshMeta := installer.storeDownloadPaths("fresh-listing")
	for _, p := range []string{staleBundle, staleMeta, freshBundle, freshMeta} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	old := time.Now().Add(-(storeDownloadTTL + time.Hour))
	if err := os.Chtimes(staleBundle, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chtimes(staleMeta, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	installer.sweepStoreDownloads()

	for _, p := range []string{staleBundle, staleMeta} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("stale file %s not swept", p)
		}
	}
	for _, p := range []string{freshBundle, freshMeta} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("fresh file %s wrongly swept: %v", p, err)
		}
	}
}

// TestSweepStoreDownloadsRemovesStaleOrphan covers a bundle left behind with
// no matching metadata (e.g. a crash between PromoteToPermanent and the
// metadata write) — it must still age out, not accumulate forever.
func TestSweepStoreDownloadsRemovesStaleOrphan(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)

	dir := installer.storeDownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	orphanBundle, _ := installer.storeDownloadPaths("orphan-listing")
	if err := os.WriteFile(orphanBundle, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	old := time.Now().Add(-(storeDownloadTTL + time.Hour))
	if err := os.Chtimes(orphanBundle, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	installer.sweepStoreDownloads()

	if _, err := os.Stat(orphanBundle); !os.IsNotExist(err) {
		t.Fatalf("stale orphan bundle not swept")
	}
}

// TestSweepStoreDownloadsIgnoresMissingDir mirrors ListStoreDownloads' own
// tolerance of a downloads dir that doesn't exist yet (fresh install).
func TestSweepStoreDownloadsIgnoresMissingDir(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)

	// No MkdirAll here — the dir genuinely doesn't exist.
	installer.sweepStoreDownloads() // must not panic or error
}

// TestListStoreDownloadsSweepsStaleEntries: a manager who just opens the
// plugin store page (no new download) still reaps abandoned stale stages,
// since ListStoreDownloads is on the read path hit far more often than a
// new download.
func TestListStoreDownloadsSweepsStaleEntries(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig("http://127.0.0.1:0", ""), db)

	dir := installer.storeDownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bundle, meta := installer.storeDownloadPaths("stale-listing")
	for _, p := range []string{bundle, meta} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	old := time.Now().Add(-(storeDownloadTTL + time.Hour))
	if err := os.Chtimes(bundle, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chtimes(meta, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if got := installer.ListStoreDownloads(); len(got) != 0 {
		t.Fatalf("expected stale entry swept before listing, got %+v", got)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("stale bundle not removed by ListStoreDownloads sweep")
	}
}

// TestDownloadToStoreSweepsBeforeDownloading confirms DownloadToStore reaps
// stale staged downloads from OTHER listings before doing its own network
// work, so repeated download-without-install activity across many listings
// doesn't grow disk usage unbounded.
func TestDownloadToStoreSweepsBeforeDownloading(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, checksum := signedMarketplaceArtifact(t, privateKey)
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":           "tok-1",
			"bundle_url":      "",
			"version":         manifest.Version,
			"checksum_sha256": checksum,
			"signature":       manifest.Signature,
			"expires_at":      "2026-03-16T16:00:00Z",
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig(server.URL, hex.EncodeToString(publicKey)), db)

	dir := installer.storeDownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	staleBundle, staleMeta := installer.storeDownloadPaths("abandoned-listing")
	for _, p := range []string{staleBundle, staleMeta} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	old := time.Now().Add(-(storeDownloadTTL + time.Hour))
	if err := os.Chtimes(staleBundle, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chtimes(staleMeta, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := installer.DownloadToStore(context.Background(), MarketplaceInstallRequest{
		ListingID: "listing-1", MerchantID: "m", StoreID: "s", DeviceID: "d",
	}); err != nil {
		t.Fatalf("DownloadToStore: %v", err)
	}

	for _, p := range []string{staleBundle, staleMeta} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("stale unrelated listing %s not swept by DownloadToStore", p)
		}
	}
}

// TestDownloadToStoreCleansUpOrphanBundleOnMetadataWriteFailure covers the
// failure path BA/Architect flagged: PromoteToPermanent moves the bundle
// into place before the metadata write. If the metadata write fails, the
// just-promoted bundle must not be left behind as an immediate orphan.
func TestDownloadToStoreCleansUpOrphanBundleOnMetadataWriteFailure(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, checksum := signedMarketplaceArtifact(t, privateKey)
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":           "tok-1",
			"bundle_url":      "",
			"version":         manifest.Version,
			"checksum_sha256": checksum,
			"signature":       manifest.Signature,
			"expires_at":      "2026-03-16T16:00:00Z",
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()
	installer := newTestMarketplaceInstaller(t, storeTestConfig(server.URL, hex.EncodeToString(publicKey)), db)

	// Make the metadata write fail: pre-create the metadata path as a
	// directory, so os.WriteFile(metaPath, ...) errors.
	_, metaPath := installer.storeDownloadPaths("listing-1")
	if err := os.MkdirAll(metaPath, 0o755); err != nil {
		t.Fatalf("mkdir metaPath as dir: %v", err)
	}

	_, err = installer.DownloadToStore(context.Background(), MarketplaceInstallRequest{
		ListingID: "listing-1", MerchantID: "m", StoreID: "s", DeviceID: "d",
	})
	if err == nil {
		t.Fatalf("expected metadata write failure")
	}

	bundlePath, _ := installer.storeDownloadPaths("listing-1")
	if _, statErr := os.Stat(bundlePath); !os.IsNotExist(statErr) {
		t.Fatalf("orphaned bundle left behind after metadata write failure")
	}
}
