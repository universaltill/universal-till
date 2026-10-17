package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/msgfmt"
)

// baseLocale is T's guaranteed-complete terminal fallback (ut-docs#995) —
// core/shipped keys always exist here (guard-i18n.sh enforces parity across
// web/locales/*.json), though a plugin-only key can still miss it.
const baseLocale = "en"

// I18n is a tiny JSON-based translator optimized for low-memory devices.
type I18n struct {
	mu       sync.RWMutex
	messages map[string]map[string]string // locale -> key -> message (base files)
	// First-party language bundles (ADR-0160 §6): plugins whose signed
	// manifest id carries the reserved com.universaltill. prefix. fresh =
	// generated after this binary's built-in strings (beats them); stale =
	// older, or with no bundle.json generated_at (loses to them).
	fresh    map[string]map[string]string
	stale    map[string]map[string]string
	overlays map[string]map[string]string // locale -> key -> message (third-party plugins)
	shop     map[string]map[string]string // locale -> key -> message (manager edits; win over everything)
	fallback string
	// gen (ut-docs#2501) moves on every SetShopOverrides/SetPluginLocales — the
	// two ways translated text changes after boot — so a cache of rendered
	// HTML (internal/ui's SellScreenCache) can key on it instead of serving
	// the old wording until its max age. Guarded by mu.
	gen uint64
}

// Generation reports how many times the overlay/shop-override layers have
// been replaced since this translator was built (ut-docs#2501).
func (i *I18n) Generation() uint64 {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.gen
}

// NewI18n loads base locale files from a plain OS directory. Kept for
// tests that already point at a real web/locales checkout; production
// startup uses NewI18nFS with the embedded FS instead (see
// internal/pages/init.go) so a packaged install never depends on web/locales
// being present relative to whatever directory the OS launches the binary
// from — a missing/misplaced directory here used to be a hard
// log.Fatalf-on-startup crash, which is unacceptable for someone else's
// shop with no one around to debug it.
//
// Test-only-reachable by design, kept deliberately (ut-docs#1566 — on
// scripts/ci/deadcode-baseline.txt, not a deletion candidate): some eighty
// page/handler tests across internal/pages (and its sub-packages),
// internal/httpx and this package construct their translator with it
// against the on-disk web/locales checkout.
func NewI18n(localesDir string, fallback string) (*I18n, error) {
	return NewI18nFS(os.DirFS(localesDir), fallback)
}

// NewI18nFS loads base locale files from any fs.FS (an embed.FS in
// production, os.DirFS(dir) for tests/back-compat).
func NewI18nFS(fsys fs.FS, fallback string) (*I18n, error) {
	i := &I18n{messages: make(map[string]map[string]string), fresh: map[string]map[string]string{}, stale: map[string]map[string]string{}, overlays: make(map[string]map[string]string), shop: make(map[string]map[string]string), fallback: fallback}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		locale := strings.TrimSuffix(name, ".json")
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", name, err)
		}
		i.messages[locale] = m
	}
	if _, ok := i.messages[fallback]; !ok {
		i.messages[fallback] = map[string]string{}
	}
	return i, nil
}

// T returns the translation for key in the given locale, falling back to the
// base language (en-US -> en), then the default locale, then its base, then
// "en" itself as a guaranteed-complete terminal entry. Locale files are keyed
// by base language (en.json, fa.json) while configuration and requests may
// carry full BCP 47 tags.
//
// Within each locale of that chain the layers are tried in this order
// (ADR-0160 §6; Entries and lookupWithSourceLocked use the same order):
//  1. shop override (manager edits);
//  2. first-party bundle generated after this binary's build date
//     (buildinfo.BuildDate);
//  3. built-in locale file;
//  4. first-party bundle older than the binary (or with no generated_at);
//  5. third-party plugin overlay — it adds locales and fills gaps, but never
//     overrides a core string (ADR-0010).
//
// The trailing "en" matters when i.fallback is itself non-English — e.g. a
// shop's configured store.locale (ut-docs#861) — and a key is missing from
// that fallback's own translation source (shipped locales can't drift,
// guard-i18n.sh enforces parity against en.json, but a plugin-provided
// language legitimately can: a bundle omits unapproved strings, ADR-0160
// §4). Without this, resolution has nothing left to try and the raw key
// renders instead of English text (ut-docs#995).
func (i *I18n) T(locale, key string) string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if v, _, ok := i.resolveLocked([]string{locale, baseLang(locale), i.fallback, baseLang(i.fallback), baseLocale}, key); ok {
		return v
	}
	return key
}

// resolveLocked walks locales in order and returns the first value for key
// and the locale it came from, trying each locale's layers in T's order
// (layerLookupLocked).
func (i *I18n) resolveLocked(locales []string, key string) (string, string, bool) {
	for _, loc := range locales {
		if v, _, ok := i.layerLookupLocked(loc, key); ok {
			return v, loc, true
		}
	}
	return "", "", false
}

