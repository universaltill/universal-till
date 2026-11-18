package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// storeItem is one listing in the plugin store: a tile on /plugins/store and
// the whole of its own page at /plugins/store/{listing_id} (ut-docs#4180).
// Lifecycle (actions on the detail page): available -> Download;
// downloaded -> Install / Delete download; installed -> managed from
// /plugins (no store actions).
type storeItem struct {
	ListingID        string
	DetailHref       string // /plugins/store/<path-escaped listing id>
	Initial          string // lettermark shown when there is no icon or it fails to load
	Name             string
	Version          string
	Description      string
	Icon             string // absolute icon URL (marketplace-hosted), "" = no icon
	Vendor           string
	Tier             string // official | verified | unverified (ADR-0006 surface)
	Type             string
	Permissions      []string // manifest capability scopes (FR-006 permission badges); empty when the catalog has none for this release
	Paid             bool     // ut-docs#673: badge only — no price/amount exists in the catalog data model yet (ut-docs#700)
	Downloaded       bool
	Installed        bool
	StatusState      string // requested|downloading|installing|failed ("" otherwise)
	StatusMessageKey string // operator-visible failure reason (locale key)
	Retryable        bool
	ReportHref       string // marketplace "Report this listing" page for this listing (ut-docs#6400)
}

// trustTierOf maps a plugin to its visible trust badge: first-party
// Universal Till plugins are OFFICIAL (the golden badge), marketplace-marked
// verified/trusted publishers are VERIFIED, everything else is UNVERIFIED —
// and unverified installs ask the operator for consent first. The live
// catalog sends the listing UUID as the id, never the com.universaltill.*
// slug, and says first-party itself with trust level "official"
// (ut-docs#2647); the id prefix stays for catalogs that send the slug.
func trustTierOf(pluginID, trustTier string) string {
	if strings.HasPrefix(pluginID, "com.universaltill.") {
		return "official"
	}
	switch strings.ToLower(strings.TrimSpace(trustTier)) {
	case "official":
		return "official"
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
	client := marketplace.NewClient(&effCfg.Marketplace)
	installer, err := plugins.NewMarketplaceInstaller(&effCfg, client, d.Db)
	return installer, &effCfg, err
}

// storeDetailHref is the listing's own page; the id is one path segment.
func storeDetailHref(listingID string) string {
	return "/plugins/store/" + url.PathEscape(listingID)
}

// storeInitial is the tile's lettermark: the name's first letter (or digit),
// uppercased — "?" for a name with none.
func storeInitial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return string(unicode.ToUpper(r))
		}
	}
	if r, _ := utf8.DecodeRuneInString(strings.TrimSpace(name)); r != utf8.RuneError {
		return string(r)
	}
	return "?"
}

// storeListings loads the catalog (the cached snapshot when offline —
// GetOrFetch serves it) and maps every listing to its storeItem, in catalog
// order. Shared by the store grid and each listing's page so the two can't
// disagree about a listing's state. err is the catalog error, if any.
func storeListings(r *http.Request, d *common.Deps) ([]storeItem, error) {
	ctx := r.Context()
	locale, deviceArch := marketplace.TillCatalogKey(d.Cfg.DefaultLocale)
	// Marketplace web base for relative icon paths (endpoint minus /api).
	webBase := strings.TrimSuffix(strings.TrimRight(enroll.Effective(d.Cfg).Marketplace.EndpointURL, "/"), "/api")
	snapshot, _, err := d.CatalogRepo.GetOrFetch(ctx, locale, deviceArch)

	var downloads map[string]plugins.StoreDownload
	if installer, _, ierr := storeInstaller(d); ierr == nil {
		downloads = installer.ListStoreDownloads()
	}
	statuses, _ := plugins.NewInstallStatusStore(d.Db).List(ctx)

	reportBase := strings.TrimSuffix(strings.TrimRight(enroll.Effective(d.Cfg).Marketplace.EndpointURL, "/"), "/api")
	items := []storeItem{}
	if err == nil && snapshot != nil {
		// Browse shows the whole public catalog — an anonymous till sees
		// every plugin. Access is enforced at install/download time (free
		// plugins self-serve; registered/paid need a claimed store), not by
		// hiding listings here.
		for _, p := range snapshot.Plugins {
			it := storeItemFrom(d, p, webBase, downloads, statuses)
			it.ReportHref = storeReportHref(reportBase, it.ListingID)
			items = append(items, it)
		}
	}
	return items, err
}

