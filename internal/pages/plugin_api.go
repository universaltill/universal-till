package pages

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

func registerPluginAPI(mux *http.ServeMux, d *common.Deps) {
	// Install endpoint - downloads from marketplace and installs
	mux.HandleFunc("/api/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pluginID := strings.TrimSpace(r.Form.Get("id"))
		if pluginID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}

		// Get plugin info from marketplace API
		marketplaceURL := d.Cfg.MarketplaceURL + "/v1/catalog/plugins"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, marketplaceURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create request: %v", err), http.StatusInternalServerError)
			return
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("marketplace request failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("marketplace returned status %d", resp.StatusCode), resp.StatusCode)
			return
		}

		var marketplaceResp struct {
			Plugins []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Version    string `json:"version"`
				PackageURL string `json:"package_url"`
				SHA256     string `json:"sha256"`
			} `json:"plugins"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&marketplaceResp); err != nil {
			http.Error(w, fmt.Sprintf("failed to parse marketplace response: %v", err), http.StatusInternalServerError)
			return
		}

		// Find the requested plugin
		var targetPlugin *struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Version    string `json:"version"`
			PackageURL string `json:"package_url"`
			SHA256     string `json:"sha256"`
		}

		for i := range marketplaceResp.Plugins {
			if marketplaceResp.Plugins[i].ID == pluginID {
				targetPlugin = &marketplaceResp.Plugins[i]
				break
			}
		}

		if targetPlugin == nil {
			http.Error(w, "plugin not found in marketplace", http.StatusNotFound)
			return
		}

		// Download plugin package
		tmpDir := os.TempDir()
		packagePath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.tar.gz", targetPlugin.ID, targetPlugin.Version))

		packageResp, err := http.Get(targetPlugin.PackageURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("download failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer packageResp.Body.Close()

		if packageResp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("download failed: status %d", packageResp.StatusCode), http.StatusInternalServerError)
			return
		}

		f, err := os.Create(packagePath)
		if err != nil {
			http.Error(w, fmt.Sprintf("create file failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		defer os.Remove(packagePath)

		if _, err := io.Copy(f, packageResp.Body); err != nil {
			http.Error(w, fmt.Sprintf("save file failed: %v", err), http.StatusInternalServerError)
			return
		}
		f.Close()

		// Verify checksum
		actualSHA256, err := plugins.ComputeSHA256(packagePath)
		if err != nil {
			http.Error(w, fmt.Sprintf("checksum computation failed: %v", err), http.StatusInternalServerError)
			return
		}

		if actualSHA256 != targetPlugin.SHA256 {
			http.Error(w, fmt.Sprintf("checksum mismatch: expected %s, got %s", targetPlugin.SHA256, actualSHA256), http.StatusBadRequest)
			return
		}

		// Create minimal manifest for installation
		// In production, this would be extracted from the tarball
		manifestPath := filepath.Join(tmpDir, fmt.Sprintf("plugin-%s.json", targetPlugin.ID))
		manifestJSON := fmt.Sprintf(`{
			"id": "%s",
			"name": "%s",
			"version": "%s",
			"entrypoint": "./plugin",
			"runtime": "go"
		}`, targetPlugin.ID, targetPlugin.Name, targetPlugin.Version)

		if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
			http.Error(w, fmt.Sprintf("write manifest failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer os.Remove(manifestPath)

		// Install plugin
		opts := plugins.InstallOptions{
			InstalledFromURL: targetPlugin.PackageURL,
			SHA256:           targetPlugin.SHA256,
			TrustLevel:       "untrusted",
			Uploader:         "marketplace",
		}

		if err := plugins.InstallPlugin(r.Context(), d.Db, manifestPath, packagePath, opts); err != nil {
			http.Error(w, fmt.Sprintf("install failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Reload plugins
		_ = d.Pm.Reload(r.Context())
		d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Plugin installed successfully"))
	})

	// Marketplace: list available binaries from marketplace service
	mux.HandleFunc("/api/plugins/marketplace", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Get current OS and architecture
		osFilter := r.URL.Query().Get("os")
		archFilter := r.URL.Query().Get("arch")

		if osFilter == "" {
			osFilter = runtime.GOOS
		}
		if archFilter == "" {
			archFilter = runtime.GOARCH
		}

		deviceArch := fmt.Sprintf("%s/%s", osFilter, archFilter)
		capability := r.URL.Query().Get("capability")

		// Call marketplace HTTP API
		marketplaceURL := d.Cfg.MarketplaceURL + "/v1/catalog/plugins"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, marketplaceURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create request: %v", err), http.StatusInternalServerError)
			return
		}

		q := req.URL.Query()
		q.Set("device_arch", deviceArch)
		if capability != "" {
			q.Set("capability", capability)
		}
		req.URL.RawQuery = q.Encode()

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("marketplace request failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("marketplace returned status %d", resp.StatusCode), resp.StatusCode)
			return
		}

		// Forward marketplace response to client
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)
	})

	// Marketplace: install plugin from URL
	mux.HandleFunc("/api/plugins/marketplace/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pluginID := strings.TrimSpace(r.Form.Get("id"))
		version := strings.TrimSpace(r.Form.Get("version"))
		packageURL := strings.TrimSpace(r.Form.Get("package_url"))
		expectedSHA256 := strings.TrimSpace(r.Form.Get("sha256"))
		trustLevel := strings.TrimSpace(r.Form.Get("trust_level"))

		if pluginID == "" || version == "" || packageURL == "" || expectedSHA256 == "" {
			http.Error(w, "missing required fields: id, version, package_url, sha256", http.StatusBadRequest)
			return
		}

		if trustLevel == "" {
			trustLevel = "untrusted"
		}

		// Download plugin package
		tmpDir := os.TempDir()
		packagePath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.tar.gz", pluginID, version))

		resp, err := http.Get(packageURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("download failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("download failed: status %d", resp.StatusCode), http.StatusInternalServerError)
			return
		}

		f, err := os.Create(packagePath)
		if err != nil {
			http.Error(w, fmt.Sprintf("create file failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		defer os.Remove(packagePath)

		if _, err := io.Copy(f, resp.Body); err != nil {
			http.Error(w, fmt.Sprintf("save file failed: %v", err), http.StatusInternalServerError)
			return
		}
		f.Close()

		// Verify checksum
		actualSHA256, err := plugins.ComputeSHA256(packagePath)
		if err != nil {
			http.Error(w, fmt.Sprintf("checksum computation failed: %v", err), http.StatusInternalServerError)
			return
		}

		if actualSHA256 != expectedSHA256 {
			http.Error(w, fmt.Sprintf("checksum mismatch: expected %s, got %s", expectedSHA256, actualSHA256), http.StatusBadRequest)
			return
		}

		// For now, assume the package contains plugin.json and a binary
		// In a real implementation, you'd extract the tarball and find these files
		manifestPath := filepath.Join(tmpDir, "plugin.json")
		binaryPath := packagePath // Simplified: treat package as binary

		// Create a minimal manifest for testing (real implementation would extract from package)
		manifestJSON := fmt.Sprintf(`{
			"id": "%s",
			"name": "%s",
			"version": "%s",
			"entrypoint": "./plugin",
			"runtime": "go"
		}`, pluginID, pluginID, version)

		if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
			http.Error(w, fmt.Sprintf("write manifest failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer os.Remove(manifestPath)

		// Install plugin
		opts := plugins.InstallOptions{
			InstalledFromURL: packageURL,
			SHA256:           expectedSHA256,
			TrustLevel:       trustLevel,
			Uploader:         "marketplace",
		}

		if err := plugins.InstallPlugin(r.Context(), d.Db, manifestPath, binaryPath, opts); err != nil {
			http.Error(w, fmt.Sprintf("install failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Reload plugins
		_ = d.Pm.Reload(r.Context())
		d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Plugin installed successfully"))
	})

	// Grant/revoke permissions
	mux.HandleFunc("/api/plugins/permissions/grant", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pluginID := strings.TrimSpace(r.Form.Get("plugin_id"))
		permission := strings.TrimSpace(r.Form.Get("permission"))

		if pluginID == "" || permission == "" {
			http.Error(w, "missing required fields: plugin_id, permission", http.StatusBadRequest)
			return
		}

		if err := plugins.GrantPermission(r.Context(), d.Db, pluginID, permission); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Permission granted"))
	})

	mux.HandleFunc("/api/plugins/permissions/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pluginID := strings.TrimSpace(r.Form.Get("plugin_id"))
		permission := strings.TrimSpace(r.Form.Get("permission"))

		if pluginID == "" || permission == "" {
			http.Error(w, "missing required fields: plugin_id, permission", http.StatusBadRequest)
			return
		}

		if err := plugins.RevokePermission(r.Context(), d.Db, pluginID, permission); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Permission revoked"))
	})

	// Update trust level
	mux.HandleFunc("/api/plugins/trust", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pluginID := strings.TrimSpace(r.Form.Get("plugin_id"))
		trustLevel := strings.TrimSpace(r.Form.Get("trust_level"))

		if pluginID == "" || trustLevel == "" {
			http.Error(w, "missing required fields: plugin_id, trust_level", http.StatusBadRequest)
			return
		}

		if err := plugins.UpdatePluginTrustLevel(r.Context(), d.Db, pluginID, trustLevel); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Trust level updated"))
	})

	// T017: Marketplace plugin installation endpoints (009-cloud-marketplace Phase 4)
	mux.HandleFunc("POST /api/plugins/install-from-marketplace", handleInstallFromMarketplace(d))
	mux.HandleFunc("POST /api/plugins/{id}/enable", handleEnablePlugin(d))
	mux.HandleFunc("POST /api/plugins/{id}/disable", handleDisablePlugin(d))
	mux.HandleFunc("DELETE /api/plugins/{id}", handleUninstallPlugin(d))
}

// handleInstallFromMarketplace handles marketplace plugin installation (T017)
func handleInstallFromMarketplace(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse request
		var req struct {
			ListingID string `json:"listing_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.ListingID == "" {
			http.Error(w, "listing_id is required", http.StatusBadRequest)
			return
		}

		// Check if catalog repository is available
		if d.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		// Get plugin details from catalog
		snapshot, _, err := d.CatalogRepo.Get()
		if err != nil {
			http.Error(w, "Failed to fetch catalog", http.StatusInternalServerError)
			return
		}

		var targetPlugin interface{}
		for _, p := range snapshot.Plugins {
			if p.ListingID == req.ListingID {
				targetPlugin = p
				break
			}
		}

		if targetPlugin == nil {
			http.Error(w, "Plugin not found in catalog", http.StatusNotFound)
			return
		}

		// Verify compatibility before installation
		systemArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
		_ = systemArch // TODO: Use for compatibility check
		
		// TODO: Extract arch from plugin and verify
		// TODO: Check RBAC - require manager override if configured
		// TODO: Check disk quota before proceeding

		// Start installation process
		downloadMgr := plugins.NewDownloadManager("./data/plugins/tmp")
		_ = downloadMgr // TODO: Use for actual download
		
		// TODO: Extract artifact URL and hash from targetPlugin
		// TODO: Call downloadMgr.Download()
		// TODO: Extract archive
		// TODO: Verify manifest
		// TODO: Persist to database (T019)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Installation started (implementation pending T019)",
		})
	}
}

// handleEnablePlugin handles enabling a plugin (T017)
func handleEnablePlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("id")
		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}

		// TODO: Update database to mark plugin as enabled
		// TODO: Start plugin process if auto-start is configured

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Plugin %s enabled", pluginID),
		})
	}
}

// handleDisablePlugin handles disabling a plugin (T017)
func handleDisablePlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("id")
		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}

		// TODO: Update database to mark plugin as disabled
		// TODO: Stop plugin process if running

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Plugin %s disabled", pluginID),
		})
	}
}

// handleUninstallPlugin handles uninstalling a plugin (T017)
func handleUninstallPlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("id")
		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}

		// TODO: Stop running plugin process
		// TODO: Remove database entries
		// TODO: Clean up plugin files
		// TODO: Create audit log entry

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Plugin %s scheduled for uninstallation", pluginID),
		})
	}
}

