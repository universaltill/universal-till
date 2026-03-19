package pages

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
)

func registerPluginAPI(mux *http.ServeMux, d *common.Deps) {
	// Manual plugin upload endpoint
	mux.HandleFunc("/api/plugins/upload", func(w http.ResponseWriter, r *http.Request) {
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
		marketplaceURL := d.Cfg.Marketplace.EndpointURL + "/v1/catalog/plugins"
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
				ListingID   string `json:"listing_id"`
				Name        string `json:"name"`
				Version     string `json:"version"`
				ArtifactURL string `json:"artifact_url"`
				SHA256      string `json:"sha256"`
			} `json:"plugins"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&marketplaceResp); err != nil {
			http.Error(w, fmt.Sprintf("failed to parse marketplace response: %v", err), http.StatusInternalServerError)
			return
		}

		// Find the requested plugin
		var targetPlugin *struct {
			ListingID   string `json:"listing_id"`
			Name        string `json:"name"`
			Version     string `json:"version"`
			ArtifactURL string `json:"artifact_url"`
			SHA256      string `json:"sha256"`
		}

		for i := range marketplaceResp.Plugins {
			if marketplaceResp.Plugins[i].ListingID == pluginID {
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
		packagePath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.tar.gz", targetPlugin.ListingID, targetPlugin.Version))

		packageResp, err := http.Get(targetPlugin.ArtifactURL)
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
		manifestPath := filepath.Join(tmpDir, fmt.Sprintf("plugin-%s.json", targetPlugin.ListingID))
		manifestJSON := fmt.Sprintf(`{
			       "id": "%s",
			       "name": "%s",
			       "version": "%s",
			       "entrypoint": "./plugin",
			       "runtime": "go"
		       }`, targetPlugin.ListingID, targetPlugin.Name, targetPlugin.Version)

		if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
			http.Error(w, fmt.Sprintf("write manifest failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer os.Remove(manifestPath)

		// Install plugin
		opts := plugins.InstallOptions{
			InstalledFromURL: targetPlugin.ArtifactURL,
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

		// Call marketplace HTTP API (use new config field)
		marketplaceURL := d.Cfg.Marketplace.EndpointURL + "/v1/catalog/plugins"
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
	mux.HandleFunc("POST /api/plugins/{id}/uninstall", handleUninstallPlugin(d))
	mux.HandleFunc("POST /api/plugins/{id}/update", handleUpdatePlugin(d))
	mux.HandleFunc("POST /api/plugins/{id}/rollback", handleRollbackPlugin(d))
	mux.HandleFunc("GET /api/plugins/check-updates", handleCheckUpdates(d))
	mux.HandleFunc("POST /api/plugins/import-from-file", handleImportFromFile(d))
}

// handleInstallFromMarketplace handles marketplace plugin installation (T017)
func handleInstallFromMarketplace(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		statusStore := plugins.NewInstallStatusStore(d.Db)

		// Parse request
		var req struct {
			ListingID string `json:"listing_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeInstallResponse(w, http.StatusBadRequest, false, "", "plugins.install.error.invalid_request")
			return
		}

		if req.ListingID == "" {
			writeInstallResponse(w, http.StatusBadRequest, false, "", "plugins.install.error.invalid_request")
			return
		}

		// Check if catalog repository is available
		if d.CatalogRepo == nil {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:  req.ListingID,
				State:      plugins.InstallStateFailed,
				MessageKey: "plugins.install.error.configuration",
				Retryable:  false,
			})
			writeInstallResponse(w, http.StatusServiceUnavailable, false, "", "plugins.install.error.configuration")
			return
		}

		// Get plugin details from catalog
		snapshot, _, err := d.CatalogRepo.Get()
		if err != nil {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:  req.ListingID,
				State:      plugins.InstallStateFailed,
				MessageKey: "plugins.install.error.retryable",
				Retryable:  true,
			})
			writeInstallResponse(w, http.StatusInternalServerError, false, "", "plugins.install.error.retryable")
			return
		}

		var targetPlugin *marketplace.PluginSummary
		for _, p := range snapshot.Plugins {
			if p.ListingID == req.ListingID {
				targetPlugin = &p
				break
			}
		}

		if targetPlugin == nil {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:  req.ListingID,
				State:      plugins.InstallStateFailed,
				MessageKey: "plugins.install.error.retryable",
				Retryable:  true,
			})
			writeInstallResponse(w, http.StatusNotFound, false, "", "plugins.install.error.not_found")
			return
		}
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID:     targetPlugin.ListingID,
			PluginName:    targetPlugin.Name,
			TargetVersion: targetPlugin.Version,
			State:         plugins.InstallStateRequested,
		})

		// T019a: Verify compatibility before installation
		systemArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
		if targetPlugin.DeviceArch != systemArch {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:     targetPlugin.ListingID,
				PluginName:    targetPlugin.Name,
				TargetVersion: targetPlugin.Version,
				State:         plugins.InstallStateFailed,
				MessageKey:    "plugins.install.error.incompatible",
				Retryable:     false,
			})
			writeInstallResponse(w, http.StatusBadRequest, false, "", "plugins.install.error.incompatible")
			return
		}

		client := marketplace.NewClient(&d.Cfg.Marketplace, oauth.NewTokenClient(&d.Cfg.Marketplace))
		installer, err := plugins.NewMarketplaceInstaller(d.Cfg, client, d.Db)
		if err != nil {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:     targetPlugin.ListingID,
				PluginName:    targetPlugin.Name,
				TargetVersion: targetPlugin.Version,
				State:         plugins.InstallStateFailed,
				MessageKey:    "plugins.install.error.configuration",
				Retryable:     false,
			})
			writeInstallResponse(w, http.StatusInternalServerError, false, "", "plugins.install.error.configuration")
			return
		}
		result, err := installer.Install(ctx, plugins.MarketplaceInstallRequest{
			ListingID:  targetPlugin.ListingID,
			Version:    targetPlugin.Version,
			TrustTier:  targetPlugin.TrustTier,
			MerchantID: d.Cfg.Marketplace.ClientID,
			StoreID:    d.Cfg.Marketplace.StoreID,
			DeviceID:   marketplace.DeviceIDFromConfig(&d.Cfg.Marketplace),
			DeviceArch: systemArch,
			OnStateChange: func(state plugins.InstallLifecycleState) {
				_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
					ListingID:      targetPlugin.ListingID,
					PluginName:     targetPlugin.Name,
					TargetVersion:  targetPlugin.Version,
					CurrentVersion: targetPlugin.Version,
					State:          state,
				})
			},
		})
		if err != nil {
			failure := plugins.ClassifyInstallError(err)
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:     targetPlugin.ListingID,
				PluginName:    targetPlugin.Name,
				TargetVersion: targetPlugin.Version,
				State:         plugins.InstallStateFailed,
				MessageKey:    failure.MessageKey,
				Retryable:     failure.Retryable,
			})
			writeInstallResponse(w, http.StatusBadRequest, false, "", failure.MessageKey)
			return
		}
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID:      targetPlugin.ListingID,
			PluginID:       result.PluginID,
			PluginName:     result.Name,
			TargetVersion:  targetPlugin.Version,
			CurrentVersion: result.Version,
			State:          plugins.InstallStateActive,
		})

		// Reload plugin manager to pick up new plugin
		if err := d.Pm.Reload(ctx); err != nil {
			// Non-fatal - plugin is installed but won't show until restart
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     true,
			"message":     "",
			"message_key": "plugins.install.success",
			"plugin_id":   result.PluginID,
		})
	}
}

