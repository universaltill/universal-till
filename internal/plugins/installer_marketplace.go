package plugins

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

type MarketplaceInstaller struct {
	client         *marketplace.Client
	db             *sql.DB
	pluginBaseDir  string
	downloadTmpDir string
	verifier       *ManifestVerifier
	reporter       *MarketplaceReporter
}

type MarketplaceInstallRequest struct {
	ListingID  string
	Version    string
	TrustTier  string
	MerchantID string
	StoreID    string
	DeviceID   string
	DeviceArch string
	// IntentID, when set, is the marketplace install-intent this install is
	// fulfilling; each state transition is reported back to it (story 1-1).
	IntentID      string
	OnStateChange func(InstallLifecycleState)
}

type MarketplaceInstallResult struct {
	PluginID string
	Name     string
	Version  string
}

func NewMarketplaceInstaller(cfg *config.Config, client *marketplace.Client, db *sql.DB) (*MarketplaceInstaller, error) {
	if cfg == nil {
		return nil, fmt.Errorf("marketplace config is required")
	}
	verifier, err := NewManifestVerifier(cfg.Marketplace.PublicKey)
	if err != nil {
		return nil, err
	}
	return &MarketplaceInstaller{
		client:         client,
		db:             db,
		pluginBaseDir:  paths.Plugins(),
		downloadTmpDir: paths.Plugins("tmp"),
		verifier:       verifier,
		reporter:       NewMarketplaceReporter(cfg.Marketplace.EndpointURL, cfg.Marketplace.UploadToken),
	}, nil
}

// emitState notifies the caller's OnStateChange callback and reports the state
// to the marketplace install-intent (when IntentID is set).
func (i *MarketplaceInstaller) emitState(ctx context.Context, req MarketplaceInstallRequest, state InstallLifecycleState, errDetail string) {
	if req.OnStateChange != nil {
		req.OnStateChange(state)
	}
	i.reporter.Report(ctx, req.IntentID, state, errDetail)
}

func (i *MarketplaceInstaller) Install(ctx context.Context, req MarketplaceInstallRequest) (result *MarketplaceInstallResult, err error) {
	if i == nil || i.client == nil || i.db == nil {
		return nil, fmt.Errorf("marketplace installer not configured")
	}
	// Report the terminal state to the marketplace intent: active on success,
	// failed (with the error) otherwise. i is non-nil past the guard above.
	defer func() {
		// Best-effort terminal report on a FRESH context: the install ctx may be
		// cancelled precisely when we most want the failed report to land (F1).
		// Report only to the marketplace here (not emitState) — the caller's
		// OnStateChange for terminal states is handled after Install returns with
		// complete data, so calling it here would double-write partial data (F2).
		rCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err != nil {
			i.reporter.Report(rCtx, req.IntentID, InstallStateFailed, err.Error())
		} else {
			i.reporter.Report(rCtx, req.IntentID, InstallStateActive, "")
		}
	}()
	if !i.verifier.HasPublicKey() {
		return nil, fmt.Errorf("marketplace public key not configured")
	}
	if strings.TrimSpace(req.ListingID) == "" {
		return nil, fmt.Errorf("listing_id is required")
	}
	if strings.TrimSpace(req.MerchantID) == "" {
		return nil, fmt.Errorf("merchant_id is required")
	}
	if strings.TrimSpace(req.StoreID) == "" {
		return nil, fmt.Errorf("store_id is required")
	}
	if strings.TrimSpace(req.DeviceID) == "" {
		return nil, fmt.Errorf("device_id is required")
	}
	if strings.TrimSpace(req.DeviceArch) == "" {
		req.DeviceArch = runtime.GOOS + "/" + runtime.GOARCH
	}
	i.emitState(ctx, req, InstallStateDownloading, "")

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

	i.emitState(ctx, req, InstallStateInstalling, "")

	return i.installBundleFile(ctx, bundleInstallSpec{
		BundlePath: downloadResult.FilePath,
		ListingID:  req.ListingID,
		Checksum:   tokenResp.ChecksumSHA256,
		Signature:  tokenResp.Signature,
		SourceURL:  tokenResp.BundleURL,
		TrustTier:  req.TrustTier,
		DeviceArch: req.DeviceArch,
	})
}

// bundleInstallSpec describes a verified-download bundle to install.
type bundleInstallSpec struct {
	BundlePath string // local .tar.gz path (checksum already verified)
	ListingID  string
	Checksum   string // marketplace-issued sha256 (hex)
	Signature  string // marketplace-issued manifest signature (hex)
	SourceURL  string
	TrustTier  string
	DeviceArch string
}

