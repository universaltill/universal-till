package pages

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#5479 (ADR-0160 §6): Weblate's approved strings reach a till as a
// signed first-party language bundle for EVERY language, including the ones
// compiled into the binary (ar, en, fa, tr). The Language card and the setup
// wizard only offer catalog languages the till does not have, so a till on a
// built-in language never downloaded its bundle; this installs it. The bundle
// is a layer on top of the built-in file (config.I18n: a bundle generated
// after this binary's build date wins, a key it omits falls back to the
// built-in file, then English), so nothing here is needed to render: offline,
// unpublished, or failed, the embedded file stays and the till reads as
// before. Later versions are kept current by the existing language-pack
// auto-update (plugin_update_scheduler.go).

const (
	// KeyBuiltinBundleChecked is "<lang>@<unix seconds>" of the last time the
	// bundle for the shop's built-in language was queued, so a language with
	// no published bundle (or an offline till) costs at most one catalog
	// look-up a day instead of one per scheduler tick.
	keyBuiltinBundleChecked     = "i18n.builtin_bundle_checked"
	builtinBundleRecheck        = 24 * time.Hour
	builtinBundleAttemptTimeout = 60 * time.Second
	firstPartyLanguagePfx       = plugins.FirstPartyIDPrefix + "language-"
)

// builtinBundleSpec is the language spec to queue for the shop locale, or
// false when it is not a built-in language (setup's country flow and the
// Language card already handle those) or is not a plain language subtag.
func builtinBundleSpec(locale string) (basePluginSpec, bool) {
	lang := baseLang(strings.TrimSpace(locale))
	if !isLanguageSubtag(lang) || !coreBundlesLanguage(lang) {
		return basePluginSpec{}, false
	}
	return basePluginSpec{CanonicalType: "language", Locale: lang}, true
}

// hasFirstPartyBundle reports whether a first-party language bundle for lang
// (id <first-party prefix>language-<lang>[-region]) is installed.
func hasFirstPartyBundle(installedIDs []string, lang string) bool {
	want := firstPartyLanguagePfx + lang
	for _, id := range installedIDs {
		id = strings.ToLower(id)
		if id == want {
			return true
		}
		// a regional bundle (…-pt-br, …-zh-hans): a 2-4 letter subtag only
		if rest, ok := strings.CutPrefix(id, want+"-"); ok && len(rest) >= 2 && len(rest) <= 4 && !strings.Contains(rest, "-") {
			return true
		}
	}
	return false
}

// builtinBundleInstallFn is indirected for tests.
var builtinBundleInstallFn = resolveAndInstallBasePluginWith

// builtinBundleOfficial accepts only Universal Till's own listings: an
// unattended install never takes a third-party pack for a built-in language.
func builtinBundleOfficial(p *marketplace.PluginSummary) bool {
	return strings.EqualFold(p.TrustTier, "official")
}

// builtinBundleDone marks a language whose bundle arrived once; after that
// the till never reinstalls it by itself (a manager who removed it keeps it
// removed) and only the language-pack auto-update touches it.
const builtinBundleDone = "done"

// installBuiltinLocaleBundle installs the first-party bundle for the shop's
// built-in language when none is installed. Main/standalone till only (a
// joined till takes the language from its main till, ut-docs#2998), online
// only, no pending-list entry (so no Settings chip: the language already
// works), and a definitive "nothing published" answer is asked again only
// after a day. A failed or offline attempt is retried on the next tick.
func installBuiltinLocaleBundle(ctx context.Context, d *common.Deps, now time.Time) {
	if d.NetReach != nil && d.NetReach.Status() == netreach.Unreachable {
		return
	}
	locale, _, err := d.Settings.Get(ctx, "store.locale")
	if err != nil {
		return
	}
	spec, ok := builtinBundleSpec(locale)
	if !ok {
		return
	}
	if raw, _, err := d.Settings.Get(ctx, keyBuiltinBundleChecked); err == nil {
		lang, ts, _ := strings.Cut(strings.TrimSpace(raw), "@")
		if lang == spec.Locale && ts == builtinBundleDone {
			return
		}
		if sec, perr := strconv.ParseInt(ts, 10, 64); perr == nil && lang == spec.Locale &&
			now.Sub(time.Unix(sec, 0)) < builtinBundleRecheck {
			return
		}
	}
	rows, err := data.NewPluginRepo(d.Db).ListInstalledPlugins(ctx)
	if err != nil {
		return
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	mark := func(v string) {
		// settings-write:allow local look-up throttle for this main till only (the scheduler skips a replica); nothing shop-wide to send
		if err := d.Settings.Set(ctx, keyBuiltinBundleChecked, spec.Locale+"@"+v); err != nil {
			logging.L().Warnf("builtin locale bundle: record check: %v", err)
		}
	}
	if hasFirstPartyBundle(ids, spec.Locale) {
		mark(builtinBundleDone)
		return
	}
	actx, cancel := context.WithTimeout(ctx, builtinBundleAttemptTimeout)
	defer cancel()
	if err := builtinBundleInstallFn(actx, d, spec, builtinBundleOfficial); err != nil {
		logging.L().Warnf("builtin locale bundle: %s: %v (the built-in file stays in use)", spec.Locale, err)
		return
	}
	mark(strconv.FormatInt(now.Unix(), 10))
}