// Marketplace publisher terms and content guidelines live on the website, in
// English (the operative text), in the chrome-less in-app legal view
// (/app/legal/, no Pricing or sign-up menu: ADR-0194 §3, like the About page),
// so a Play or App Store till never lands on a purchase surface.
// Microsoft Store policy 11.13, ut-docs#6400. The pages themselves are
// published by ut-website (owner-gated, ut-docs#6400).
const (
	storeTermsHref      = "https://universaltill.com/en-gb/app/legal/marketplace-terms/"
	storeGuidelinesHref = "https://universaltill.com/en-gb/app/legal/marketplace-content-guidelines/"
	storeDefaultCloud   = "https://cloud.universaltill.com"
)

// storeReportHref is the marketplace's public report page for one listing,
// opened in the browser: nothing is fetched while the till renders. base is
// the marketplace web base (endpoint minus /api); empty falls back to the
// public cloud host.
func storeReportHref(base, listingID string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = storeDefaultCloud
	}
	return base + "/ui/en-gb/plugins/" + url.PathEscape(listingID) + "/report"
}

// storeItemFrom maps one catalog listing plus the till's local download and
// install-status records to its storeItem.
func storeItemFrom(d *common.Deps, p marketplace.PluginSummary, webBase string,
	downloads map[string]plugins.StoreDownload, statuses map[string]plugins.InstallStatusRecord) storeItem {
	listingID := p.ListingID
	if listingID == "" {
		listingID = p.ID
	}
	item := storeItem{
		ListingID:   listingID,
		DetailHref:  storeDetailHref(listingID),
		Initial:     storeInitial(p.Name),
		Name:        p.Name,
		Version:     p.Version,
		Description: p.Description,
		Vendor:      p.Vendor,
		Tier:        trustTierOf(p.ID, p.TrustTier),
		Type:        p.CanonicalType,
		Permissions: p.Permissions,
		Paid:        p.PaidListing,
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
			// (e.g. uninstalled plugin) doesn't freeze the store listing.
			if _, installed := d.InstalledPlugin(st.PluginID); installed {
				item.Installed = true
			}
		case plugins.InstallStateFailed:
			// Operator-visible failure: keep the listing actionable (retry
			// via Download/Install) and say what went wrong.
			item.StatusState = string(st.State)
			item.StatusMessageKey = st.MessageKey
			item.Retryable = st.Retryable
		case plugins.InstallStateRequested, plugins.InstallStateDownloading, plugins.InstallStateInstalling:
			item.StatusState = string(st.State)
		}
	}
	return item
}

// PluginStoreHandler renders the store: a grid of small tiles, one per
// catalog listing, each a link to the listing's own page (ut-docs#4180).
func PluginStoreHandler(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#3079: same gate as /plugins and the store's own POSTs.
		if !requirePage(d, w, r, "plugin_management") {
			return
		}
		if d.CatalogRepo == nil {
			httpx.RenderError(w, r, http.StatusServiceUnavailable, "plugins.install.error.configuration", nil)
			return
		}
		items, err := storeListings(r, d)
		cats := storeCategories(items)
		httpx.Render("ui/pages/plugins_store.html", map[string]any{
			"title":            httpx.T(httpx.RequestLocale(r), "page.title.plugin_store"),
			"theme":            d.ResolvedTheme(r),
			"menuItems":        d.MenuSnapshot(),
			"Items":            items,
			"Categories":       cats,
			"ActiveCat":        storeActiveCategory(r.URL.Query().Get("type"), cats),
			"EntitledFiltered": false,
			"CatalogError":     err != nil,
			"TermsHref":        storeTermsHref,
			"GuidelinesHref":   storeGuidelinesHref,
		})(w, r)
	}
}

