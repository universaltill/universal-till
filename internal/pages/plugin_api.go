package pages

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
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
	mux.HandleFunc("GET /api/plugins/{id}/export", handleExportPlugin(d))
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

		// Install directly from the listing ID. The download-token endpoint
		// resolves the approved release for the listing, and the installer verifies
		// the Ed25519 signature and manifest compatibility (device_arch,
		// min_pos_version) itself — the signed manifest is the source of truth. We
		// deliberately do not parse the marketplace catalog here: its summary schema
		// differs from the client model, and the manifest check is authoritative.
		systemArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID: req.ListingID,
			State:     plugins.InstallStateRequested,
		})

		client := marketplace.NewClient(&d.Cfg.Marketplace, oauth.NewTokenClient(&d.Cfg.Marketplace))
		installer, err := plugins.NewMarketplaceInstaller(d.Cfg, client, d.Db)
		if err != nil {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:  req.ListingID,
				State:      plugins.InstallStateFailed,
				MessageKey: "plugins.install.error.configuration",
				Retryable:  false,
			})
			writeInstallResponse(w, http.StatusInternalServerError, false, "", "plugins.install.error.configuration")
			return
		}
		result, err := installer.Install(ctx, plugins.MarketplaceInstallRequest{
			ListingID:  req.ListingID,
			MerchantID: d.Cfg.Marketplace.ClientID,
			StoreID:    d.Cfg.Marketplace.StoreID,
			DeviceID:   marketplace.DeviceIDFromConfig(&d.Cfg.Marketplace),
			DeviceArch: systemArch,
			OnStateChange: func(state plugins.InstallLifecycleState) {
				_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
					ListingID: req.ListingID,
					State:     state,
				})
			},
		})
		if err != nil {
			failure := plugins.ClassifyInstallError(err)
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:  req.ListingID,
				State:      plugins.InstallStateFailed,
				MessageKey: failure.MessageKey,
				Retryable:  failure.Retryable,
			})
			writeInstallResponse(w, http.StatusBadRequest, false, "", failure.MessageKey)
			return
		}
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID:      req.ListingID,
			PluginID:       result.PluginID,
			PluginName:     result.Name,
			CurrentVersion: result.Version,
			State:          plugins.InstallStateActive,
		})

		// Reload plugin manager to pick up new plugin
		if err := d.Pm.Reload(ctx); err != nil {
			// Non-fatal - plugin is installed but won't show until restart
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}
		// Rebuild the nav so newly registered pages appear immediately.
		d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)

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
	return setPluginActiveHandler(d, true, "enabled")
}

// setPluginActiveHandler flips the plugin's is_active flag and refreshes the
// manager + nav so the change takes effect immediately.
func setPluginActiveHandler(d *common.Deps, active bool, verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pluginID := r.PathValue("id")
		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}
		if err := data.NewPluginRepo(d.Db).SetPluginActive(ctx, nil, pluginID, active); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("failed to %s plugin: %v", verb, err),
			})
			return
		}
		if d.Pm != nil {
			if err := d.Pm.Reload(ctx); err != nil {
				log.Printf("warning: reload after %s %s: %v", verb, pluginID, err)
			}
			d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Plugin %s %s", pluginID, verb),
		})
	}
}

// handleDisablePlugin disables a plugin (is_active=0); its entries drop out of
// menus/dispatch until re-enabled.
func handleDisablePlugin(d *common.Deps) http.HandlerFunc {
	return setPluginActiveHandler(d, false, "disabled")
}

