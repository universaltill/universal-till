package pages

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4477 (ADR-0160 §6): Settings → Language lists the marketplace's
// language packs next to the installed locales, and picking one that isn't
// installed downloads it in the background. Same catalog list as the setup
// wizard's language step (ut-docs#1092, setup_language_catalog.go) and the
// same install path as a country change (queueBasePluginsForCountryChange):
// the pack joins the ut-docs#591 pending list and the retry loop is nudged —
// no second installer. The Language card shows a polling status chip, never
// a modal, and applies the language through the card's own save once the
// pack has arrived.

// settingsInstallableLanguages is the Language card's "download" group:
// catalog languages the till doesn't have yet. Offline it is empty, so the
// card lists installed locales only — a stale cache is not offered while
// netreach says the cloud can't be reached, since picking from it could
// only queue a download that can't run. Empty on an additional till too.
func settingsInstallableLanguages(ctx context.Context, d *common.Deps) []installableLanguage {
	if !settingsOffersLanguageDownloads(ctx, d) {
		return nil
	}
	entries, ok := setupLanguageCatalogEntries(ctx, d)
	if !ok {
		return nil
	}
	return installableSetupLanguages(entries, httpx.AvailableLocales())
}

// settingsOffersLanguageDownloads: online (as far as netreach knows) and not
// an additional till. The shop's language is shop-wide and an additional till
// takes it from its main till (ut-docs#2998), which would not have the pack:
// the download is offered on the main till, like a country change's packs.
func settingsOffersLanguageDownloads(ctx context.Context, d *common.Deps) bool {
	return d.NetReach.Status() != netreach.Unreachable && !tillFollowsMain(ctx, d)
}

// settingsLanguageRefreshing keeps at most one background catalog refresh
// in flight for GET /settings.
var settingsLanguageRefreshing atomic.Bool

// settingsInstallableLanguagesCached is GET /settings' view of the same
// list: it never waits on the network (offline-first — every Settings tab
// renders this page). It serves the cached catalog and, when that is cold
// or expired, refreshes it in the background for the next render; the
// fetch itself stays bounded and throttled by setupLanguageCatalogEntries.
// Only the Apply (queueSettingsLanguageInstall) re-checks synchronously.
func settingsInstallableLanguagesCached(ctx context.Context, d *common.Deps) []installableLanguage {
	if !settingsOffersLanguageDownloads(ctx, d) {
		return nil
	}
	c := &setupLangCatalogCache
	if !c.mu.TryLock() {
		return nil // a fetch is running right now; the next render has it
	}
	entries, fetched := c.entries, c.fetched
	fresh := fetched && time.Since(c.lastSuccess) < setupLanguageCatalogTTL
	c.mu.Unlock()
	if !fresh && settingsLanguageRefreshing.CompareAndSwap(false, true) {
		d.AsyncWork.Add(1)
		go func() {
			defer d.AsyncWork.Done()
			defer settingsLanguageRefreshing.Store(false)
			defer logging.RecoverAndLog("pages.settingsLanguageCatalog")
			setupLanguageCatalogEntries(context.Background(), d)
		}()
	}
	if !fetched {
		return nil
	}
	return installableSetupLanguages(entries, httpx.AvailableLocales())
}

// queueSettingsLanguageInstall handles a Language card pick that isn't an
// installed locale. It reports whether the pick was queued: only a locale
// the current catalog list offers is accepted, so a form can never name an
// arbitrary listing (same rule as POST /api/setup/language).
func queueSettingsLanguageInstall(ctx context.Context, d *common.Deps, locale string) bool {
	locale = baseLang(strings.TrimSpace(locale))
	if !slices.ContainsFunc(settingsInstallableLanguages(ctx, d), func(l installableLanguage) bool { return l.Locale == locale }) {
		return false
	}
	if err := addPendingBasePlugins(ctx, d, []basePluginSpec{{CanonicalType: "language", Locale: locale}}); err != nil {
		logging.L().Errorf("settings: queue language pack %s: %v", locale, err)
		return false
	}
	if err := d.Settings.Set(ctx, common.KeyLocaleSwitchPending, locale+"@"+strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		logging.L().Errorf("settings: record pending language switch %s: %v", locale, err)
		return false
	}
	nudgeBasePluginRetry()
	return true
}