// PluginStoreDetailHandler renders one listing's page: logo, publisher,
// trust/paid badges, full description, what it may access (permission badges
// + plain-language lines, ADR-0121 §2) and its Download / Install / Delete
// download / Manage actions. Same gate and same offline catalog snapshot as
// the store grid.
func PluginStoreDetailHandler(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "plugin_management") {
			return
		}
		if d.CatalogRepo == nil {
			httpx.RenderError(w, r, http.StatusServiceUnavailable, "plugins.install.error.configuration", nil)
			return
		}
		listingID := r.PathValue("listing_id")
		items, err := storeListings(r, d)
		locale := httpx.RequestLocale(r)
		termsGate := !enroll.CurrentStatus().Registered && d.SyncPrimaryURL(r.Context()) == "" && !termsAlreadyAccepted(r.Context(), d)
		for _, it := range items {
			if it.ListingID == listingID {
				httpx.Render("ui/pages/plugins_store_detail.html", map[string]any{
					"title":     it.Name + " — " + httpx.T(locale, "page.title.plugin_store"),
					"theme":     d.ResolvedTheme(r),
					"menuItems": d.MenuSnapshot(),
					"Item":      it,
					// ut-docs#4791: the first Download registers an
					// unregistered till, so ask for the cloud terms there.
					"TermsGate":      termsGate,
					"TermsHref":      storeTermsHref,
					"GuidelinesHref": storeGuidelinesHref,
					"terms":          termsView(r.Context(), d, locale),
				})(w, r)
				return
			}
		}
		if err != nil {
			// No catalog at all (offline, never cached): the store page
			// says so; this page can't know the listing.
			httpx.RenderError(w, r, http.StatusServiceUnavailable, "plugins.store.offline", err)
			return
		}
		httpx.RenderError(w, r, http.StatusNotFound, "plugins.install.error.not_found", nil)
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