// layerLookupLocked resolves key in exactly one locale through T's layer
// order, reporting which layer supplied it ("shop", "base" or "plugin" —
// both first-party tiers and third-party overlays report "plugin", the
// translation editor's existing label).
func (i *I18n) layerLookupLocked(loc, key string) (string, string, bool) {
	if v, ok := i.shop[loc][key]; ok {
		return v, "shop", true
	}
	if v, ok := i.fresh[loc][key]; ok {
		return v, "plugin", true
	}
	if v, ok := i.messages[loc][key]; ok {
		return v, "base", true
	}
	if v, ok := i.stale[loc][key]; ok {
		return v, "plugin", true
	}
	if v, ok := i.overlays[loc][key]; ok {
		return v, "plugin", true
	}
	return "", "", false
}

// TM translates a MessageFormat key (ADR-0160 §8, ut-docs#4373) — a value
// such as "{count, plural, one {# item} other {# items}}" — and formats it
// with args. Plural categories follow the language the value is written in
// (a German till falling back to English text uses English categories).
// A value that doesn't format (malformed ICU in a translation, a missing
// argument) falls back to the English value; if English fails too, its
// raw text renders. An unknown key renders as the key, like T.
func (i *I18n) TM(locale, key string, args map[string]any, opts msgfmt.Options) string {
	i.mu.RLock()
	v, src, ok := i.resolveLocked([]string{locale, baseLang(locale), i.fallback, baseLang(i.fallback), baseLocale}, key)
	en, _, enOK := i.resolveLocked([]string{baseLocale}, key)
	i.mu.RUnlock()
	if !ok {
		return key
	}
	if out, err := msgfmt.Format(src, v, args, opts); err == nil {
		return out
	}
	if !enOK {
		return v
	}
	if out, err := msgfmt.Format(baseLocale, en, args, opts); err == nil {
		return out
	}
	return en
}

// Has reports whether key is a real, known translation key — present in any
// locale's base messages, plugin layer, or shop override — as opposed to
// arbitrary text that would otherwise ride T's fallback-to-key behaviour.
// Unlike T, this deliberately checks across ALL locales rather than
// resolving one: the question it answers is "does this key exist anywhere
// in the catalog", not "does it resolve for this locale" (a key can be a
// genuine, if untranslated-in-this-locale, key). Used to distinguish a real
// i18n key from user-controlled text before it reaches a template's
// `{{ T .errKey }}` (ut-docs#2148 — an unrecognised `?err=` query value
// otherwise renders verbatim in a page's error banner).
func (i *I18n) Has(key string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	for _, layer := range []map[string]map[string]string{i.messages, i.fresh, i.stale, i.overlays, i.shop} {
		for _, m := range layer {
			if _, ok := m[key]; ok {
				return true
			}
		}
	}
	return false
}

// SetShopOverrides atomically replaces all manager-edited translations
// (docs repo: architecture/translation-editor.md).
func (i *I18n) SetShopOverrides(shop map[string]map[string]string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if shop == nil {
		shop = map[string]map[string]string{}
	}
	i.shop = shop
	i.gen++
}

// TranslationEntry is one row of the translation editor: the key, its
// effective value for a locale, which layer supplied it, and the reference
// (fallback-locale) text shown beside the edit box.
type TranslationEntry struct {
	Key       string
	Value     string
	Source    string // "shop" | "base" | "plugin" | "" (untranslated)
	Reference string
}

