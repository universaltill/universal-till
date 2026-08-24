package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// storeDownloadTTL bounds how long a staged-but-never-installed download may
// sit under {pluginBaseDir}/downloads/ before sweepStoreDownloads reclaims
// it (ut-docs#749). Not config-surfaced — a fixed safety-net default, not a
// feature: a manager who repeatedly downloads-without-installing, or an
// install that fails after staging, would otherwise accumulate unbounded
// disk usage with no automatic recovery.
const storeDownloadTTL = 48 * time.Hour

// StoreDownload records a marketplace bundle downloaded to the till but not
// (yet) installed — the "downloaded" stage of the store lifecycle. The bundle
// and this metadata live under {pluginBaseDir}/downloads/.
type StoreDownload struct {
	ListingID    string    `json:"listing_id"`
	Version      string    `json:"version"`
	Checksum     string    `json:"checksum_sha256"`
	Signature    string    `json:"signature"`
	SourceURL    string    `json:"source_url"`
	BundlePath   string    `json:"bundle_path"`
	DownloadedAt time.Time `json:"downloaded_at"`
}

func (i *MarketplaceInstaller) storeDownloadsDir() string {
	return filepath.Join(i.pluginBaseDir, "downloads")
}

func (i *MarketplaceInstaller) storeDownloadPaths(listingID string) (bundle, meta string) {
	name := sanitizePathSegment(listingID)
	dir := i.storeDownloadsDir()
	return filepath.Join(dir, name+".tar.gz"), filepath.Join(dir, name+".json")
}

// sweepStoreDownloads removes any file directly under the store-downloads
// dir whose mtime is older than storeDownloadTTL. This is deliberately a
// single age-based pass with no separate orphan-detection branch: a bundle
// or metadata file left behind by a partial/failed write ages out on the
// same pass, since it has a real mtime too. Synchronous and called from the
// hot paths below rather than a background goroutine, so it's self-healing
// on next real use and stays simple to test (no timers/daemons).
func (i *MarketplaceInstaller) sweepStoreDownloads() {
	dir := i.storeDownloadsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-storeDownloadTTL)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			p := filepath.Join(dir, e.Name())
			if err := os.Remove(p); err != nil {
				logging.L().Warnf("[MarketplaceInstaller] sweep stale store download %s: %v", p, err)
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		logging.L().Infof("[MarketplaceInstaller] swept %d stale store download file(s) past %s", removed, storeDownloadTTL)
	}
}