// storeActiveCategory is the category the store opens on: ?type=<t> when
// the catalog has listings of that type (ut-docs#4431: the import page's
// "Get the importer for your system" link), else "" (All) — a link never
// opens an empty grid.
func storeActiveCategory(want string, cats []storeCategory) string {
	for _, c := range cats {
		if c.Type == want {
			return want
		}
	}
	return ""
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
		if !canPerform(d, r, "plugin_management") {
			respond(w, http.StatusForbidden, "plugins.install.error.forbidden")
			return
		}
		// ut-docs#460: the store page is fully reachable on a replica, so
		// its download/install lifecycle needs the same primary-
		// authoritative guard as handleInstallFromMarketplace — otherwise
		// the store is a complete bypass of the replica rule. The error is
		// the i18n key; the store page maps it to localized text.
		if d.SyncPrimaryURL(r.Context()) != "" {
			respond(w, http.StatusConflict, "plugins.install.error.replica_use_primary")
			return
		}
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "plugins.install.error.invalid_request")
			return
		}
		// ut-docs#4791: the first Download on an unregistered till registers
		// it, so the cloud terms must be ticked first; refused = nothing is
		// registered or downloaded. The error is the i18n key.
		refuse, gerr := termsGateLazyRegistration(r.Context(), d, termsTicked(r), httpx.RequestLocale(r))
		if gerr != nil {
			logging.L().Errorf("store download: record terms acceptance: %v", gerr)
			respond(w, http.StatusInternalServerError, "settings.error.save_failed")
			return
		}
		if refuse {
			respond(w, http.StatusPreconditionRequired, "settings.enrol.terms_required")
			return
		}
		// ADR-0194: a code listing is never downloaded on the App Store build.
		listingType, codeErr := refuseCodeInstall(d, listingID)
		if codeErr != nil {
			respond(w, http.StatusForbidden, plugins.CodeInstallRefusedMsgID)
			return
		}
		// Store registration is lazy — a download is the first interaction
		// that needs a store identity, so enrol before building the request.
		checkinAfterRegistration(d, enroll.EnsureRegistered(r.Context(), d.Cfg, d.Settings)) // ADR-0148 §2
		installer, effCfg, err := storeInstaller(d)
		if err != nil {
			respond(w, http.StatusInternalServerError, "plugins.install.error.configuration")
			return
		}
		sd, err := installer.DownloadToStore(r.Context(), plugins.MarketplaceInstallRequest{
			ListingID:     listingID,
			CanonicalType: listingType,
			MerchantID:    effCfg.Marketplace.ClientID,
			StoreID:       effCfg.Marketplace.StoreID,
			DeviceID:      marketplace.DeviceIDFromConfig(&effCfg.Marketplace),
		})
		if err != nil {
			// ut-docs#673/#703/#704/#3541/#6548: one classification path —
			// marketplace codes keep their key+status, a transport failure
			// is "unreachable", anything else a translated key. The raw
			// error is logged, never sent to the browser.
			logging.L().Warnf("[PluginStore] download %s: %v", listingID, err)
			f := plugins.ClassifyInstallError(err, storeHasOwner(r.Context(), d.Settings))
			if f.MessageKey == "plugins.install.error.retryable" {
				f.MessageKey = "plugins.install.error.download_failed"
			}
			status := f.Status
			if status == 0 {
				status = http.StatusBadGateway
			}
			respond(w, status, f.MessageKey)
			return
		}
		respond(w, http.StatusOK, fmt.Sprintf("downloaded v%s", sd.Version))
	})

	mux.HandleFunc("POST /api/plugins/store/install", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			respond(w, http.StatusForbidden, "plugins.install.error.forbidden")
			return
		}
		// ut-docs#460: same replica guard as store/download above.
		if d.SyncPrimaryURL(r.Context()) != "" {
			respond(w, http.StatusConflict, "plugins.install.error.replica_use_primary")
			return
		}
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "plugins.install.error.invalid_request")
			return
		}
		// The signing key is fetched lazily (ADR-0148 audit item 3).
		enroll.EnsureSigningKey(r.Context(), d.Cfg, d.Settings)
		installer, _, err := storeInstaller(d)
		if err != nil {
			respond(w, http.StatusInternalServerError, "plugins.install.error.configuration")
			return
		}
		result, err := installer.InstallFromStore(r.Context(), listingID, "")
		if err != nil {
			logging.L().Warnf("[PluginStore] install %s: %v", listingID, err)
			f := plugins.ClassifyInstallError(err, storeHasOwner(r.Context(), d.Settings))
			recordStoreInstallFailure(r.Context(), d, listingID, f)
			status := f.Status
			if status == 0 {
				status = http.StatusBadRequest
			}
			respond(w, status, f.MessageKey)
			return
		}
		_ = plugins.NewInstallStatusStore(d.Db).Save(r.Context(), plugins.InstallStatusRecord{
			ListingID:      listingID,
			PluginID:       result.PluginID,
			PluginName:     result.Name,
			CurrentVersion: result.Version,
			State:          plugins.InstallStateActive,
		})
		// ut-docs#1370: the Plugin Store's "install" button activates the
		// plugin — reconcile the catalog's pinned takeaway rates for the
		// German tax plugin, same as every other activation path.
		reconcileTaxDeTakeawayOverridesIfActivated(r.Context(), d.Db, result.PluginID)
		if err := d.ReloadPlugins(r.Context()); err != nil {
			logging.L().Warnf("[PluginStore] reload after install: %v", err)
		}
		respond(w, http.StatusOK, fmt.Sprintf("installed %s v%s", result.Name, result.Version))
	})

	mux.HandleFunc("POST /api/plugins/store/delete-download", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			respond(w, http.StatusForbidden, "plugins.install.error.forbidden")
			return
		}
		listingID := listingFrom(r)
		if listingID == "" {
			respond(w, http.StatusBadRequest, "plugins.install.error.invalid_request")
			return
		}
		installer, _, err := storeInstaller(d)
		if err != nil {
			respond(w, http.StatusInternalServerError, "plugins.install.error.configuration")
			return
		}
		if err := installer.DeleteStoreDownload(listingID); err != nil {
			respond(w, http.StatusInternalServerError, fmt.Sprintf("delete failed: %v", err))
			return
		}
		respond(w, http.StatusOK, "download deleted")
	})
}

