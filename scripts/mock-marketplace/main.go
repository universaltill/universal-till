// Mock marketplace server for local development and testing
// Implements marketplace.proto HTTP/JSON API at :8081
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
)

type PluginSummary struct {
	ListingID     string `json:"listing_id"`
	DeveloperID   string `json:"developer_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	CanonicalType string `json:"canonical_type"`
	TrustTier     string `json:"trust_tier"`
	ArtifactURL   string `json:"artifact_url"`
	ArtifactHash  string `json:"artifact_hash"`
	Version       string `json:"version"`
	Locale        string `json:"locale"`
	DeviceArch    string `json:"device_arch"`
	ApprovedAt    string `json:"approved_at"`
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

// Mock plugin catalog - matches official marketplace API structure
var mockCatalog = []PluginSummary{
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440001",
		DeveloperID:   "dev-universaltill",
		Name:          "Sales Report Plugin",
		Description:   "Generate daily sales reports with charts and export to PDF",
		CanonicalType: "report",
		TrustTier:     "verified",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/sales-report-1.0.0.tar.gz",
		ArtifactHash:  "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Version:       "1.0.0",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-12-01T10:00:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440002",
		DeveloperID:   "dev-acmecorp",
		Name:          "Loyalty Card Scanner",
		Description:   "Scan QR code loyalty cards and apply discounts automatically",
		CanonicalType: "payment",
		TrustTier:     "approved",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/loyalty-card-2.1.3.tar.gz",
		ArtifactHash:  "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
		Version:       "2.1.3",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-11-15T14:30:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440003",
		DeveloperID:   "dev-universaltill",
		Name:          "Cloud Inventory Sync",
		Description:   "Synchronize inventory counts to central warehouse management system",
		CanonicalType: "background_job",
		TrustTier:     "verified",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/inventory-sync-0.9.0.tar.gz",
		ArtifactHash:  "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
		Version:       "0.9.0",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-12-05T09:00:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440004",
		DeveloperID:   "dev-stripe",
		Name:          "Stripe Payment Gateway",
		Description:   "Accept credit card payments through Stripe with Apple Pay and Google Pay support",
		CanonicalType: "payment",
		TrustTier:     "verified",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/stripe-gateway-3.2.1.tar.gz",
		ArtifactHash:  "sha256:5d41402abc4b2a76b9719d911017c592e4e1b8e9e3e3c8b3c1f1e1d1c1b1a101",
		Version:       "3.2.1",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-11-20T16:45:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440005",
		DeveloperID:   "dev-quickbooks",
		Name:          "QuickBooks Integration",
		Description:   "Automatically sync sales, inventory, and taxes to QuickBooks Online",
		CanonicalType: "integration",
		TrustTier:     "verified",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/quickbooks-sync-1.5.0.tar.gz",
		ArtifactHash:  "sha256:7d793037a0760186574b0282f2f435e7ae6e1b9e1e1e8d8e8f8e8d8e8f8e8d01",
		Version:       "1.5.0",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-11-28T11:20:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440006",
		DeveloperID:   "dev-barcode-pro",
		Name:          "Advanced Barcode Scanner",
		Description:   "Support for all barcode types including QR, Data Matrix, PDF417, and Code 128",
		CanonicalType: "device",
		TrustTier:     "approved",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/barcode-scanner-2.0.5.tar.gz",
		ArtifactHash:  "sha256:8e959b75dae313da8cf4f72814fc143f6a7f6b9f9f9e9e9f9f9e9e9f9f9e9e02",
		Version:       "2.0.5",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-11-10T08:15:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440007",
		DeveloperID:   "dev-tax-wizard",
		Name:          "VAT Tax Calculator",
		Description:   "Automatic VAT calculation for EU countries with tax-free shopping support",
		CanonicalType: "page",
		TrustTier:     "approved",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/vat-calculator-1.2.0.tar.gz",
		ArtifactHash:  "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b2b2b2b0b2b2b2b0b2b03",
		Version:       "1.2.0",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-12-03T13:00:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440008",
		DeveloperID:   "dev-shopify",
		Name:          "Shopify Store Sync",
		Description:   "Sync products, orders, and inventory between POS and Shopify store",
		CanonicalType: "integration",
		TrustTier:     "verified",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/shopify-sync-2.3.0.tar.gz",
		ArtifactHash:  "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a104",
		Version:       "2.3.0",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-11-25T10:30:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440009",
		DeveloperID:   "dev-email-alerts",
		Name:          "Email Notification System",
		Description:   "Send email alerts for low stock, daily sales summaries, and failed transactions",
		CanonicalType: "background_job",
		TrustTier:     "approved",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/email-alerts-1.0.2.tar.gz",
		ArtifactHash:  "sha256:b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b205",
		Version:       "1.0.2",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-12-02T15:45:00Z",
	},
	{
		ListingID:     "550e8400-e29b-41d4-a716-446655440010",
		DeveloperID:   "dev-receipt-custom",
		Name:          "Custom Receipt Designer",
		Description:   "Design branded receipts with logo, custom messages, and promotional QR codes",
		CanonicalType: "page",
		TrustTier:     "approved",
		ArtifactURL:   "http://127.0.0.1:8082/artifacts/receipt-designer-1.1.0.tar.gz",
		ArtifactHash:  "sha256:c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c306",
		Version:       "1.1.0",
		Locale:        "en-US",
		DeviceArch:    "linux/amd64",
		ApprovedAt:    "2025-11-18T12:00:00Z",
	},
}

func main() {
	mux := http.NewServeMux()

	// OAuth2 token endpoint (simplified mock)
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Return a mock OAuth2 token response
		resp := map[string]interface{}{
			"access_token": "mock-access-token-12345",
			"token_type":   "Bearer",
			"expires_in":   3600, // 1 hour
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

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

		// Filter plugins by device_arch and locale if specified
		locale := r.URL.Query().Get("locale")

		filtered := make([]PluginSummary, 0, len(mockCatalog))
		for _, p := range mockCatalog {
			// For demo, show all plugins compatible with requested arch/locale
			if locale != "" && p.Locale != locale {
				continue
			}
			if deviceArch != "" && p.DeviceArch != deviceArch {
				continue
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
			if mockCatalog[i].ListingID == req.PluginID && mockCatalog[i].Version == req.Version {
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
			ArtifactURL:   plugin.ArtifactURL,
			Token:         fmt.Sprintf("mock-token-%s-%s", req.PluginID, req.Version),
			ExpiresAtUnix: 0, // no expiry for mock
			SHA256:        plugin.ArtifactHash,
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

	log.Println("Mock Marketplace Server")
	log.Println("Listening on: :8082 (production marketplace typically uses :8081)")
	log.Println("Endpoints:")
	log.Println("  POST /oauth/token (OAuth2 client credentials)")
	log.Println("  GET  /health")
	log.Println("  GET  /v1/catalog/plugins?device_arch=&capability=")
	log.Println("  POST /v1/download/token")
	log.Println("  GET  /artifacts/{filename}")
	log.Println("")

	// Wrap with logging middleware
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.URL.RawQuery)
		mux.ServeHTTP(w, r)
	})

	log.Fatal(http.ListenAndServe(":8082", handler))
}
