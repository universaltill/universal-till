package pages

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
)

func registerPluginAPI(mux *http.ServeMux, d *common.Deps) {
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

	// Grant/revoke permissions
	mux.HandleFunc("/api/plugins/permissions/grant", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
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
		// A fresh grant can change what a subscribed plugin answers (e.g.
		// events:receive turning cached declines into real overrides) —
		// drop cached ".ask" answers (ut-docs#222 review finding).
		plugins.SharedBus(d.Db).BumpGeneration()

		locale := httpx.ResolveLocale(w, r)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html.EscapeString(httpx.T(locale, "plugins.permission.granted"))))
	})

	mux.HandleFunc("/api/plugins/permissions/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
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
		// Revoking must take effect on the NEXT recompute, as it did
		// pre-cache: Ask skips permission-denied subscribers, but only a
		// generation bump makes the cache re-ask (ut-docs#222 review finding).
		plugins.SharedBus(d.Db).BumpGeneration()

		locale := httpx.ResolveLocale(w, r)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html.EscapeString(httpx.T(locale, "plugins.permission.revoked"))))
	})

	// Update trust level
	mux.HandleFunc("/api/plugins/trust", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
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

		locale := httpx.ResolveLocale(w, r)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html.EscapeString(httpx.T(locale, "plugins.trust.level_updated"))))
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
		if !isManagerOrAuthOff(r) {
			writeInstallResponse(w, http.StatusForbidden, false, "manager or admin required", "plugins.install.error.forbidden")
			return
		}
		ctx := r.Context()
		// ut-docs#460: the installed plugin set is primary-authoritative,
		// like the enrol-token/revoke handlers in sync_api.go — reject on a
		// replica (no proxying), pointing the operator at the primary. The
		// sync pull tick (syncPullPlugins) brings the primary's set here
		// automatically within ~30s.
		if d.SyncPrimaryURL(ctx) != "" {
			writeInstallResponse(w, http.StatusConflict, false, "", "plugins.install.error.replica_use_primary")
			return
		}
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

		// Store registration is lazy: an install is the first interaction
		// that needs a store identity, so enrol here if the till hasn't yet.
		// The effective config fills any fields the operator didn't set.
		effCfg := enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
		client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
		installer, err := plugins.NewMarketplaceInstaller(&effCfg, client, d.Db)
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
			MerchantID: effCfg.Marketplace.ClientID,
			StoreID:    effCfg.Marketplace.StoreID,
			DeviceID:   marketplace.DeviceIDFromConfig(&effCfg.Marketplace),
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

		// Reload plugin manager to pick up the new plugin and rebuild the
		// nav so newly registered pages appear immediately (serialized —
		// the sync-pull goroutine runs the same sequence).
		if err := d.ReloadPlugins(ctx); err != nil {
			// Non-fatal - plugin is installed but won't show until restart
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"success":     true,
				"message":     "",
				"message_key": "plugins.install.success",
				"plugin_id":   result.PluginID,
			},
			"error": nil,
		})
	}
}

// writeInstallResponse writes the { "data": …, "error": null|"…" } envelope
// universal-till/CLAUDE.md mandates (ut-docs#387). Every existing call site
// passes success=false (the success path above encodes its own richer body
// directly) so on failure data is null and error carries the most specific
// message available -- messageKey first (an i18n key the modal's JS already
// maps to localized text), falling back to the plain message some call sites
// use instead.
func writeInstallResponse(w http.ResponseWriter, status int, success bool, message string, messageKey string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if success {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"success":     success,
				"message":     message,
				"message_key": messageKey,
			},
			"error": nil,
		})
		return
	}
	errMsg := messageKey
	if errMsg == "" {
		errMsg = message
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data":  nil,
		"error": errMsg,
	})
}

