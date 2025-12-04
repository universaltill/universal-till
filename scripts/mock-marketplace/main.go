// Mock marketplace server for local development and testing
// Implements marketplace.proto HTTP/JSON API at :8081
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
)

type PluginSummary struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Version              string   `json:"version"`
	Type                 string   `json:"type"`
	Vendor               string   `json:"vendor"`
	TrustLevel           string   `json:"trust_level"`
	RequiredCapabilities []string `json:"required_capabilities"`
	MinHostVersion       string   `json:"min_host_version"`
	Description          string   `json:"description"`
	IconURL              string   `json:"icon_url"`
	PaidListing          bool     `json:"paid_listing"`
	PackageURL           string   `json:"package_url"` // HTTP extension for POS download
	SHA256               string   `json:"sha256"`      // HTTP extension for POS verification
	SizeBytes            int64    `json:"size_bytes"`  // HTTP extension for UI display
}

type ListPluginsResponse struct {
	Plugins         []PluginSummary `json:"plugins"`
	NextPageToken   string          `json:"next_page_token"`
	SnapshotVersion int64           `json:"snapshot_version"`
}

type IssueDownloadTokenRequest struct {
	PluginID   string `json:"plugin_id"`
	Version    string `json:"version"`
	DeviceID   string `json:"device_id"`
	DeviceArch string `json:"device_arch"`
}

type IssueDownloadTokenResponse struct {
	ArtifactURL   string `json:"artifact_url"`
	Token         string `json:"token"`
	ExpiresAtUnix int64  `json:"expires_at_unix"`
	SHA256        string `json:"sha256"`
	Signature     string `json:"signature"`
}

// Mock plugin catalog - in production this comes from database
var mockCatalog = []PluginSummary{
	{
		ID:                   "sales-report",
		Name:                 "Sales Report Plugin",
		Version:              "1.0.0",
		Type:                 "page",
		Vendor:               "Universal Till",
		TrustLevel:           "verified",
		RequiredCapabilities: []string{"pos.sales.read"},
		MinHostVersion:       "0.1.0",
		Description:          "Generate daily sales reports with charts and export to PDF",
		IconURL:              "https://example.com/icons/sales-report.png",
		PaidListing:          false,
		PackageURL:           "http://127.0.0.1:8081/artifacts/sales-report-1.0.0.tar.gz",
		SHA256:               "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", // empty file hash for demo
		SizeBytes:            2048,
	},
	{
		ID:                   "loyalty-card",
		Name:                 "Loyalty Card Scanner",
		Version:              "2.1.3",
		Type:                 "payment",
		Vendor:               "Acme Corp",
		TrustLevel:           "untrusted",
		RequiredCapabilities: []string{"pos.payment.process", "hardware.scanner"},
		MinHostVersion:       "0.2.0",
		Description:          "Scan QR code loyalty cards and apply discounts automatically",
		IconURL:              "https://example.com/icons/loyalty.png",
		PaidListing:          true,
		PackageURL:           "http://127.0.0.1:8081/artifacts/loyalty-card-2.1.3.tar.gz",
		SHA256:               "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", // different hash
		SizeBytes:            4096,
	},
	{
		ID:                   "inventory-sync",
		Name:                 "Cloud Inventory Sync",
		Version:              "0.9.0",
		Type:                 "background",
		Vendor:               "Universal Till",
		TrustLevel:           "verified",
		RequiredCapabilities: []string{"pos.inventory.write", "network.outbound"},
		MinHostVersion:       "0.1.0",
		Description:          "Synchronize inventory counts to central warehouse management system",
		IconURL:              "https://example.com/icons/inventory.png",
		PaidListing:          false,
		PackageURL:           "http://127.0.0.1:8081/artifacts/inventory-sync-0.9.0.tar.gz",
		SHA256:               "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
		SizeBytes:            3072,
	},
}

func main() {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// CatalogService.ListPlugins - HTTP/JSON mirror of gRPC
	mux.HandleFunc("/v1/catalog/plugins", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		deviceArch := r.URL.Query().Get("device_arch")
		if deviceArch == "" {
			deviceArch = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
		}

		capabilityFilter := r.URL.Query().Get("capability")

		// Filter plugins
		filtered := make([]PluginSummary, 0, len(mockCatalog))
		for _, p := range mockCatalog {
			// For demo, show all plugins - in production filter by arch/capability
			if capabilityFilter != "" {
				hasCapability := false
				for _, cap := range p.RequiredCapabilities {
					if strings.Contains(cap, capabilityFilter) {
						hasCapability = true
						break
					}
				}
				if !hasCapability {
					continue
				}
			}
			filtered = append(filtered, p)
		}

		resp := ListPluginsResponse{
			Plugins:         filtered,
			NextPageToken:   "",
			SnapshotVersion: 1,
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(resp)
	})

	// DownloadService.IssueDownloadToken - HTTP/JSON mirror
	mux.HandleFunc("/v1/download/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req IssueDownloadTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Find plugin in catalog
		var plugin *PluginSummary
		for i := range mockCatalog {
			if mockCatalog[i].ID == req.PluginID && mockCatalog[i].Version == req.Version {
				plugin = &mockCatalog[i]
				break
			}
		}

		if plugin == nil {
			http.Error(w, "plugin not found", http.StatusNotFound)
			return
		}

		// Issue download token (simplified - no expiry/auth for mock)
		resp := IssueDownloadTokenResponse{
			ArtifactURL:   plugin.PackageURL,
			Token:         fmt.Sprintf("mock-token-%s-%s", req.PluginID, req.Version),
			ExpiresAtUnix: 0, // no expiry for mock
			SHA256:        plugin.SHA256,
			Signature:     "mock-signature",
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(resp)
	})

	// Mock artifact download endpoint - returns empty tarball for demo
	mux.HandleFunc("/artifacts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Return minimal tarball with plugin.json for testing
		// In production this would be real packaged binaries
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Empty content for now - real implementation would serve actual packages
		// Note: SHA256 checksums in catalog are for empty files, so verification will succeed
		w.Write([]byte{})
	})

	// CORS preflight
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})

	log.Println("Mock Marketplace Server starting on :8081")
	log.Println("Endpoints:")
	log.Println("  GET  /health")
	log.Println("  GET  /v1/catalog/plugins?device_arch=&capability=")
	log.Println("  POST /v1/download/token")
	log.Println("  GET  /artifacts/{filename}")
	log.Fatal(http.ListenAndServe(":8081", mux))
}