// recordStoreInstallFailure saves a failed install-status record for the
// listing so the store tile and heartbeat show it (ut-docs#6548), unless the
// listing is recorded active and that plugin is still installed — a failed
// reinstall must not mark a working plugin failed. A later successful install
// overwrites the record with active.
func recordStoreInstallFailure(ctx context.Context, d *common.Deps, listingID string, f plugins.InstallFailure) {
	store := plugins.NewInstallStatusStore(d.Db)
	rec, found, err := store.Get(ctx, listingID)
	if err != nil {
		logging.L().Warnf("[PluginStore] read install status %s: %v", listingID, err)
	}
	if found && rec.State == plugins.InstallStateActive && rec.PluginID != "" {
		if _, ok := d.InstalledPlugin(rec.PluginID); ok {
			return
		}
	}
	if err := store.Save(ctx, plugins.InstallStatusRecord{
		ListingID:  listingID,
		PluginID:   rec.PluginID,
		PluginName: rec.PluginName,
		State:      plugins.InstallStateFailed,
		MessageKey: f.MessageKey,
		Retryable:  f.Retryable,
	}); err != nil {
		logging.L().Warnf("[PluginStore] save install status %s: %v", listingID, err)
	}
}

// storeDetailPattern serves a listing's page at /plugins/store/{listing_id}
// (ut-docs#4180). It can't be registered as "GET /plugins/store/{listing_id}":
// that and plugin_settings_page.go's "GET /plugins/{id}/settings" both match
// /plugins/store/settings with neither more specific, and ServeMux panics at
// registration. This two-wildcard pattern is strictly LESS specific than
// /plugins/{id}/settings (and than any future literal /plugins/x/y), so it
// never conflicts; storeDetailDispatch serves only the "store" section and
// 404s the rest exactly as the "/" catch-all did before. The one path it
// can't have is a listing literally named "settings" — listing ids are UUIDs
// (or com.* slugs), so /plugins/store/settings stays the settings page of a
// plugin whose id is "store".
const storeDetailPattern = "GET /plugins/{section}/{listing_id}"

// registerPluginStore mounts the store page, each listing's page and the
// store's lifecycle API.
func registerPluginStore(mux *http.ServeMux, deps *common.Deps) {
	mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))
	mux.HandleFunc("GET /plugins/{section}/{listing_id}", storeDetailDispatch(deps)) // = storeDetailPattern (a literal, so route scanners see it)
	registerPluginStoreAPI(mux, deps)
}

// storeDetailDispatch routes /plugins/store/{listing_id} to the listing page;
// any other /plugins/{x}/{y} is the plain 404 the "/" catch-all gave it
// before this pattern existed (/plugins is core-reserved, so no plugin page
// entry can live there — findPageEntry skips it).
func storeDetailDispatch(d *common.Deps) http.HandlerFunc {
	detail := PluginStoreDetailHandler(d)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("section") != "store" {
			http.NotFound(w, r)
			return
		}
		detail(w, r)
	}
}
