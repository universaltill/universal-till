package pages

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
)

// ut-docs#1092: the setup wizard's language step lists not just the bundled
// locales (httpx.AvailableLocales) but every marketplace catalog listing with
// canonical_type=language whose locale isn't already covered — and installs
// one on selection (POST /api/setup/language) through the EXACT existing
// ut-docs#591 install path (resolveAndInstallBasePlugin), never a second one.

// setupWizardLanguageInstallTimeout bounds POST /api/setup/language's one
// synchronous resolve+install attempt. Deliberately separate from — and much
// longer than — setupBasePluginAttemptTimeout (5s, setup_base_plugins.go):
// that one bounds ut-docs#1055's SILENT best-effort install tucked behind the
// wizard's own response, where the operator isn't waiting on it; this one is
// a FOREGROUND action the operator explicitly chose and is watching a
// spinner for, so it gets long enough to actually download a language pack
// on shop WiFi before falling back to the same background retry.
const setupWizardLanguageInstallTimeout = 20 * time.Second

const (
	// setupLanguageCatalogTTL is how long one successful catalog fetch backs
	// the wizard's tile list before GET /setup refetches.
	setupLanguageCatalogTTL = 5 * time.Minute
	// setupLanguageCatalogFetchTimeout bounds the fetch itself so GET /setup
	// never hangs on the catalog (offline-first: first boot must render with
	// no network, promptly).
	setupLanguageCatalogFetchTimeout = 3 * time.Second
	// setupLanguageCatalogRetryInterval throttles refetch attempts after a
	// FAILED fetch. Much shorter than the success TTL on purpose: an operator
	// who just connected the shop WiFi and reloads the wizard should see the
	// catalog languages within seconds, not after a 5-minute cooldown — but a
	// fully offline till re-rendering /setup repeatedly must not pay the
	// fetch timeout on every render either.
	setupLanguageCatalogRetryInterval = 30 * time.Second
)

// setupLangCatalogCache is the package-level, mutex-guarded TTL cache behind
// the wizard's installable-language tiles. Held across requests (the wizard
// re-renders on every step error and every ?lang= switch); entries survive
// past their TTL as a stale fallback when a refetch fails.
var setupLangCatalogCache struct {
	mu          sync.Mutex
	lastAttempt time.Time
	lastSuccess time.Time
	fetched     bool // at least one fetch succeeded; entries is trustworthy
	entries     []marketplace.PluginSummary
}

// resetSetupLanguageCatalog clears the cache — tests only (the cache is
// package-global, so without this one test's catalog leaks into every other
// GET /setup rendered within the TTL).
func resetSetupLanguageCatalog() {
	c := &setupLangCatalogCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt = time.Time{}
	c.lastSuccess = time.Time{}
	c.fetched = false
	c.entries = nil
}

// setupLanguageCatalogEntries returns the cached language-capability catalog,
// fetching (bounded) when the cache is cold or expired. ok=false means the
// catalog is unreachable AND nothing was ever cached — the caller shows the
// "more languages once connected" note instead of tiles. A stale cache after
// a failed refetch is served as-is (ok=true): a listing that existed five
// minutes ago is a better wizard than an empty step.
//
// The fetch runs under the cache mutex by design: the setup wizard is a
// single-operator, first-boot-only surface, and serializing the (3s-bounded)
// fetch is simpler and safer than letting concurrent renders race to fetch.
func setupLanguageCatalogEntries(ctx context.Context, d *common.Deps) (entries []marketplace.PluginSummary, ok bool) {
	c := &setupLangCatalogCache
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.fetched && now.Sub(c.lastSuccess) < setupLanguageCatalogTTL {
		return c.entries, true
	}
	if now.Sub(c.lastAttempt) < setupLanguageCatalogRetryInterval {
		return c.entries, c.fetched // failed recently: stale-or-empty, no refetch storm
	}
	c.lastAttempt = now

	// No config wired (minimal test harnesses build Deps without Cfg) or no
	// marketplace endpoint configured at all (dev tills): nothing to fetch,
	// and no point paying an enrolment attempt on every render.
	if d.Cfg == nil || strings.TrimSpace(d.Cfg.Marketplace.EndpointURL) == "" {
		return c.entries, c.fetched
	}

	fctx, cancel := context.WithTimeout(ctx, setupLanguageCatalogFetchTimeout)
	defer cancel()
	// Same client construction as resolveAndInstallBasePlugin — one
	// marketplace access pattern, not a new one.
	effCfg := enroll.EnsureRegistered(fctx, d.Cfg, d.Settings)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	// No Locale filter: browsing wants every language listing. Capability is
	// the server-side canonical-type filter; installableSetupLanguages still
	// re-checks CanonicalType client-side (the server-side filter isn't
	// trusted alone — same posture as resolveAndInstallBasePlugin).
	resp, err := client.ListPlugins(fctx, &marketplace.ListPluginsRequest{Capability: []string{"language"}})
	if err != nil {
		logging.L().Warnf("setup wizard: language catalog fetch failed (serving %s): %v",
			map[bool]string{true: "stale cache", false: "bundled only"}[c.fetched], err)
		return c.entries, c.fetched
	}
	c.entries = resp.Plugins
	c.fetched = true
	c.lastSuccess = now
	return c.entries, true
}

// installableLanguage is one catalog-only language tile on the wizard's
// step 1: the base-language subtag the install POST re-resolves through,
// its native display name (what the tile shows — a German speaker looks for
// "Deutsch", not "German"), and the backing listing id (display/debug; the
// POST only needs the locale, which the handler re-resolves via the same
// cache so the request body can't pick an arbitrary listing).
type installableLanguage struct {
	Locale     string
	NativeName string
	ListingID  string
}

