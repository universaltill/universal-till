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
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

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

func marketplaceInstallTestServer(t *testing.T, artifact []byte, response map[string]any) *httptest.Server {
	t.Helper()
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
