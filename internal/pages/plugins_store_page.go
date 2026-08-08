package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
)

// storeItem is one card on the plugin store page. Lifecycle:
// available -> Download; downloaded -> Install / Delete download;
// installed -> managed from /plugins (no store actions).
type storeItem struct {
	ListingID        string
	Name             string
	Version          string
	Description      string
	Icon             string // absolute icon URL (marketplace-hosted), "" = no icon
	Vendor           string
	Tier             string // official | verified | unverified (ADR-0006 surface)
	Type             string
	Permissions      []string // manifest capability scopes (FR-006 permission badges); empty when the catalog has none for this release
	Downloaded       bool
	Installed        bool
	StatusState      string // requested|downloading|installing|failed ("" otherwise)
	StatusMessageKey string // operator-visible failure reason (locale key)
	Retryable        bool
}

// trustTierOf maps a plugin to its visible trust badge: first-party
// Universal Till plugins are OFFICIAL (the golden badge), marketplace-marked
// verified/trusted publishers are VERIFIED, everything else is UNVERIFIED —
// and unverified installs ask the operator for consent first.
func trustTierOf(pluginID, trustTier string) string {
	if strings.HasPrefix(pluginID, "com.universaltill.") {
		return "official"
	}
	switch strings.ToLower(strings.TrimSpace(trustTier)) {
	case "verified", "trusted":
		return "verified"
	}
	return "unverified"
}

// storeInstaller builds a marketplace installer on the EFFECTIVE config — the
// enrolled identity (device token, signing key) fills fields the operator
// didn't set, so a freshly enrolled till installs without a restart. The
// effective config is returned too for request identity fields.
func storeInstaller(d *common.Deps) (*plugins.MarketplaceInstaller, *config.Config, error) {
	effCfg := enroll.Effective(d.Cfg)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	installer, err := plugins.NewMarketplaceInstaller(&effCfg, client, d.Db)
	return installer, &effCfg, err
}

// PluginStoreHandler renders the store: catalog listings the merchant is
// approved for, each in its download/install lifecycle state.
func PluginStoreHandler(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if d.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		deviceArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
		// Marketplace web base for relative icon paths (endpoint minus /api).
		webBase := strings.TrimSuffix(strings.TrimRight(enroll.Effective(d.Cfg).Marketplace.EndpointURL, "/"), "/api")
		snapshot, _, err := d.CatalogRepo.GetOrFetch(ctx, d.Cfg.DefaultLocale, deviceArch)

		var downloads map[string]plugins.StoreDownload
		if installer, _, ierr := storeInstaller(d); ierr == nil {
			downloads = installer.ListStoreDownloads()
		}
		statuses, _ := plugins.NewInstallStatusStore(d.Db).List(ctx)

		items := []storeItem{}
		if err == nil && snapshot != nil {
			for _, p := range snapshot.Plugins {
				listingID := p.ListingID
				if listingID == "" {
					listingID = p.ID
				}
				// Browse shows the whole public catalog — an anonymous till sees
				// every plugin. Access is enforced at install/download time
				// (free plugins self-serve; registered/paid need a claimed store),
				// not by hiding listings here.
				item := storeItem{
					ListingID:   listingID,
					Name:        p.Name,
					Version:     p.Version,
					Description: p.Description,
					Vendor:      p.Vendor,
					Tier:        trustTierOf(p.ID, p.TrustTier),
					Type:        p.CanonicalType,
					Permissions: p.Permissions,
				}
				if icon := p.IconURL; icon != "" {
					if strings.HasPrefix(icon, "/") {
						icon = webBase + icon
					}
					item.Icon = icon
				}
				if _, ok := downloads[listingID]; ok {
					item.Downloaded = true
				}
				if st, ok := statuses[listingID]; ok {
					switch st.State {
					case plugins.InstallStateActive:
						// Confirm against the actual installed set so a stale status
						// (e.g. uninstalled plugin) doesn't freeze the store card.
						if d.Pm != nil {
							if _, installed := d.Pm.Installed[st.PluginID]; installed {
								item.Installed = true
							}
						}
					case plugins.InstallStateFailed:
						// Operator-visible failure: keep the card actionable (retry
						// via Download/Install) and say what went wrong.
						item.StatusState = string(st.State)
						item.StatusMessageKey = st.MessageKey
						item.Retryable = st.Retryable
					case plugins.InstallStateRequested, plugins.InstallStateDownloading, plugins.InstallStateInstalling:
						item.StatusState = string(st.State)
					}
				}
				items = append(items, item)
			}
		}

		httpx.Render("ui/pages/plugins_store.html", map[string]any{
			"title":            "Plugin Store",
			"theme":            d.CurrentState().Theme,
			"menuItems":        d.Menu,
			"Items":            items,
			"Categories":       storeCategories(items),
			"EntitledFiltered": false,
			"CatalogError":     err != nil,
		})(w, r)
	}
}

