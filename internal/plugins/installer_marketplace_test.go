package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ackCapture is a concurrency-safe recorder for /v1/download/ack requests a
// test server receives. Needed because ackDownload (ut-docs#2381 review) is
// invoked via `go i.ackDownload(...)` — fire-and-forget from the caller's
// point of view — so a test asserting on captured acks must wait for the
// async call to actually land rather than reading a plain slice
// immediately after Install/DownloadToStore returns.
type ackCapture struct {
	mu   sync.Mutex
	reqs []marketplace.AckDownloadRequest
}

func (c *ackCapture) add(r marketplace.AckDownloadRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, r)
}

func (c *ackCapture) snapshot() []marketplace.AckDownloadRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]marketplace.AckDownloadRequest, len(c.reqs))
	copy(out, c.reqs)
	return out
}

// waitForCount polls (the ack lands over a real, if local, HTTP round trip
// on a goroutine this test doesn't control) until at least n acks have been
// captured, or fails the test after timeout.
func (c *ackCapture) waitForCount(t *testing.T, n int, timeout time.Duration) []marketplace.AckDownloadRequest {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		snap := c.snapshot()
		if len(snap) >= n {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d ack(s), got %d: %+v", n, len(snap), snap)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMarketplaceInstallerInstallSuccess(t *testing.T) {
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

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	result, err := installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if result.PluginID != manifest.ID {
		t.Fatalf("unexpected plugin id %q", result.PluginID)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugins WHERE id = ?`, manifest.ID).Scan(&count); err != nil {
		t.Fatalf("query installed plugin: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected installed plugin, got count=%d", count)
	}
}

// TestMarketplaceInstallerAcksSuccessfulDownload verifies ut-docs#2381's
// wiring: a successful Install reports the download outcome back to the
// marketplace via AckDownload, with the token/version the token endpoint
// issued and Success=true.
func TestMarketplaceInstallerAcksSuccessfulDownload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, checksum := signedMarketplaceArtifact(t, privateKey)

	acks := &ackCapture{}
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":               "tok-ack-1",
			"bundle_url":          "",
			"release_id":          "release-1",
			"version":             manifest.Version,
			"checksum_sha256":     checksum,
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	}, acks)
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	if _, err := installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	}); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	got := acks.waitForCount(t, 1, 2*time.Second)
	ack := got[0]
	if !ack.Success {
		t.Fatalf("expected Success=true, got %+v", ack)
	}
	if ack.Token != "tok-ack-1" {
		t.Fatalf("expected token %q, got %q", "tok-ack-1", ack.Token)
	}
	if ack.PluginID != "listing-1" {
		t.Fatalf("expected plugin_id %q, got %q", "listing-1", ack.PluginID)
	}
	if ack.Version != manifest.Version {
		t.Fatalf("expected version %q, got %q", manifest.Version, ack.Version)
	}
	if ack.FailureReason != "" {
		t.Fatalf("expected empty failure_reason on success, got %q", ack.FailureReason)
	}
}

// TestMarketplaceInstallerAcksFailedDownload verifies the failure half of
// ut-docs#2381: a Download error (here, a checksum mismatch, the same
// failure TestMarketplaceInstallerRejectsChecksumMismatch exercises) still
// reports the outcome back, with Success=false and the download error's
// message as FailureReason, before Install returns its own error.
func TestMarketplaceInstallerAcksFailedDownload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, _ := signedMarketplaceArtifact(t, privateKey)

	acks := &ackCapture{}
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":               "tok-ack-2",
			"bundle_url":          "",
			"release_id":          "release-1",
			"version":             manifest.Version,
			"checksum_sha256":     strings.Repeat("0", 64), // deliberately wrong
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	}, acks)
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	_, installErr := installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if installErr == nil {
		t.Fatal("expected Install to return a checksum error")
	}

	got := acks.waitForCount(t, 1, 2*time.Second)
	ack := got[0]
	if ack.Success {
		t.Fatalf("expected Success=false, got %+v", ack)
	}
	if ack.Token != "tok-ack-2" {
		t.Fatalf("expected token %q, got %q", "tok-ack-2", ack.Token)
	}
	// The ack's failure_reason is a COARSE, safe-to-transmit reason, never
	// Install's own raw error text (ut-docs#2381 review: the raw
	// DownloadManager error stringifies the full pre-signed bundle URL,
	// including its signature query parameters).
	if ack.FailureReason != "checksum_mismatch" {
		t.Fatalf("expected failure_reason %q, got %q (Install's own error was %q)", "checksum_mismatch", ack.FailureReason, installErr.Error())
	}
	if strings.Contains(ack.FailureReason, "http") || strings.Contains(ack.FailureReason, server.URL) {
		t.Fatalf("failure_reason must never leak the download URL, got %q", ack.FailureReason)
	}
}

// TestMarketplaceInstallerDoesNotAckBeforeDownloadAttempted verifies the
// scope boundary from ut-docs#2381: a failure that happens BEFORE
// DownloadManager.Download is even called (here, missing checksum/signature
// metadata in the token response) must not trigger an AckDownload call —
// there is no download attempt for the server to account for.
func TestMarketplaceInstallerDoesNotAckBeforeDownloadAttempted(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, _ := signedMarketplaceArtifact(t, privateKey)

	acks := &ackCapture{}
	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":      "tok-ack-3",
			"bundle_url": "",
			// checksum_sha256 deliberately omitted: fails the pre-Download
			// "download metadata is incomplete" validation in Install.
			"release_id":          "release-1",
			"version":             manifest.Version,
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	}, acks)
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	if _, err := installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	}); err == nil {
		t.Fatal("expected Install to reject incomplete download metadata")
	}

	// No goroutine is ever spawned on this path (Install returns before
	// reaching the `go i.ackDownload(...)` call site at all), so this is
	// deterministic without a wait — unlike the success/failure tests above.
	if got := acks.snapshot(); len(got) != 0 {
		t.Fatalf("expected no AckDownload call before Download is attempted, got %d: %+v", len(got), got)
	}
}

func TestMarketplaceInstallerRejectsMissingIntegrityMetadata(t *testing.T) {
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
			"signature":           "",
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	_, err = installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete metadata error, got %v", err)
	}
	assertPluginNotInstalled(t, db, manifest.ID)
}

func TestMarketplaceInstallerRejectsBadSignature(t *testing.T) {
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
			"signature":           strings.Repeat("ab", 64),
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	_, err = installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("expected signature mismatch error, got %v", err)
	}
	assertPluginNotInstalled(t, db, manifest.ID)
}

func TestMarketplaceInstallerRejectsChecksumMismatch(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, _ := signedMarketplaceArtifact(t, privateKey)

	server := marketplaceInstallTestServer(t, artifact, map[string]any{
		"data": map[string]any{
			"token":               "tok-1",
			"bundle_url":          "",
			"release_id":          "release-1",
			"version":             manifest.Version,
			"checksum_sha256":     strings.Repeat("0", 64),
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	_, err = installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	assertPluginNotInstalled(t, db, manifest.ID)
}

func TestMarketplaceInstallerRejectsTamperedBundlePayload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	_, manifest, _ := signedMarketplaceArtifact(t, privateKey)
	tamperedArtifact := []byte("this-is-not-a-valid-tar-gzip")
	tamperedSum := sha256.Sum256(tamperedArtifact)

	server := marketplaceInstallTestServer(t, tamperedArtifact, map[string]any{
		"data": map[string]any{
			"token":               "tok-1",
			"bundle_url":          "",
			"release_id":          "release-1",
			"version":             manifest.Version,
			"checksum_sha256":     hex.EncodeToString(tamperedSum[:]),
			"signature":           manifest.Signature,
			"expires_at":          "2026-03-16T16:00:00Z",
			"resumable_supported": true,
		},
		"error": nil,
	})
	defer server.Close()

	db := openMarketplaceInstallerDB(t)
	defer db.Close()

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
	_, err = installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "extract plugin bundle") {
		t.Fatalf("expected tampered payload extraction failure, got %v", err)
	}
	assertPluginNotInstalled(t, db, manifest.ID)
}

func TestMarketplaceInstallerNormalizesEntrypointFromExecutable(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	artifact, manifest, _ := signedMarketplaceArtifact(t, privateKey)
	manifest.Entrypoint = ""
	manifest.ArtifactHash = ""
	artifact = signedMarketplaceArtifactWithManifest(t, privateKey, manifest)

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
	if _, err := installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	}); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	var entrypoint string
	if err := db.QueryRowContext(context.Background(), `SELECT entrypoint FROM plugins WHERE id = ?`, manifest.ID).Scan(&entrypoint); err != nil {
		t.Fatalf("query installed entrypoint: %v", err)
	}
	if entrypoint != "./plugin-bin" {
		t.Fatalf("expected normalized entrypoint, got %q", entrypoint)
	}
}

func newTestMarketplaceInstaller(t *testing.T, cfg *config.Config, db *sql.DB) *MarketplaceInstaller {
	t.Helper()
	client := marketplace.NewClient(&cfg.Marketplace, &mockTokenClient{token: "test-token"})
	installer, err := NewMarketplaceInstaller(cfg, client, db)
	if err != nil {
		t.Fatalf("NewMarketplaceInstaller: %v", err)
	}
	root := t.TempDir()
	installer.pluginBaseDir = root
	installer.downloadTmpDir = filepath.Join(root, "tmp")
	if err := os.MkdirAll(installer.downloadTmpDir, 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	return installer
}

func openMarketplaceInstallerDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugins.db")
	database, err := appdb.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return database.DB
}

// marketplaceInstallTestServer serves the token-issue and bundle-download
// endpoints every installer test needs, plus a no-op /v1/download/ack (204,
// unconditionally — ut-docs#2381 wires every Install/DownloadToStore call to
// hit this). Pass ackLog to additionally capture each decoded ack request,
// in call order, for tests that assert on it; omit it (as every pre-existing
// caller does) to just let the ack succeed silently.
func marketplaceInstallTestServer(t *testing.T, artifact []byte, response map[string]any, ackLog ...*ackCapture) *httptest.Server {
	t.Helper()
	var log *ackCapture
	if len(ackLog) > 0 {
		log = ackLog[0]
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/downloads/tokens":
			resp := cloneResponseMap(response)
			if data, ok := resp["data"].(map[string]any); ok {
				data["bundle_url"] = server.URL + "/bundle.tar.gz"
				data["resumable_url"] = data["bundle_url"]
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && r.URL.Path == "/bundle.tar.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(artifact)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/download/ack":
			if log != nil {
				var req marketplace.AckDownloadRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				log.add(req)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func cloneResponseMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if nested, ok := v.(map[string]any); ok {
			copied := make(map[string]any, len(nested))
			for nk, nv := range nested {
				copied[nk] = nv
			}
			out[k] = copied
			continue
		}
		out[k] = v
	}
	return out
}

func signedMarketplaceArtifact(t *testing.T, privateKey ed25519.PrivateKey) ([]byte, *Manifest, string) {
	t.Helper()
	manifest := &Manifest{
		ID:            "com.test.marketplace",
		Name:          "Marketplace Plugin",
		Version:       "1.2.3",
		Entrypoint:    "./plugin-bin",
		Executable:    "plugin-bin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "linux/amd64",
	}
	artifact := signedMarketplaceArtifactWithManifest(t, privateKey, manifest)
	return artifact, manifest, checksumSHA256Hex(t, artifact)
}

func signedMarketplaceArtifactWithManifest(t *testing.T, privateKey ed25519.PrivateKey, manifest *Manifest) []byte {
	t.Helper()
	canonical := *manifest
	canonical.Signature = ""
	canonicalBytes, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal canonical manifest: %v", err)
	}
	manifest.Signature = hex.EncodeToString(ed25519.Sign(privateKey, canonicalBytes))

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	var archive bytes.Buffer
	gzWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzWriter)

	writeMarketplaceTarFile(t, tarWriter, "manifest.json", manifestBytes, 0o644)
	writeMarketplaceTarFile(t, tarWriter, "plugin-bin", []byte("binary"), 0o755)

	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	manifest.ArtifactHash = checksumSHA256Hex(t, archive.Bytes())
	return archive.Bytes()
}

func checksumSHA256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeMarketplaceTarFile(t *testing.T, tw *tar.Writer, name string, data []byte, mode int64) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write tar data: %v", err)
	}
}

func assertPluginNotInstalled(t *testing.T, db *sql.DB, pluginID string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugins WHERE id = ?`, pluginID).Scan(&count); err != nil {
		t.Fatalf("query plugins: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected plugin %s to remain uninstalled, got count=%d", pluginID, count)
	}
}

type mockTokenClient struct {
	token string
}

func (m *mockTokenClient) GetToken(ctx context.Context) (string, error) {
	return m.token, nil
}

func (m *mockTokenClient) ClearCache() error {
	return nil
}

// Found by the independent review of ut-docs#15 / logged as ut-docs#16:
// upsertCatalogEntry runs BEFORE PersistManifest, so a manifest that fails
// PersistManifest's own validation (here: a payment entry key colliding
// with the built-in cash tender, ADR-0031) still leaves its plugin_catalog
// row behind — PersistManifest's own EnsureCatalogEntry call is a
// same-transaction no-op since the row already exists (ON CONFLICT DO
// NOTHING), so it's never a substitute for it running first.
func TestMarketplaceInstallerLeavesNoCatalogRowOnRejectedInstall(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	manifest := &Manifest{
		ID:            "com.test.rejected",
		Name:          "Rejected Plugin",
		Version:       "1.0.0",
		Entrypoint:    "./plugin-bin",
		Executable:    "plugin-bin",
		Runtime:       "go",
		CanonicalType: "payment",
		DeviceArch:    "linux/amd64",
		Entries: []ManifestEntry{
			{Type: "payment", Key: "cash", Label: "Evil Cash"},
		},
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
	_, err = installer.Install(context.Background(), MarketplaceInstallRequest{
		ListingID:  "listing-1",
		Version:    manifest.Version,
		TrustTier:  "verified",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err == nil {
		t.Fatal("expected install to be rejected due to a payment key colliding with the built-in cash tender")
	}
	assertPluginNotInstalled(t, db, manifest.ID)

	var catalogCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugin_catalog WHERE id = ?`, manifest.ID).Scan(&catalogCount); err != nil {
		t.Fatalf("query plugin_catalog: %v", err)
	}
	if catalogCount != 0 {
		t.Fatalf("rejected install left an orphan plugin_catalog row behind, count=%d", catalogCount)
	}
}
