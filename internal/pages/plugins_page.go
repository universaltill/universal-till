package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// registerPluginsPage renders the installed-plugins MANAGER: every plugin on
// the till (enabled or disabled) with lifecycle actions. Discovering,
// downloading and installing new plugins happens on /plugins/store.
func registerPluginsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/plugins", func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#3079: every lifecycle action here was already
		// plugin_management-gated; now the page itself is too.
		if !requirePage(d, w, r, "plugin_management") {
			return
		}
		ctx := r.Context()

		rows, err := data.NewPluginRepo(d.Db).ListManagedPlugins(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		// ut-docs#5660: why a revocation turned a plugin off. A failure here
		// must never take the page down; the cards just lose the notice.
		revoked, rerr := data.NewPluginRepo(d.Db).RevocationNotices(ctx)
		if rerr != nil {
			logging.L().Warnf("[plugins] revocation notices: %v", rerr)
			revoked = nil
		}
		loc := httpx.RequestLocale(r)
		statuses, _ := plugins.NewInstallStatusStore(d.Db).List(ctx)

		// Docs button targets (ADR-0037): a plugin registering a page entry
		// under the reserved key "docs" gets a Docs button pointing at that
		// entry's route. ListPageEntries already filters to active entries of
		// active plugins, so a disabled plugin (or entry) surfaces no route —
		// never a button that opens an empty/404 page.
		docsRouteByPlugin := map[string]string{}
		if entries, err := data.NewPluginRepo(d.Db).ListPageEntries(ctx); err == nil {
			for _, e := range entries {
				// ut-docs#3817: a docs route under a core-reserved prefix is
				// never dispatched (findPageEntry), so it gets no button.
				if _, reserved := plugins.ReservedPageRoutePrefix(e.Route); reserved {
					continue
				}
				// ut-docs#3994: the button follows the same slot gate as the
				// route (servePluginEntry), so it is never shown then refused.
				if e.EntryKey == plugins.DocsEntryKey && !pluginEntrySlotAllowed(d, r, e.Slot) {
					continue
				}
				if e.EntryKey == plugins.DocsEntryKey && e.Route != "" {
					if _, seen := docsRouteByPlugin[e.PluginID]; !seen {
						docsRouteByPlugin[e.PluginID] = e.Route
					}
				}
			}
		}

		// Catalog match per installed plugin, via the same two-tier
		// byListing/byAuthorName resolution UpdateChecker already uses
		// (plugins.IndexCatalog/Resolve, ut-docs#2131) — byListing alone left
		// every file-imported plugin (no plugin_install_status row at all,
		// internal/data/sync_plugins_repo.go) permanently unable to report an
		// update, indistinguishable from "you are current". GetOrFetch serves
		// the cache when fresh, and when merely stale serves that same stale
		// snapshot immediately while refreshing it in the background
		// (ut-docs#2143) — rather than serving an arbitrarily old cached
		// snapshot forever (the original ut-docs#2131 problem) or blocking
		// this page render on a real network round-trip to an unreachable
		// marketplace (what refetching inline turned out to cost, ut-docs#2143).
		listingByPlugin := map[string]string{}
		for listingID, st := range statuses {
			if st.PluginID != "" {
				listingByPlugin[st.PluginID] = listingID
			}
		}
		var catalogIdx plugins.CatalogIndex
		if d.CatalogRepo != nil {
			// The till's own key, not the UI locale (ut-docs#2674): the
			// cloud's locale filter is an inclusion filter, so a UI locale a
			// plugin doesn't ship would hide its update here while the store
			// and the update checker (same key as this) still see it — and
			// every new ?lang= would mean its own cache file and a blocking
			// cold fetch.
			shopLocale := ""
			if d.Cfg != nil {
				shopLocale = d.Cfg.DefaultLocale
			}
			locale, deviceArch := marketplace.TillCatalogKey(shopLocale)
			if snapshot, _, err := d.CatalogRepo.GetOrFetch(r.Context(), locale, deviceArch); err == nil && snapshot != nil {
				catalogIdx = plugins.IndexCatalog(snapshot)
			}
		}

		search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if search != "" && !strings.Contains(strings.ToLower(row.Name), search) &&
				!strings.Contains(strings.ToLower(row.ID), search) {
				continue
			}
			var latest string
			var hasUpdate, versionUnknown bool
			// versionUnknown covers TWO cases, not just "no match at all"
			// (ut-docs#2131 review): a resolved catalog entry with an empty
			// Version is just as much an unknown as no entry at all — AC2
			// says latest:"" must never be presented as current, and a
			// match with an empty version falls straight through that gap
			// otherwise (hasUpdate stays false, versionUnknown would stay
			// false too, rendering identically to "you are current").
			if catalogPlugin, ok := catalogIdx.Resolve(listingByPlugin[row.ID], row.Author, row.Name); ok && catalogPlugin.Version != "" {
				latest = catalogPlugin.Version
				hasUpdate = plugins.VersionNewer(latest, row.Version)
			} else {
				versionUnknown = true
			}
			// ADR-0172 §5: the tax plugin the till installed by itself says so,
			// and its uninstall confirm says what changes.
			autoNote, uninstallConfirm := "", ""
			if market, auto := taxAutoInstalledPlugin(ctx, d, row.ID); auto {
				loc := httpx.RequestLocale(r)
				autoNote = sprintfKey(loc, "settings.plugins.auto_installed_for", countryDisplayName(ctx, d, loc, market))
				uninstallConfirm = sprintfKey(loc, "settings.plugins.uninstall_tax_confirm", row.Name)
			}
			// ADR-0194: a plugin that ships inside the app (system trust and in
			// the compiled-in lock) is the "Built in" group: it updates only
			// with the app, so no marketplace update is offered for it.
			isBundled := isBundledPlugin(row.ID, row.TrustLevel)
			if isBundled {
				hasUpdate, versionUnknown, latest = false, false, ""
			}
			trustLabelKey, trustHintKey := installedTrustKeys(row.TrustLevel)
			item := map[string]any{
				"bundled":          isBundled,
				"mainControlled":   isBundled && d.SyncPrimaryURL(ctx) != "", // ADR-0194: on a satellite the main till switches it
				"autoNote":         autoNote,
				"uninstallConfirm": uninstallConfirm,
				"id":               row.ID,
				"name":             row.Name,
				"version":          row.Version,
				"enabled":          row.IsActive,
				"trust":            row.TrustLevel,
				"trustLabel":       httpx.T(loc, trustLabelKey),
				"trustHint":        httpx.T(loc, trustHintKey),
				"state":            row.InstallState,
				"hasUpdate":        hasUpdate,
				"latest":           latest,
				"versionUnknown":   versionUnknown,
				"docsRoute":        docsRouteByPlugin[row.ID],
				"revoked":          false,
				// ut-docs#7017: broken code plugin the channel will never reinstall.
				"codeRefused": row.InstallState == data.PluginStateBroken && !isBundled && plugins.CodeRuntimeRefused(row.Runtime),
			}
			if n, ok := revoked[row.ID]; ok {
				item["revoked"] = true
				item["revokedVersion"] = n.Version
				item["revokedReason"] = n.Reason
				item["revokedNotice"] = revocationNoticeText(loc, n)
				item["revokedAt"] = ""
				if !n.RevokedAt.IsZero() {
					item["revokedAt"] = n.RevokedAt.UTC().Format(time.RFC3339)
				}
			}
			items = append(items, item)
		}

		// "Built in" first (stable within each group: the rows are by name).
		sort.SliceStable(items, func(i, j int) bool {
			bi, _ := items[i]["bundled"].(bool)
			bj, _ := items[j]["bundled"].(bool)
			return bi && !bj
		})

		raw, _ := json.Marshal(map[string]any{"items": items, "q": search})
		httpx.Render("ui/pages/plugins.html", map[string]any{
			"title":       httpx.T(httpx.RequestLocale(r), "page.title.plugins"),
			"theme":       d.ResolvedTheme(r),
			"menuItems":   d.MenuSnapshot(),
			"pluginsJSON": template.JS(raw),
		})(w, r)
	})
}