// DownloadToStore fetches a listing's signed bundle (download token +
// checksum-verified download) and keeps it on disk for a later install —
// without installing anything.
func (i *MarketplaceInstaller) DownloadToStore(ctx context.Context, req MarketplaceInstallRequest) (*StoreDownload, error) {
	if i == nil || i.client == nil {
		return nil, fmt.Errorf("marketplace installer not configured")
	}
	if strings.TrimSpace(req.ListingID) == "" {
		return nil, fmt.Errorf("listing_id is required")
	}
	if strings.TrimSpace(req.DeviceArch) == "" {
		req.DeviceArch = runtime.GOOS + "/" + runtime.GOARCH
	}

	// Reclaim space from stale/abandoned stages before doing new network
	// work (ut-docs#749) — cheap local disk housekeeping, self-healing.
	i.sweepStoreDownloads()

	tokenResp, err := i.client.IssueDownloadToken(ctx, &marketplace.IssueDownloadTokenRequest{
		PluginID:   req.ListingID,
		Version:    req.Version,
		MerchantID: req.MerchantID,
		StoreID:    req.StoreID,
		DeviceID:   req.DeviceID,
		DeviceArch: req.DeviceArch,
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenResp.BundleURL) == "" || strings.TrimSpace(tokenResp.ChecksumSHA256) == "" || strings.TrimSpace(tokenResp.Signature) == "" {
		return nil, fmt.Errorf("marketplace download metadata is incomplete")
	}
	bundleURL, err := i.client.ResolveURL(tokenResp.BundleURL)
	if err != nil {
		return nil, err
	}

	downloadMgr := NewDownloadManager(i.downloadTmpDir)
	downloadResult, err := downloadMgr.Download(ctx, &DownloadRequest{
		URL:              bundleURL,
		PluginID:         req.ListingID,
		ExpectedChecksum: tokenResp.ChecksumSHA256,
		MaxSizeBytes:     200 * 1024 * 1024,
	})
	if err != nil {
		_ = downloadMgr.CleanupPartFile(req.ListingID)
		return nil, err
	}
	defer downloadMgr.CleanupPartFile(req.ListingID)

	bundlePath, metaPath := i.storeDownloadPaths(req.ListingID)
	if err := os.MkdirAll(i.storeDownloadsDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create downloads dir: %w", err)
	}
	if err := downloadMgr.PromoteToPermanent(downloadResult.FilePath, i.storeDownloadsDir(), filepath.Base(bundlePath)); err != nil {
		return nil, fmt.Errorf("keep downloaded bundle: %w", err)
	}

	sd := &StoreDownload{
		ListingID:    req.ListingID,
		Version:      tokenResp.Version,
		Checksum:     tokenResp.ChecksumSHA256,
		Signature:    tokenResp.Signature,
		SourceURL:    tokenResp.BundleURL,
		BundlePath:   bundlePath,
		DownloadedAt: time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		// Bundle is already promoted to disk — don't leave it (or any
		// stale metadata from a prior stage of this same listing) as an
		// immediate orphan waiting on the next TTL sweep.
		if delErr := i.DeleteStoreDownload(req.ListingID); delErr != nil {
			logging.L().Warnf("[MarketplaceInstaller] cleanup after metadata encode failure for %s: %v", req.ListingID, delErr)
		}
		return nil, fmt.Errorf("encode download metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, raw, 0o644); err != nil {
		if delErr := i.DeleteStoreDownload(req.ListingID); delErr != nil {
			logging.L().Warnf("[MarketplaceInstaller] cleanup after metadata write failure for %s: %v", req.ListingID, delErr)
		}
		return nil, fmt.Errorf("write download metadata: %w", err)
	}
	return sd, nil
}

// InstallFromStore installs a previously downloaded bundle: same signature /
// compatibility / executable verification as a direct marketplace install.
// The downloaded bundle is removed after a successful install.
func (i *MarketplaceInstaller) InstallFromStore(ctx context.Context, listingID, trustTier string) (*MarketplaceInstallResult, error) {
	if i == nil || i.db == nil {
		return nil, fmt.Errorf("marketplace installer not configured")
	}
	if !i.verifier.HasPublicKey() {
		return nil, fmt.Errorf("marketplace public key not configured")
	}
	sd, err := i.GetStoreDownload(listingID)
	if err != nil {
		return nil, err
	}

	result, err := i.installBundleFile(ctx, bundleInstallSpec{
		BundlePath: sd.BundlePath,
		ListingID:  sd.ListingID,
		Checksum:   sd.Checksum,
		Signature:  sd.Signature,
		SourceURL:  sd.SourceURL,
		TrustTier:  trustTier,
		DeviceArch: runtime.GOOS + "/" + runtime.GOARCH,
	})
	if err != nil {
		return nil, err
	}
	// The staged bundle served its purpose.
	_ = i.DeleteStoreDownload(listingID)
	return result, nil
}

// GetStoreDownload loads the download record for a listing.
func (i *MarketplaceInstaller) GetStoreDownload(listingID string) (*StoreDownload, error) {
	bundlePath, metaPath := i.storeDownloadPaths(listingID)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("no downloaded bundle for listing %s: %w", listingID, err)
	}
	var sd StoreDownload
	if err := json.Unmarshal(raw, &sd); err != nil {
		return nil, fmt.Errorf("decode download metadata: %w", err)
	}
	sd.BundlePath = bundlePath // trust our own path, not the stored one
	if _, err := os.Stat(bundlePath); err != nil {
		return nil, fmt.Errorf("downloaded bundle missing for listing %s: %w", listingID, err)
	}
	return &sd, nil
}

// DeleteStoreDownload removes a downloaded bundle and its metadata.
func (i *MarketplaceInstaller) DeleteStoreDownload(listingID string) error {
	bundlePath, metaPath := i.storeDownloadPaths(listingID)
	var firstErr error
	for _, p := range []string{bundlePath, metaPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListStoreDownloads returns all staged downloads keyed by listing ID.
func (i *MarketplaceInstaller) ListStoreDownloads() map[string]StoreDownload {
	// Reap stale/abandoned stages on the read path too (ut-docs#749) — a
	// manager opening the plugin-store page hits this far more often than
	// a new download, so it's the main place unbounded growth self-heals.
	i.sweepStoreDownloads()

	out := map[string]StoreDownload{}
	entries, err := os.ReadDir(i.storeDownloadsDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(i.storeDownloadsDir(), e.Name()))
		if err != nil {
			continue
		}
		var sd StoreDownload
		if err := json.Unmarshal(raw, &sd); err != nil || sd.ListingID == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(i.storeDownloadsDir(), strings.TrimSuffix(e.Name(), ".json")+".tar.gz")); err != nil {
			continue
		}
		out[sd.ListingID] = sd
	}
	return out
}