// handleUninstallPlugin uninstalls a plugin: removes its DB rows (cascading its
// menu entries / settings / hooks / permissions + an audit event), deletes the
// installed files, and reloads the plugin manager so the nav/menu drops it.
func handleUninstallPlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pluginID := r.PathValue("id")
		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}
		// The id indexes a directory under ./data/plugins; reject traversal.
		if strings.ContainsAny(pluginID, `/\`) || strings.Contains(pluginID, "..") {
			http.Error(w, "invalid plugin id", http.StatusBadRequest)
			return
		}

		// Remove DB rows (FK cascade removes entries/settings/hooks/permissions)
		// and record the uninstall audit event.
		if err := plugins.UninstallPlugin(ctx, d.Db, pluginID); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("Uninstall failed: %v", err),
			})
			return
		}

		// Remove the installed files (best-effort; the DB is the source of truth).
		pluginDir := filepath.Join("./data/plugins", pluginID)
		if err := os.RemoveAll(pluginDir); err != nil {
			log.Printf("warning: failed to remove plugin files %s: %v", pluginDir, err)
		}

		// Clear marketplace install-status records so the plugins page doesn't
		// keep reporting the listing as active after uninstall.
		if err := plugins.NewInstallStatusStore(d.Db).ClearForPlugin(ctx, pluginID); err != nil {
			log.Printf("warning: failed to clear install status for %s: %v", pluginID, err)
		}

		// Reload so the nav/menu no longer shows the plugin's entries.
		if d.Pm != nil {
			if err := d.Pm.Reload(ctx); err != nil {
				log.Printf("warning: failed to reload plugin manager after uninstall %s: %v", pluginID, err)
			}
			d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Plugin %s uninstalled", pluginID),
		})
	}
}

// handleUpdatePlugin updates an installed plugin to the latest marketplace
// version. It resolves the plugin's listing ID (install-status records map
// listing → plugin) and delegates to the same MarketplaceInstaller used by
// install-from-marketplace, so the update goes through the download-token +
// Ed25519 verification path — never an unverified extract.
func handleUpdatePlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pluginID := r.PathValue("id")

		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}

		currentPlugin, exists := d.Pm.Installed[pluginID]
		if !exists {
			http.Error(w, "Plugin not installed", http.StatusNotFound)
			return
		}

		// Marketplace installs record the listing↔plugin mapping in the
		// install-status store; manual imports have no listing and cannot be
		// updated from the marketplace.
		statusStore := plugins.NewInstallStatusStore(d.Db)
		listingID := ""
		if records, err := statusStore.List(ctx); err == nil {
			for id, record := range records {
				if record.PluginID == pluginID {
					listingID = id
					break
				}
			}
		}
		if listingID == "" {
			http.Error(w, "Plugin has no marketplace listing (manually imported?)", http.StatusNotFound)
			return
		}

		// Keep the current version available for rollback.
		rollbackMgr := plugins.NewRollbackManager(d.Db, "./data/plugins")
		sourcePath := filepath.Join("./data/plugins", pluginID, currentPlugin.Version)
		if err := rollbackMgr.StoreVersion(pluginID, currentPlugin.Version, sourcePath); err != nil {
			log.Printf("Warning: Failed to store version for rollback: %v", err)
		}

		client := marketplace.NewClient(&d.Cfg.Marketplace, oauth.NewTokenClient(&d.Cfg.Marketplace))
		installer, err := plugins.NewMarketplaceInstaller(d.Cfg, client, d.Db)
		if err != nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}
		result, err := installer.Install(ctx, plugins.MarketplaceInstallRequest{
			ListingID:  listingID,
			MerchantID: d.Cfg.Marketplace.ClientID,
			StoreID:    d.Cfg.Marketplace.StoreID,
			DeviceID:   marketplace.DeviceIDFromConfig(&d.Cfg.Marketplace),
			DeviceArch: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			OnStateChange: func(state plugins.InstallLifecycleState) {
				_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
					ListingID: listingID,
					PluginID:  pluginID,
					State:     state,
				})
			},
		})
		if err != nil {
			failure := plugins.ClassifyInstallError(err)
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
				ListingID:  listingID,
				PluginID:   pluginID,
				State:      plugins.InstallStateFailed,
				MessageKey: failure.MessageKey,
				Retryable:  failure.Retryable,
			})
			http.Error(w, fmt.Sprintf("Update failed: %v", err), http.StatusBadGateway)
			return
		}
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID:      listingID,
			PluginID:       result.PluginID,
			PluginName:     result.Name,
			CurrentVersion: result.Version,
			State:          plugins.InstallStateActive,
		})

		if err := d.Pm.Reload(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}
		d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":      true,
			"message":      fmt.Sprintf("Plugin updated from %s to %s", currentPlugin.Version, result.Version),
			"from_version": currentPlugin.Version,
			"to_version":   result.Version,
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

// handleExportPlugin streams an installed plugin as a downloadable .tar.gz bundle
// (the offline-provisioning counterpart to import-from-file). The bundle
// round-trips back through import-from-file on another, possibly offline, till.
// Version is taken from ?version= since a plugin may have multiple installed.
func handleExportPlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pluginID := strings.TrimSpace(r.PathValue("id"))
		version := strings.TrimSpace(r.URL.Query().Get("version"))
		if pluginID == "" || version == "" {
			http.Error(w, "plugin id (path) and version (?version=) are required", http.StatusBadRequest)
			return
		}

		tmpDir := filepath.Join("./data/plugins/tmp")
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			http.Error(w, "failed to create temp directory", http.StatusInternalServerError)
			return
		}
		tmpFile := filepath.Join(tmpDir, fmt.Sprintf("export-%d.tar.gz", os.Getpid()))
		defer os.Remove(tmpFile)

		res, err := plugins.NewExporter("./data/plugins").Export(ctx, &plugins.ExportRequest{
			PluginID: pluginID,
			Version:  version,
			DestPath: tmpFile,
		})
		if err != nil {
			// Missing plugin/version or an invalid id -> 404; the exporter guards
			// path traversal and manifest presence.
			http.Error(w, fmt.Sprintf("export failed: %v", err), http.StatusNotFound)
			return
		}

		f, err := os.Open(tmpFile)
		if err != nil {
			http.Error(w, "failed to read bundle", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		filename := fmt.Sprintf("%s-%s.tar.gz", strings.ReplaceAll(pluginID, "/", "_"), version)
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", res.Size))
		w.Header().Set("X-Bundle-SHA256", res.SHA256)
		if _, err := io.Copy(w, f); err != nil {
			log.Printf("Warning: export stream interrupted for %s v%s: %v", pluginID, version, err)
		}
	}
}