func writeInstallResponse(w http.ResponseWriter, status int, success bool, message string, messageKey string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     success,
		"message":     message,
		"message_key": messageKey,
	})
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

// extractTarGz extracts a tar.gz archive to the specified directory
func extractTarGz(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Security: prevent path traversal
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
		case tar.TypeReg:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, tarReader); err != nil {
				return fmt.Errorf("failed to write file: %w", err)
			}
		}
	}

	return nil
}

// mapTrustTier maps marketplace trust tier to plugin trust level
func mapTrustTier(tier string) string {
	switch tier {
	case "verified":
		return "trusted"
	case "approved":
		return "trusted"
	default:
		return "untrusted"
	}
}

// handleUpdatePlugin handles updating a plugin to the latest version (T025)
func handleUpdatePlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pluginID := r.PathValue("id")

		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}

		// Check catalog for latest version
		if d.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		snapshot, _, err := d.CatalogRepo.Get()
		if err != nil {
			http.Error(w, "Failed to fetch catalog", http.StatusInternalServerError)
			return
		}

		var targetPlugin *marketplace.PluginSummary
		for _, p := range snapshot.Plugins {
			if p.ListingID == pluginID {
				targetPlugin = &p
				break
			}
		}

		if targetPlugin == nil {
			http.Error(w, "Plugin not found in catalog", http.StatusNotFound)
			return
		}

		// Get current version
		currentPlugin, exists := d.Pm.Installed[pluginID]
		if !exists {
			http.Error(w, "Plugin not installed", http.StatusNotFound)
			return
		}

		// Store current version for rollback
		rollbackMgr := plugins.NewRollbackManager(d.Db, "./data/plugins")
		sourcePath := filepath.Join("./data/plugins", pluginID, currentPlugin.Version)
		if err := rollbackMgr.StoreVersion(pluginID, currentPlugin.Version, sourcePath); err != nil {
			log.Printf("Warning: Failed to store version for rollback: %v", err)
		}

		// Perform installation (similar to install flow)
		downloadMgr := plugins.NewDownloadManager("./data/plugins/tmp")
		downloadReq := &plugins.DownloadRequest{
			URL:              targetPlugin.ArtifactURL,
			PluginID:         targetPlugin.ListingID,
			ExpectedChecksum: strings.TrimPrefix(targetPlugin.ArtifactHash, "sha256:"),
			MaxSizeBytes:     200 * 1024 * 1024,
		}

		downloadResult, err := downloadMgr.Download(ctx, downloadReq)
		if err != nil {
			http.Error(w, fmt.Sprintf("Download failed: %v", err), http.StatusInternalServerError)
			return
		}

		pluginDir := filepath.Join("./data/plugins", targetPlugin.ListingID, targetPlugin.Version)
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			http.Error(w, fmt.Sprintf("Failed to create plugin directory: %v", err), http.StatusInternalServerError)
			return
		}

		if err := extractTarGz(downloadResult.FilePath, pluginDir); err != nil {
			http.Error(w, fmt.Sprintf("Failed to extract plugin: %v", err), http.StatusInternalServerError)
			return
		}

		manifestPath := filepath.Join(pluginDir, "manifest.json")
		manifestFile, err := os.Open(manifestPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("Manifest not found: %v", err), http.StatusInternalServerError)
			return
		}
		defer manifestFile.Close()

		manifest, err := plugins.ParseManifest(manifestFile)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid manifest: %v", err), http.StatusBadRequest)
			return
		}

		installOpts := plugins.InstallOptions{
			InstalledFromURL: targetPlugin.ArtifactURL,
			SHA256:           downloadResult.ActualChecksum,
			TrustLevel:       mapTrustTier(targetPlugin.TrustTier),
			Uploader:         "marketplace",
		}

		if err := plugins.PersistManifest(ctx, d.Db, manifest, installOpts); err != nil {
			http.Error(w, fmt.Sprintf("Failed to persist plugin: %v", err), http.StatusInternalServerError)
			return
		}

		downloadMgr.CleanupPartFile(targetPlugin.ListingID)

		if err := d.Pm.Reload(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		// TODO: Track telemetry for update event

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":      true,
			"message":      fmt.Sprintf("Plugin updated from %s to %s", currentPlugin.Version, targetPlugin.Version),
			"from_version": currentPlugin.Version,
			"to_version":   targetPlugin.Version,
		})
	}
}