// installableSetupLanguages maps catalog entries to the wizard's tile list:
// language listings only, one tile per base-language subtag, skipping any
// locale already covered by the bundled/installed set (no redundant tile for
// a language the till already speaks).
func installableSetupLanguages(entries []marketplace.PluginSummary, available []string) []installableLanguage {
	covered := make(map[string]bool, len(available))
	for _, a := range available {
		if b := baseLang(strings.TrimSpace(a)); b != "" {
			covered[b] = true
		}
	}
	var out []installableLanguage
	for i := range entries {
		e := &entries[i]
		if e.CanonicalType != "language" {
			continue
		}
		listingID := e.ListingID
		if listingID == "" {
			listingID = e.ID
		}
		for _, loc := range e.AvailableLocales {
			b := baseLang(strings.TrimSpace(loc))
			if b == "" || covered[b] {
				continue
			}
			covered[b] = true // also dedups across listings
			out = append(out, installableLanguage{
				Locale:     b,
				NativeName: nativeLanguageName(b),
				ListingID:  listingID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Locale < out[j].Locale })
	return out
}

// setupInstallableLanguages is renderWizard's/the POST handler's view of the
// catalog: the deduped tile list plus whether the catalog is genuinely
// unavailable (unreachable with nothing cached).
func setupInstallableLanguages(ctx context.Context, d *common.Deps) (langs []installableLanguage, unavailable bool) {
	entries, ok := setupLanguageCatalogEntries(ctx, d)
	return installableSetupLanguages(entries, httpx.AvailableLocales()), !ok
}

// nativeLanguageName renders a language code in its own language ("de" →
// "Deutsch", "fr" → "français") via x/text's CLDR self-names — fully offline,
// no lookup service. Falls back to the raw code when the tag is unknown.
func nativeLanguageName(code string) string {
	tag, err := language.Parse(code)
	if err != nil {
		return code
	}
	if n := display.Self.Name(tag); n != "" {
		return n
	}
	return code
}

// isPlausibleLocaleParam keeps the install_pending echo to something
// locale-shaped — the value lands in a rendered attribute (template-escaped
// anyway), but there's no reason to reflect arbitrary query strings at all.
func isPlausibleLocaleParam(v string) bool {
	if len(v) < 2 || len(v) > 12 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// setupLanguageInstallHandler is POST /api/setup/language (ut-docs#1092):
// the wizard's install-a-catalog-language action. Auth-exempt on the same
// first-boot-only window as POST /api/setup — NeedsFirstBoot is the gate.
func setupLanguageInstallHandler(d *common.Deps, svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		locale := baseLang(strings.TrimSpace(r.PostFormValue("locale")))

		// Only a locale the current cached catalog actually offers may be
		// installed — the form body never gets to pick an arbitrary listing.
		// The real UI only renders tiles from this same cache, so a miss here
		// is a stale/forged request; just go back to the wizard.
		langs, _ := setupInstallableLanguages(r.Context(), d)
		known := false
		for _, l := range langs {
			if l.Locale == locale {
				known = true
				break
			}
		}
		if !known {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}

		spec := basePluginSpec{CanonicalType: "language", Locale: locale}
		ctx, cancel := context.WithTimeout(r.Context(), setupWizardLanguageInstallTimeout)
		err = resolveAndInstallBasePlugin(ctx, d, spec)
		cancel()
		if err == nil {
			// Success: redirect through the SAME /setup?lang= mechanism a
			// bundled tile's <a href="/setup?lang=…"> uses — the follow-up GET
			// sets the ut_lang cookie via httpx.ResolveLocale exactly as it
			// would for a bundled pick, so the resulting state is identical
			// (no second cookie mechanism).
			http.Redirect(w, r, "/setup?lang="+url.QueryEscape(locale), http.StatusSeeOther)
			return
		}
		logging.L().Warnf("setup wizard: foreground install of language/%s failed, joining background retry: %v", locale, err)

		// Failure/timeout: join the EXISTING ut-docs#591 pending list — the
		// 5-minute background retry (StartBasePluginRetry) and the Settings
		// pending-chip both already act on it; no second retry mechanism.
		// r.Context(), not the (possibly expired) install ctx: the persist
		// must still work after a timeout.
		pending, loadErr := loadPendingBasePlugins(r.Context(), d)
		if loadErr != nil {
			logging.L().Errorf("setup wizard: load pending base plugins: %v", loadErr)
		}
		already := false
		for _, p := range pending {
			if p == spec {
				already = true
				break
			}
		}
		if !already {
			if saveErr := savePendingBasePlugins(r.Context(), d, append(pending, spec)); saveErr != nil {
				logging.L().Errorf("setup wizard: persist pending language install: %v", saveErr)
			}
		}

		// Continue the wizard in the language it's already showing (cookie or
		// default — never the not-yet-installed pick), with install_pending
		// driving the "still installing in the background" note.
		current := baseLang(httpx.ResolveLocale(w, r))
		if !localeAvailable(current) {
			current = "en"
		}
		http.Redirect(w, r, "/setup?lang="+url.QueryEscape(current)+"&install_pending="+url.QueryEscape(locale), http.StatusSeeOther)
	}
}

// localeAvailable reports whether the till ships (or has installed) locale —
// exact match against httpx.AvailableLocales' bare codes.
func localeAvailable(locale string) bool {
	for _, a := range httpx.AvailableLocales() {
		if a == locale {
			return true
		}
	}
	return false
}