// clearLanguageSwitchPending forgets a pending switch: the operator saved a
// language (the chip's own apply included), or the pack is no longer coming.
func clearLanguageSwitchPending(ctx context.Context, d *common.Deps) {
	if v, _, err := d.Settings.Get(ctx, common.KeyLocaleSwitchPending); err == nil && v == "" {
		return // nothing pending: no write on every language save
	}
	if err := d.Settings.Set(ctx, common.KeyLocaleSwitchPending, ""); err != nil {
		logging.L().Warnf("settings: clear pending language switch: %v", err)
	}
}

// installedLocaleFor returns the installed locale to apply for a picked base
// language: the exact code when installed, else the first installed locale
// with the same base language (a pack may ship de-DE rather than de).
func installedLocaleFor(locale string) string {
	avail := httpx.AvailableLocales()
	if slices.Contains(avail, locale) {
		return locale
	}
	for _, a := range avail {
		if baseLang(a) == locale {
			return a
		}
	}
	return ""
}

// languageSwitchView is the Language card chip's state.
type languageSwitchView struct {
	// State: "installing" (still queued, the chip polls), "ready" (installed;
	// the chip applies ApplyLocale through POST /api/settings/save) or
	// "unavailable" (left the queue without arriving: no listing for it, or
	// the operator removed it under Settings → Data).
	State       string
	Locale      string
	ApplyLocale string
	NativeName  string
}

// languageSwitchApplyWindow bounds how long after the pick the chip still
// applies a language by itself. A pack that arrives later (the till was
// offline for a day) must not switch the whole shop when someone next opens
// Settings: the pick is forgotten and the language simply sits in the list
// as installed, for the operator to pick (review of ut-docs#4477).
const languageSwitchApplyWindow = time.Hour

// parseLanguageSwitchPending splits KeyLocaleSwitchPending's "<locale>@<unix
// seconds of the pick>". A value without a valid time reads as picked long
// ago, so it is never applied by itself.
func parseLanguageSwitchPending(raw string) (locale string, pickedAt time.Time) {
	locale, ts, _ := strings.Cut(strings.TrimSpace(raw), "@")
	if sec, err := strconv.ParseInt(ts, 10, 64); err == nil {
		pickedAt = time.Unix(sec, 0)
	}
	return locale, pickedAt
}

// pendingLanguageSwitch reads the chip's state; nil = nothing pending.
func pendingLanguageSwitch(ctx context.Context, d *common.Deps) *languageSwitchView {
	raw, ok, err := d.Settings.Get(ctx, common.KeyLocaleSwitchPending)
	locale, pickedAt := parseLanguageSwitchPending(raw)
	if err != nil || !ok || locale == "" {
		return nil
	}
	v := &languageSwitchView{Locale: locale, NativeName: httpx.NativeLanguageName(locale)}
	if apply := installedLocaleFor(locale); apply != "" {
		if time.Since(pickedAt) > languageSwitchApplyWindow {
			clearLanguageSwitchPending(ctx, d)
			return nil
		}
		v.State, v.ApplyLocale = "ready", apply
		return v
	}
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		logging.L().Warnf("settings: read pending base plugins: %v", err)
		v.State = "installing" // unknown: keep waiting rather than give up
		return v
	}
	if slices.Contains(pending, basePluginSpec{CanonicalType: "language", Locale: locale}) {
		v.State = "installing"
		return v
	}
	// Shown once: the next render has nothing pending.
	clearLanguageSwitchPending(ctx, d)
	v.State = "unavailable"
	return v
}

// registerSettingsLanguageInstall: GET /ui/settings/language-install, the
// Language card's chip. Empty 200 when nothing is pending or the viewer may
// not open Settings.
func registerSettingsLanguageInstall(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/settings/language-install", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !canPerform(d, r, "settings") {
			w.WriteHeader(http.StatusOK)
			return
		}
		v := pendingLanguageSwitch(r.Context(), d)
		if v == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/settings_language_install.html", v)(w, r)
	})
}