// writeReplicaGuard answers a plugin mutation attempted on a replica
// (ut-docs#460): 409 with an i18n message key in the standard envelope —
// the plugins page maps the key to localized text client-side. Same
// reject-with-pointer precedent as sync_api.go's enrol-token handlers.
func writeReplicaGuard(w http.ResponseWriter, messageKey string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data":  nil,
		"error": messageKey,
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
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		// ut-docs#460: plugin state (including is_active) is
		// primary-authoritative. Nothing in the sync pull reconciles a
		// locally flipped is_active back, so a replica-local toggle would
		// be permanent silent drift from the shop's set — reject it.
		if d.SyncPrimaryURL(ctx) != "" {
			writeReplicaGuard(w, "plugins.manage.error.replica_use_primary")
			return
		}
		pluginID := r.PathValue("id")
		if pluginID == "" {
			http.Error(w, "plugin ID is required", http.StatusBadRequest)
			return
		}
		if err := data.NewPluginRepo(d.Db).SetPluginActive(ctx, nil, pluginID, active); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data":  nil,
				"error": fmt.Sprintf("failed to %s plugin: %v", verb, err),
			})
			return
		}
		if err := d.ReloadPlugins(ctx); err != nil {
			log.Printf("warning: reload after %s %s: %v", verb, pluginID, err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data":  map[string]interface{}{"message": fmt.Sprintf("Plugin %s %s", pluginID, verb)},
			"error": nil,
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
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		// ut-docs#460: same primary-authoritative guard as install — a
		// local uninstall on a replica would only be undone by the next
		// plugin sync pull, so reject it with a pointer to the primary
		// (sync_api.go's enrol-token precedent; no proxying).
		if d.SyncPrimaryURL(ctx) != "" {
			writeReplicaGuard(w, "plugins.uninstall.error.replica_use_primary")
			return
		}
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
				"data":  nil,
				"error": fmt.Sprintf("Uninstall failed: %v", err),
			})
			return
		}

		// Remove the installed files (best-effort; the DB is the source of truth).
		pluginDir := filepath.Join(paths.Plugins(), pluginID)
		if err := os.RemoveAll(pluginDir); err != nil {
			log.Printf("warning: failed to remove plugin files %s: %v", pluginDir, err)
		}

		// Clear marketplace install-status records so the plugins page doesn't
		// keep reporting the listing as active after uninstall.
		if err := plugins.NewInstallStatusStore(d.Db).ClearForPlugin(ctx, pluginID); err != nil {
			log.Printf("warning: failed to clear install status for %s: %v", pluginID, err)
		}

		// Reload so the nav/menu no longer shows the plugin's entries.
		if err := d.ReloadPlugins(ctx); err != nil {
			log.Printf("warning: failed to reload plugin manager after uninstall %s: %v", pluginID, err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data":  map[string]interface{}{"message": fmt.Sprintf("Plugin %s uninstalled", pluginID)},
			"error": nil,
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
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		// ut-docs#460: version changes are primary-authoritative too — a
		// replica-local update would put this till on a version the sync
		// pull then fights (it converges versions to the primary's).
		if d.SyncPrimaryURL(ctx) != "" {
			writeReplicaGuard(w, "plugins.manage.error.replica_use_primary")
			return
		}
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
		rollbackMgr := plugins.NewRollbackManager(d.Db, paths.Plugins())
		sourcePath := filepath.Join(paths.Plugins(), pluginID, currentPlugin.Version)
		if err := rollbackMgr.StoreVersion(pluginID, currentPlugin.Version, sourcePath); err != nil {
			log.Printf("Warning: Failed to store version for rollback: %v", err)
		}

		effCfg := enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
		client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
		installer, err := plugins.NewMarketplaceInstaller(&effCfg, client, d.Db)
		if err != nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}
		result, err := installer.Install(ctx, plugins.MarketplaceInstallRequest{
			ListingID:  listingID,
			MerchantID: effCfg.Marketplace.ClientID,
			StoreID:    effCfg.Marketplace.StoreID,
			DeviceID:   marketplace.DeviceIDFromConfig(&effCfg.Marketplace),
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

		if err := d.ReloadPlugins(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"message":      fmt.Sprintf("Plugin updated from %s to %s", currentPlugin.Version, result.Version),
				"from_version": currentPlugin.Version,
				"to_version":   result.Version,
			},
			"error": nil,
		})
	}
}