// installBundleFile extracts, verifies (signature, compatibility, executable)
// and installs a plugin bundle that was downloaded from the marketplace. It is
// shared by the direct install path and the store download->install flow.
func (i *MarketplaceInstaller) installBundleFile(ctx context.Context, spec bundleInstallSpec) (*MarketplaceInstallResult, error) {
	extractDir := filepath.Join(i.downloadTmpDir, "extract-"+sanitizePathSegment(spec.ListingID))
	if err := os.RemoveAll(extractDir); err != nil {
		return nil, fmt.Errorf("cleanup extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return nil, fmt.Errorf("create extract dir: %w", err)
	}
	if err := extractMarketplaceTarGz(spec.BundlePath, extractDir); err != nil {
		return nil, fmt.Errorf("extract plugin bundle: %w", err)
	}

	manifestPath := filepath.Join(extractDir, "manifest.json")
	verification, err := i.verifier.VerifyManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("verify manifest: %w", err)
	}
	manifest := verification.Manifest
	if manifest == nil {
		return nil, fmt.Errorf("verify manifest: manifest missing")
	}
	if manifest.Signature != spec.Signature {
		return nil, fmt.Errorf("signature mismatch between marketplace metadata and manifest")
	}
	if manifest.ArtifactHash != "" && normalizeMarketplaceChecksum(manifest.ArtifactHash) != spec.Checksum {
		return nil, fmt.Errorf("manifest artifact hash does not match marketplace checksum")
	}
	if err := i.verifier.VerifyCompatibility(manifest, spec.DeviceArch, "2.5.0"); err != nil {
		return nil, err
	}
	executable := manifest.Executable
	if executable == "" {
		executable = strings.TrimPrefix(manifest.Entrypoint, "./")
	}
	if manifest.Entrypoint == "" && executable != "" {
		manifest.Entrypoint = "./" + strings.TrimPrefix(executable, "./")
	}
	// Asset-only plugins (runtime "none", e.g. themes) ship no executable.
	// WASM modules must exist but are not native executables (no +x bit).
	switch manifest.Runtime {
	case "none":
	case "wasm":
		if _, err := os.Stat(filepath.Join(extractDir, executable)); err != nil {
			return nil, fmt.Errorf("wasm module not found: %s", executable)
		}
	default:
		if err := i.verifier.VerifyExecutable(extractDir, executable); err != nil {
			return nil, err
		}
	}

	finalDir := filepath.Join(i.pluginBaseDir, manifest.ID, manifest.Version)
	if err := os.RemoveAll(finalDir); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("clean existing plugin dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o755); err != nil {
		return nil, fmt.Errorf("create plugin parent dir: %w", err)
	}
	if err := os.Rename(extractDir, finalDir); err != nil {
		return nil, fmt.Errorf("move verified plugin into place: %w", err)
	}

	if err := i.upsertCatalogEntry(ctx, manifest, spec.SourceURL, spec.Checksum, spec.Signature); err != nil {
		_ = os.RemoveAll(finalDir)
		return nil, fmt.Errorf("persist plugin catalog entry: %w", err)
	}

	if err := PersistManifest(ctx, i.db, manifest, InstallOptions{
		InstalledFromURL: spec.SourceURL,
		SHA256:           spec.Checksum,
		TrustLevel:       marketplaceTrustTier(spec.TrustTier),
		Uploader:         "marketplace",
	}); err != nil {
		_ = os.RemoveAll(finalDir)
		return nil, fmt.Errorf("persist plugin manifest: %w", err)
	}

	// Marketplace installs are Ed25519-verified and their manifest (including
	// the permission set) passed marketplace review, so grant the declared
	// permissions. Menu entries and capabilities gate on granted permissions;
	// without this, installed pages never surface in the menu. Manual imports
	// stay ungranted for the operator to approve.
	for _, perm := range manifest.Permissions {
		if err := GrantPermission(ctx, i.db, manifest.ID, perm); err != nil {
			logging.L().Warnf("[MarketplaceInstaller] grant declared permission %s to %s: %v", perm, manifest.ID, err)
		}
	}

	return &MarketplaceInstallResult{
		PluginID: manifest.ID,
		Name:     manifest.Name,
		Version:  manifest.Version,
	}, nil
}

func extractMarketplaceTarGz(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return err
			}
			if err := outFile.Close(); err != nil {
				return err
			}
		}
	}
}

func normalizeMarketplaceChecksum(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
}

func sanitizePathSegment(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-")
	return replacer.Replace(value)
}

func marketplaceTrustTier(tier string) string {
	switch tier {
	case "verified", "approved":
		return "trusted"
	default:
		return "untrusted"
	}
}

func (i *MarketplaceInstaller) upsertCatalogEntry(ctx context.Context, manifest *Manifest, bundleURL, checksum, signature string) error {
	return data.NewPluginRepo(i.db).UpsertCatalogEntry(ctx, data.CatalogUpsertRow{
		ID:            manifest.ID,
		Version:       manifest.Version,
		Name:          manifest.Name,
		Description:   manifest.Description,
		Author:        manifest.Author,
		Website:       manifest.Website,
		Runtime:       manifest.Runtime,
		Entrypoint:    manifest.Entrypoint,
		PackageURL:    bundleURL,
		SHA256:        checksum,
		Signature:     signature,
		MinPOSVersion: minPOSVersion(manifest),
		PublishedAt:   time.Now().UTC().Format(time.RFC3339),
	})
}

func minPOSVersion(manifest *Manifest) string {
	if manifest != nil && manifest.MinPOSVersion != "" {
		return manifest.MinPOSVersion
	}
	return "2.5.0"
}