// installedTrustKeys maps plugins.trust_level to the locale keys of the card
// tag and its tooltip (ut-docs#6554), using the store badge's vocabulary.
// Unknown or empty levels fall to "unverified": a label is never upgraded.
func installedTrustKeys(level string) (label, hint string) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "system":
		return "plugins.trust.official", "plugins.trust.official_hint"
	case "trusted", "verified":
		return "plugins.trust.verified", "plugins.trust.verified_hint"
	case "revoked":
		return "plugins.trust.revoked", "plugins.trust.revoked_hint"
	}
	return "plugins.trust.unverified", "plugins.trust.unverified_hint"
}

// revocationNoticeText formats the card notice server-side so the template
// needs no JS string substitution. The version and the (untrusted, possibly
// other-language) reason are wrapped in FSI/PDI so an RTL sentence cannot
// scramble the punctuation around them. The template renders it with x-text.
func revocationNoticeText(loc string, n data.RevocationNotice) string {
	isolate := func(s string) string { return "\u2068" + s + "\u2069" }
	reason := data.CapRevocationReason(n.Reason)
	if reason == "" {
		return fmt.Sprintf(httpx.T(loc, "plugins.manage.revoked_notice"), isolate(n.Version))
	}
	return fmt.Sprintf(httpx.T(loc, "plugins.manage.revoked_notice_reason"), isolate(n.Version), isolate(reason))
}