// handleRollbackPlugin handles rolling back a plugin to a previous version (T025)
func handleRollbackPlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		// ut-docs#460: same as update — a replica-local rollback moves this
		// till's version off the shop's set; roll back on the primary and
		// let the sync pull converge every till.
		if d.SyncPrimaryURL(ctx) != "" {
			writeReplicaGuard(w, "plugins.manage.error.replica_use_primary")
			return
		}
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

		rollbackMgr := plugins.NewRollbackManager(d.Db, paths.Plugins())
		// TODO: Extract actual user from session
		if err := rollbackMgr.Rollback(ctx, pluginID, req.Version, "system"); err != nil {
			http.Error(w, fmt.Sprintf("Rollback failed: %v", err), http.StatusInternalServerError)
			return
		}

		if err := d.ReloadPlugins(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"message": fmt.Sprintf("Plugin rolled back to version %s", req.Version),
				"version": req.Version,
			},
			"error": nil,
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
			"data": map[string]interface{}{
				"updates": updates,
				"count":   len(updates),
			},
			"error": nil,
		})
	}
}

// handleImportFromFile handles manual plugin import from uploaded file (T028)
func handleImportFromFile(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		// ut-docs#460: deliberately NOT replica-guarded. File imports are
		// till-local by design — no listing_id, so the sync pull never
		// propagates OR prunes them (its uninstall diff only touches
		// plugins named by a plugin_install_status record), meaning an
		// import here cannot drift from the primary's marketplace set.
		// Blocking it with "install on the primary instead" would be
		// actively false: a file import on the primary never arrives on
		// any other till. Offline provisioning stays per-till, per the
		// ADR-0011 amendment's scope boundary.

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
		tmpDir := filepath.Join(paths.Plugins("tmp"))
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

		// Create importer. Use the configured marketplace Ed25519 public key so a
		// bundle carrying a signature is actually verified against it — CLAUDE.md:
		// "Never run an unverified plugin." When no key is configured (dev/offline
		// default) the verifier has nothing to check and unsigned bundles import,
		// preserving offline provisioning.
		verifier, err := plugins.NewManifestVerifier(d.Cfg.Marketplace.PublicKey)
		if err != nil {
			log.Printf("Warning: failed to create verifier: %v", err)
			verifier = nil // Allow import without verification in dev-mode
		}
		importer := plugins.NewImporter(d.Db, paths.Plugins(), verifier)

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
		if err := d.ReloadPlugins(ctx); err != nil {
			log.Printf("Warning: failed to reload plugin manager: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"message":  fmt.Sprintf("Plugin %s v%s imported successfully", result.Name, result.Version),
				"plugin":   result.PluginID,
				"version":  result.Version,
				"warnings": result.Warnings,
			},
			"error": nil,
		})
	}
}

// handleExportPlugin streams an installed plugin as a downloadable .tar.gz bundle
// (the offline-provisioning counterpart to import-from-file). The bundle
// round-trips back through import-from-file on another, possibly offline, till.
// Version is taken from ?version= since a plugin may have multiple installed.
func handleExportPlugin(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		pluginID := strings.TrimSpace(r.PathValue("id"))
		version := strings.TrimSpace(r.URL.Query().Get("version"))
		if pluginID == "" || version == "" {
			http.Error(w, "plugin id (path) and version (?version=) are required", http.StatusBadRequest)
			return
		}

		tmpDir := filepath.Join(paths.Plugins("tmp"))
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			http.Error(w, "failed to create temp directory", http.StatusInternalServerError)
			return
		}
		tmpFile := filepath.Join(tmpDir, fmt.Sprintf("export-%d.tar.gz", os.Getpid()))
		defer os.Remove(tmpFile)

		res, err := plugins.NewExporter(paths.Plugins()).Export(ctx, &plugins.ExportRequest{
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