// storeCategory is one "browse by category" chip: a plugin type present in the
// catalog and how many listings carry it. Cards filter to it client-side.
type storeCategory struct {
	Type  string
	Count int
}

// storeCategories reduces the visible listings to the distinct plugin types
// present, with counts, sorted by type so the chip row is stable. The type IS
// the merchandising category (payment, theme, report, delivery, …).
func storeCategories(items []storeItem) []storeCategory {
	counts := map[string]int{}
	for _, it := range items {
		if it.Type != "" {
			counts[it.Type]++
		}
	}
	cats := make([]storeCategory, 0, len(counts))
	for t, c := range counts {
		cats = append(cats, storeCategory{Type: t, Count: c})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Type < cats[j].Type })
	return cats
}

// registerPluginStoreAPI wires the store lifecycle actions.
func registerPluginStoreAPI(mux *http.ServeMux, d *common.Deps) {
	// respond writes the { "data": …, "error": null|"…" } envelope
	// universal-till/CLAUDE.md mandates (ut-docs#387): on success msg lands
	// under data.message with error:null, on failure data is null and error
	// carries msg.
	respond := func(w http.ResponseWriter, status int, msg string) {
		if status >= 400 {
			logging.L().Warnf("[PluginStore] %s", msg)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status < 400 {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message": msg}, "error": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": msg})
	}

	listingFrom := func(r *http.Request) string {
		_ = r.ParseForm()
		return strings.TrimSpace(r.FormValue("listing_id"))
	}

	mux.HandleFunc("POST /api/plugins/store/download", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, "manager or admin required")
			return
		}
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "listing_id required")
			return
		}
		// Store registration is lazy — a download is the first interaction
		// that needs a store identity, so enrol before building the request.
		enroll.EnsureRegistered(r.Context(), d.Cfg, d.Settings)
		installer, effCfg, err := storeInstaller(d)
		if err != nil {
			respond(w, http.StatusInternalServerError, "marketplace not configured")
			return
		}
		sd, err := installer.DownloadToStore(r.Context(), plugins.MarketplaceInstallRequest{
			ListingID:  listingID,
			MerchantID: effCfg.Marketplace.ClientID,
			StoreID:    effCfg.Marketplace.StoreID,
			DeviceID:   marketplace.DeviceIDFromConfig(&effCfg.Marketplace),
		})
		if err != nil {
			respond(w, http.StatusBadGateway, fmt.Sprintf("download failed: %v", err))
			return
		}
		respond(w, http.StatusOK, fmt.Sprintf("downloaded v%s", sd.Version))
	})

	mux.HandleFunc("POST /api/plugins/store/install", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, "manager or admin required")
			return
		}
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "listing_id required")
			return
		}
		installer, _, err := storeInstaller(d)
		if err != nil {
			respond(w, http.StatusInternalServerError, "marketplace not configured")
			return
		}
		result, err := installer.InstallFromStore(r.Context(), listingID, "")
		if err != nil {
			respond(w, http.StatusBadRequest, fmt.Sprintf("install failed: %v", err))
			return
		}
		_ = plugins.NewInstallStatusStore(d.Db).Save(r.Context(), plugins.InstallStatusRecord{
			ListingID:      listingID,
			PluginID:       result.PluginID,
			PluginName:     result.Name,
			CurrentVersion: result.Version,
			State:          plugins.InstallStateActive,
		})
		if d.Pm != nil {
			if err := d.Pm.Reload(r.Context()); err != nil {
				logging.L().Warnf("[PluginStore] reload after install: %v", err)
			}
			d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)
		}
		respond(w, http.StatusOK, fmt.Sprintf("installed %s v%s", result.Name, result.Version))
	})

	mux.HandleFunc("POST /api/plugins/store/delete-download", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, "manager or admin required")
			return
		}
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "listing_id required")
			return
		}
		installer, _, err := storeInstaller(d)
		if err != nil {
			respond(w, http.StatusInternalServerError, "marketplace not configured")
			return
		}
		if err := installer.DeleteStoreDownload(listingID); err != nil {
			respond(w, http.StatusInternalServerError, fmt.Sprintf("delete failed: %v", err))
			return
		}
		respond(w, http.StatusOK, "download deleted")
	})
}

// registerPluginStore mounts the store page + its lifecycle API.
func registerPluginStore(mux *http.ServeMux, deps *common.Deps) {
	mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))
	registerPluginStoreAPI(mux, deps)
}
