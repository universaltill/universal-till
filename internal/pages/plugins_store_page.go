package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

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
	Type             string
	Downloaded       bool
	Installed        bool
	StatusState      string // requested|downloading|installing|failed ("" otherwise)
	StatusMessageKey string // operator-visible failure reason (locale key)
	Retryable        bool
}

// fetchEntitledListings asks the marketplace which listings this merchant is
// approved for (the merchant-portal approve/unapprove decision). Returns
// (ids, true) on success; (nil, false) when the endpoint is unavailable so the
// caller can fall back to the whole catalog (older marketplace deployments).
func fetchEntitledListings(ctx context.Context, d *common.Deps) (map[string]bool, bool) {
	effCfg := enroll.Effective(d.Cfg)
	base := strings.TrimSuffix(strings.TrimRight(effCfg.Marketplace.EndpointURL, "/"), "/api")
	url := fmt.Sprintf("%s/ui/api/merchant/entitlements?merchant_id=%s", base, effCfg.Marketplace.ClientID)

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	// Merchant portal token: required when the marketplace enforces merchant
	// auth; without it a 401 lands in the full-catalog fallback below.
	if tok := effCfg.Marketplace.MerchantToken; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var payload struct {
		Data struct {
			Entitled []struct {
				ListingID string `json:"listing_id"`
			} `json:"entitled"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false
	}
	ids := map[string]bool{}
	for _, e := range payload.Data.Entitled {
		ids[e.ListingID] = true
	}
	return ids, true
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
		snapshot, _, err := d.CatalogRepo.GetOrFetch(ctx, d.Cfg.DefaultLocale, deviceArch)

		entitled, entitledKnown := fetchEntitledListings(ctx, d)

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
				// Only approved (entitled) listings appear in the store. When the
				// marketplace doesn't expose entitlements yet, show everything.
				if entitledKnown && !entitled[listingID] {
					continue
				}
				item := storeItem{
					ListingID:   listingID,
					Name:        p.Name,
					Version:     p.Version,
					Description: p.Description,
					Type:        p.CanonicalType,
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
			"EntitledFiltered": entitledKnown,
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
	respond := func(w http.ResponseWriter, status int, msg string) {
		if status >= 400 {
			logging.L().Warnf("[PluginStore] %s", msg)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": status < 400, "message": msg})
	}

	listingFrom := func(r *http.Request) string {
		_ = r.ParseForm()
		return strings.TrimSpace(r.FormValue("listing_id"))
	}

	mux.HandleFunc("POST /api/plugins/store/download", func(w http.ResponseWriter, r *http.Request) {
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "listing_id required")
			return
		}
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