// Entries lists every known key (base fallback + locale base + first-party
// bundles + third-party overlays + shop overrides) with its effective value
// for the locale, resolved in T's layer order (shop, fresh first-party,
// built-in, stale first-party, third-party).
//
// baseLocale is always scanned too, the same way T's resolution chain
// unconditionally falls back to it (ut-docs#995) — otherwise a shop whose
// store.locale (ut-docs#861) is a non-English, overlay-only locale (a
// language-pack plugin) missing a key that en.json has never gets that key
// listed at all, so the translation editor has no row for a translator to
// fill in (ut-docs#997). Shipped locales can't drift from en.json
// (guard-i18n.sh), but a language-pack plugin legitimately can
// (lang-pack-drift CI is advisory, not blocking, by design).
func (i *I18n) Entries(locale string) []TranslationEntry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	keys := map[string]bool{}
	for _, loc := range []string{i.fallback, baseLang(i.fallback), locale, baseLang(locale), baseLocale} {
		for _, layer := range []map[string]map[string]string{i.messages, i.fresh, i.stale, i.overlays, i.shop} {
			for k := range layer[loc] {
				keys[k] = true
			}
		}
	}
	out := make([]TranslationEntry, 0, len(keys))
	for k := range keys {
		ref := i.lookupLocked(i.fallback, k)
		if ref == "" {
			// i.fallback's own bundle can miss a key en.json has (same
			// overlay-drift case as above) — fall back to baseLocale so the
			// reference column isn't blank for a key that does exist in
			// English, mirroring T()'s terminal fallback.
			ref = i.lookupLocked(baseLocale, k)
		}
		e := TranslationEntry{Key: k, Reference: ref}
		e.Value, e.Source = i.lookupWithSourceLocked(locale, k)
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

func (i *I18n) lookupLocked(locale, key string) string {
	v, _ := i.lookupWithSourceLocked(locale, key)
	return v
}

// lookupWithSourceLocked mirrors T's precedence for ONE locale (no fallback
// chain — the editor shows what this locale itself defines).
func (i *I18n) lookupWithSourceLocked(locale, key string) (string, string) {
	for _, loc := range []string{locale, baseLang(locale)} {
		if v, src, ok := i.layerLookupLocked(loc, key); ok {
			return v, src
		}
	}
	return "", ""
}

// PluginLocales is everything installed plugins contribute to the
// translator, by layer (ADR-0160 §6). Each map is locale -> key -> message.
type PluginLocales struct {
	// FirstPartyFresh: first-party bundles generated after this binary's
	// built-in strings — they beat the built-in locale files.
	FirstPartyFresh map[string]map[string]string
	// FirstPartyStale: first-party bundles older than the binary, or with no
	// usable generated_at — they only fill gaps the built-in files leave.
	FirstPartyStale map[string]map[string]string
	// ThirdParty: every other plugin's locales/ — new locales and gaps only.
	ThirdParty map[string]map[string]string
}

// SetPluginLocales atomically replaces every plugin-provided layer (resynced
// on every plugin lifecycle change). It first drops any plugin string whose
// printf placeholders differ from English (ADR-0160 §5 — defence in depth
// behind the publish check): the key then falls through to the next layer,
// ending in English, never a broken format string. The caller's maps are
// never mutated.
func (i *I18n) SetPluginLocales(pl PluginLocales) {
	// Built-in files never change after NewI18nFS, so the check runs on a
	// snapshot outside the write lock: T() isn't blocked while every plugin
	// string goes through the placeholder regex on each reload.
	i.mu.RLock()
	coreEN := i.messages[baseLocale]
	i.mu.RUnlock()
	pluginEN := map[string]string{}
	for _, layer := range []map[string]map[string]string{pl.ThirdParty, pl.FirstPartyStale, pl.FirstPartyFresh} {
		for k, v := range layer[baseLocale] {
			pluginEN[k] = v
		}
	}
	check := func(layer map[string]map[string]string) map[string]map[string]string {
		out := make(map[string]map[string]string, len(layer))
		for loc, m := range layer {
			cp := make(map[string]string, len(m))
			for k, v := range m {
				ref, ok := coreEN[k]
				if !ok {
					ref, ok = pluginEN[k]
				}
				if ok && !samePlaceholders(ref, v) {
					log.Printf("i18n: dropped plugin string %q for %q: its placeholders differ from English (ADR-0160 §5)", k, loc)
					continue
				}
				cp[k] = v
			}
			out[loc] = cp
		}
		return out
	}
	fresh, stale, third := check(pl.FirstPartyFresh), check(pl.FirstPartyStale), check(pl.ThirdParty)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.fresh, i.stale, i.overlays = fresh, stale, third
	i.gen++
}

// SetOverlays replaces every plugin layer with third-party overlays only —
// shorthand for SetPluginLocales(PluginLocales{ThirdParty: overlays}).
func (i *I18n) SetOverlays(overlays map[string]map[string]string) {
	i.SetPluginLocales(PluginLocales{ThirdParty: overlays})
}

// printfVerb matches one fmt verb with optional explicit index, flags, width
// and precision (%s, %d, %[1]s, %-5.2f, %v, %q…). "%%" is a literal percent,
// not a placeholder, and is stripped before matching. The space flag is left
// out on purpose: prose such as "19%, 7% or 0% of" must not read as a "% o"
// verb, or every translation of it would be dropped. Known false positive:
// German "100%ig" reads as a %i verb, so such a translation of an English
// string with no verb is dropped (falls back to English) — reword it.
var printfVerb = regexp.MustCompile(`%(\[\d+\])?[-+#0]*(\d+|\*)?(\.(\d+|\*))?[a-zA-Z]`)

// samePlaceholders reports whether a and b carry the same multiset of printf
// verbs. Order may differ only through explicit indexes, which are part of
// the verb text, so "%[2]s %[1]s" still has to match English's own verbs.
func samePlaceholders(a, b string) bool {
	va := printfVerb.FindAllString(strings.ReplaceAll(a, "%%", ""), -1)
	vb := printfVerb.FindAllString(strings.ReplaceAll(b, "%%", ""), -1)
	if len(va) != len(vb) {
		return false
	}
	sort.Strings(va)
	sort.Strings(vb)
	for n := range va {
		if va[n] != vb[n] {
			return false
		}
	}
	return true
}

// Available returns the sorted locale codes with any translations.
func (i *I18n) Available() []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	seen := map[string]bool{}
	for loc, m := range i.messages {
		if len(m) > 0 {
			seen[loc] = true
		}
	}
	for _, layer := range []map[string]map[string]string{i.fresh, i.stale, i.overlays} {
		for loc, m := range layer {
			if len(m) > 0 {
				seen[loc] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for loc := range seen {
		out = append(out, loc)
	}
	sort.Strings(out)
	return out
}

// baseLang strips the region from a BCP 47 tag: "en-US" -> "en".
func baseLang(locale string) string {
	if idx := strings.IndexAny(locale, "-_"); idx > 0 {
		return locale[:idx]
	}
	return locale
}