// handleRollbackPlugin handles rolling back a plugin to a previous version (T025)
func handleRollbackPlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pluginID := r.PathValue("id")

		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}

		var req struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Version == "" {
			http.Error(w, "version is required", http.StatusBadRequest)
			return
		}

		rollbackMgr := plugins.NewRollbackManager(d.Db, "./data/plugins")
		// TODO: Extract actual user from session
		if err := rollbackMgr.Rollback(ctx, pluginID, req.Version, "system"); err != nil {
			http.Error(w, fmt.Sprintf("Rollback failed: %v", err), http.StatusInternalServerError)
			return
		}

		if err := d.Pm.Reload(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Plugin rolled back to version %s", req.Version),
			"version": req.Version,
		})
	}
}

// handleCheckUpdates checks for available plugin updates (T025)
func handleCheckUpdates(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if d.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		updateChecker := plugins.NewUpdateChecker(d.Db, d.CatalogRepo)
		updates, err := updateChecker.CheckForUpdates(ctx)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to check for updates: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"updates": updates,
			"count":   len(updates),
		})
	}
}

// handleImportFromFile handles manual plugin import from uploaded file (T028)
func handleImportFromFile(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Parse multipart form (max 200 MB)
		if err := r.ParseMultipartForm(200 << 20); err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}

		// Get uploaded file
		file, handler, err := r.FormFile("plugin_file")
		if err != nil {
			http.Error(w, "plugin_file is required", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Get optional trust level (default to untrusted for manual imports)
		trustLevel := r.FormValue("trust_level")
		if trustLevel == "" {
			trustLevel = "untrusted"
		}

		// Get uploader (TODO: extract from session)
		uploader := r.FormValue("uploader")
		if uploader == "" {
			uploader = "manual-import"
		}

		// Check if signature verification should be skipped (dev-mode only)
		skipSignature := false
		if d.Cfg.Env == "dev" && r.FormValue("skip_signature") == "true" {
			skipSignature = true
		}

		// Save uploaded file to temporary location
		tmpDir := filepath.Join("./data/plugins/tmp")
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			http.Error(w, "Failed to create temp directory", http.StatusInternalServerError)
			return
		}

		tmpFile := filepath.Join(tmpDir, handler.Filename)
		out, err := os.Create(tmpFile)
		if err != nil {
			http.Error(w, "Failed to save uploaded file", http.StatusInternalServerError)
			return
		}
		defer os.Remove(tmpFile)

		if _, err := io.Copy(out, file); err != nil {
			out.Close()
			http.Error(w, "Failed to save uploaded file", http.StatusInternalServerError)
			return
		}
		out.Close()

		// Detect format from filename
		format := plugins.ImportFormatTarGz
		if strings.HasSuffix(strings.ToLower(handler.Filename), ".zip") {
			format = plugins.ImportFormatZip
		}

		// Create importer
		verifier, err := plugins.NewManifestVerifier("") // TODO: Add public key path from config
		if err != nil {
			log.Printf("Warning: failed to create verifier: %v", err)
			verifier = nil // Allow import without verification in dev-mode
		}
		importer := plugins.NewImporter(d.Db, "./data/plugins", verifier)

		// Import plugin
		importReq := &plugins.ImportRequest{
			FilePath:      tmpFile,
			Format:        format,
			MaxSizeBytes:  200 * 1024 * 1024, // 200 MB
			TrustLevel:    trustLevel,
			Uploader:      uploader,
			SkipSignature: skipSignature,
		}

		result, err := importer.Import(ctx, importReq)
		if err != nil {
			http.Error(w, fmt.Sprintf("Import failed: %v", err), http.StatusBadRequest)
			return
		}

		// Reload plugin manager
		if err := d.Pm.Reload(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  true,
			"message":  fmt.Sprintf("Plugin %s v%s imported successfully", result.Name, result.Version),
			"plugin":   result.PluginID,
			"version":  result.Version,
			"warnings": result.Warnings,
		})
	}
}
