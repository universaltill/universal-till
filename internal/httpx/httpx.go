package httpx

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/manual"
	moneypkg "github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pihealth"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
	uiassets "github.com/universaltill/universal-till/web"
)

// CrossDeviceLinkActionable is the platform seam behind the
// crossdevicelinkactionable template func (ut-docs#390/#1057): defaults to
// the real selfupdate.DownloadLinkActionableNow() predicate (windows/darwin
// = true, unix kiosk = false), but is a var — not a direct call — so a test
// in another package that renders one of these templates can stub it to
// exercise BOTH the actionable and inactionable render paths regardless of
// the OS the test suite happens to run on, rather than only ever asserting
// whichever answer the test runner's own GOOS gives. Restore the original
// value (e.g. via t.Cleanup) after overriding it.
var CrossDeviceLinkActionable = selfupdate.DownloadLinkActionableNow

// UpdateInstallBridge is the platform seam behind the updateinstallbridge
// template func (ut-docs#1246): true only where the native shell can drive
// the OS package installer, which today means Android. A var — not a direct
// call — for the same reason as CrossDeviceLinkActionable above:
// a test rendering settings.html can then exercise BOTH branches instead of
// only the one its own GOOS gives, and the Android update UI is otherwise
// untestable on every machine that builds it (ut-docs#1534). Restore the
// original value (e.g. via t.Cleanup) after overriding it.
var UpdateInstallBridge = selfupdate.InstallBridgeAvailableNow

// UpdateAvailable is the seam behind the updateavailable template func
// (ut-docs#1545). Same reason as UpdateInstallBridge above: the update UI is
// now gated on BOTH platform and freshness, and without a seam a test cannot
// render the "an update exists" branch at all — it would depend on whatever
// the real GitHub API happened to say when the suite ran. Restore the original
// value (e.g. via t.Cleanup) after overriding it.
var UpdateAvailable = func() bool { return updates.Current().Available }

var baseFuncs = template.FuncMap{
	"div100":          func(cents int64) float64 { return float64(cents) / 100.0 },
	"bpPercent":       func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
	"appversion":      func() string { return buildinfo.Version },
	"updateavailable": func() bool { return UpdateAvailable() },
	"latestversion":   func() string { return updates.Current().Latest },
	"canselfupdate":   func() bool { return selfupdate.Supported() },
	// updatedownloadlink: whether the status-bar chip's fallback (when
	// canselfupdate is false) may show an actionable website link — false on
	// a unix kiosk, where that link is a dead end (ut-docs#147/#159). Mirrors
	// internal/pages/update_api.go's updateUnavailableHTML via the shared
	// selfupdate.DownloadLinkActionable predicate.
	"updatedownloadlink": func() bool { return selfupdate.DownloadLinkActionableNow() },
	// updateinstallbridge: Android only. The Go core can never self-swap
	// there (it ships as a native library inside the APK — only the package
	// installer may replace an app's own code), so canselfupdate is false by
	// design, but the native shell CAN drive that installer. Without this the
	// chip fell through to the unix-kiosk dead-end text and the operator was
	// told to reinstall by hand for every build (ut-docs#1246). Keep in step
	// with internal/pages/update_api.go's updateUnavailableHTML, which makes
	// the same distinction for the Settings page. Indirected through the
	// UpdateInstallBridge var above so a test can stub the platform seam.
	"updateinstallbridge": func() bool { return UpdateInstallBridge() },
	// crossdevicelinkactionable: whether a link to ANOTHER device (a replica
	// linking to its primary till's own UI, ut-docs#390) is safe to make
	// clickable — false on a unix kiosk (fullscreen, no chrome, no way back
	// once followed) or a desktop shell with no window-escape handling. Same
	// underlying platform signal as updatedownloadlink above (deliberately
	// not the same template-func name — that one reads as update-specific at
	// a call site that has nothing to do with updates); both wrap the one
	// shared selfupdate.DownloadLinkActionable predicate so there is still
	// only one place that decides "does this install have real, recoverable
	// browser chrome." Indirected through the CrossDeviceLinkActionable var
	// below rather than calling selfupdate.DownloadLinkActionableNow()
	// directly, so callers rendering this template from another package can
	// stub the platform seam instead of asserting whatever runtime.GOOS the
	// test happens to run on (ut-docs#1057).
	"crossdevicelinkactionable": func() bool { return CrossDeviceLinkActionable() },
	"enrolled":                  func() bool { return enroll.CurrentStatus().Registered },
	"enrolstore":                func() string { return enroll.CurrentStatus().StoreID },
	"enroldevice":               func() string { return enroll.CurrentStatus().DeviceID },
	// psuunderpowered: ut-docs#1232 — a Raspberry Pi whose power supply
	// can't negotiate 5V/5A restricts USB peripheral current (this till's
	// own touchscreen is USB) with no on-screen warning today; the status
	// bar's persistent chip surfaces internal/pihealth's local, offline
	// check. Always false on non-Pi platforms.
	"psuunderpowered": func() bool { return pihealth.Current().Underpowered },
	// pluginupdatesavailable/pluginupdatescount: StartPluginUpdateScheduler
	// (ut-docs#1953) publishes the count of installed-plugin updates still
	// waiting on a merchant decision — everything it didn't auto-apply
	// itself (a language pack auto-applies silently; anything else needs a
	// human). Same always-cheap, no-DB-query, offline-safe status-bar chip
	// pattern as updateavailable/latestversion above, just for plugins
	// instead of the core app.
	"pluginupdatesavailable": func() bool { return plugins.CurrentPendingUpdates().Count > 0 },
	"pluginupdatescount":     func() int { return plugins.CurrentPendingUpdates().Count },
	"jsonVals":               jsonVals,
	// Default target for the nav's contextual "?" — the manual's index.
	// Render() overrides this per request with the topic documenting the page
	// actually being rendered; fragment renderers that also parse nav.html
	// (internal/ui, RenderWith) keep this fallback rather than failing to
	// parse, which is why it lives in the base map at all.
	"helpHref": func() string { return "/help" },
	// Explicit contextual "?" for a SECTION of a page whose route is already
	// claimed by another topic (the settings cards). Locale-less fallback for
	// the same reason as helpHref above; FuncsFor overrides it locale-bound.
	"helpLink": func(id string) template.HTML { return helpLinkHTML(id, DefaultLocale()) },
	// {{ range railEntries }} — the nav rail's primary links, resolved from
	// uislot.CoreRail + the active `layout` plugins' rail-slot amendments
	// (ADR-0088, ut-docs#1912; rail.go). Locale-less fallback for the same
	// reason as helpHref/helpLink above (fragment renderers parse nav.html
	// with baseFuncs only); FuncsFor overrides it locale-bound.
	"railEntries": func() []RailEntry { return railEntriesFor(DefaultLocale()) },
	// {{ icon "lock" }} — inline SVG rail icons (icons.go, ut-docs#1423).
	"icon": iconHTML,
	// {{ dict "k" v ... }} — per-call parameters for a shared partial
	// (list_header.html / record_dialog.html, ut-docs#2010), so a page
	// adopts the list/edit standard with a template-only change.
	"dict": dict,
	// {{ $_ := req . "id" "formID" }} — the FIRST line of a shared partial:
	// an execute-time error naming the key when a required dict key is
	// absent or empty. Without it a misspelled key renders as "" at HTTP
	// 200 (Save bound to form="", a New button targeting no dialog) — see
	// req below.
	"req": req,
}

// dict builds a map for {{ template "x" (dict "k" v ...) }}. An odd-length
// argument list or a non-string key is an execute-time error rather than a
// half-built map. That is the ONLY loud failure dict gives: a key that is
// merely missing at the call site is NOT an error — html/template renders
// a missing map key as the empty string — which is why every partial that
// takes a dict guards its required keys with req on its first line
// (ut-docs#2010 review, B2).
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: want key/value pairs, got %d arguments", len(kv))
	}
	out := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is %T, want string", i/2, kv[i])
		}
		out[k] = kv[i+1]
	}
	return out, nil
}

// req is the required-key guard a dict-taking partial runs first:
// {{ $_ := req . "id" "formID" }}. It returns an error naming the first
// required key that is absent, nil, or an empty string, so a misspelled
// or forgotten key at a call site fails the render instead of shipping a
// partial that looks fine and does nothing (ut-docs#2010 review, B2: a
// missing formID rendered Save with form="" → getElementById("") → null →
// Save did nothing and the discard guard silently disabled, at HTTP 200).
// A nil dict (the partial called with no argument) fails on the first key.
// The empty string it returns is so a bare {{ req . "k" }} prints nothing.
func req(m map[string]any, keys ...string) (string, error) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			return "", fmt.Errorf("req: required key %q is missing from the dict", k)
		}
		if s, isStr := v.(string); isStr && s == "" {
			return "", fmt.Errorf("req: required key %q is empty", k)
		}
	}
	return "", nil
}

// helpLinkHTML renders the same .help-hint markup nav.html's automatic "?"
// carries (visual + a11y parity: translated title/aria-label via help.open,
// the shared data-testid), pointing at an explicitly named manual topic —
// {{ helpLink "backups" }} next to the backups card inside /settings, which
// display.md owns. This is deliberately NOT a competing routes: claim (the
// manual's duplicate-route guard forbids two topics on one route). An unknown
// id degrades to the manual's index rather than rendering a dead link, the
// same rule manual.HelpHref applies.
func helpLinkHTML(id, locale string) template.HTML {
	href := "/help"
	if lib := manual.Builtin(); lib != nil {
		if _, ok := lib.Topic(manual.FallbackLocale, id); ok {
			href = "/help/" + id
		}
	}
	label := template.HTMLEscapeString(T(locale, "help.open"))
	return template.HTML(fmt.Sprintf( //nolint:gosec // id is a repo topic slug (validated against the embedded manual above), label is escaped
		`<a class="help-hint" href="%s" title="%s" aria-label="%s" data-testid="help-hint">?</a>`,
		href, label, label))
}

// NewRenderer renders a layout + page (and optional partial) with funcs.
type Renderer struct {
	t *template.Template
}

// stripWebPrefix converts a caller-supplied disk-style path
// (filepath.Join("web", "ui", "pages", "x.html")) into the path used inside
// the embedded web.FS ("ui/pages/x.html", no "web/" prefix — the FS root
// already is web/). Callers throughout internal/pages still build paths the
// old way; this keeps that call-site code unchanged.
func stripWebPrefix(path string) string {
	path = filepath.ToSlash(path)
	return strings.TrimPrefix(path, "web/")
}

func stripWebPrefixes(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = stripWebPrefix(p)
	}
	return out
}

// NewRenderer has zero production call sites today (confirmed via
// `grep -rn "httpx.NewRenderer("` across the repo, ut-docs#1320 review) —
// dead code, not wired into anything. Deliberately NOT converted to the
// ClonedTemplate cache alongside every actually-invoked render path in this
// file: caching something nothing calls has no effect, and "fixing" dead
// code invites someone reading it to assume it's a live, exercised pattern.
// If this is ever wired up for real, route it through ClonedTemplate like
// Render/RenderPartial/RenderWith do, keyed on the (layout, page, partials)
// tuple the way ui.NewRenderer (internal/ui/buttons.go) already does.
func NewRenderer(layout string, page string, funcs template.FuncMap, partials ...string) (*Renderer, error) {
	// nav.html and bugreport_panel.html ride along automatically: base.html
	// references both on every page.
	files := []string{layout, page,
		filepath.Join("web", "ui", "partials", "nav.html"),
		filepath.Join("web", "ui", "partials", "bugreport_panel.html")}
	files = append(files, partials...)
	t, err := template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS, stripWebPrefixes(files)...)
	if err != nil {
		return nil, err
	}
	return &Renderer{t: t}, nil
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) error {
	return r.t.ExecuteTemplate(w, name, data)
}

// withHelpHref binds the nav's contextual "?" to the page being rendered.
//
// It has to be applied at every whole-page render path, not just Render() —
// most pages go through RenderWith with funcs built by FuncsFor, and binding
// it in only one of the two is how the "?" on /catalog silently degraded to
// the manual's index while the one on / worked. Copies the map rather than
// mutating the caller's, which may be shared across requests.
func withHelpHref(funcs template.FuncMap, r *http.Request) template.FuncMap {
	out := make(template.FuncMap, len(funcs)+1)
	maps.Copy(out, funcs)
	out["helpHref"] = func() string { return manual.HelpHref(r.URL.Path) }
	return out
}

// RenderWith builds a one-off renderer from explicit files and funcs.
func RenderWith(files []string, funcs template.FuncMap) func(name string, data any) http.HandlerFunc {
	stripped := stripWebPrefixes(files)
	// Cache key is the file set itself (ut-docs#1320): callers rebuild the
	// same literal file slice on every call (some per-request, e.g.
	// catalog/handlers.go's row_oob.go fragment renderers), so keying on
	// the joined paths — rather than trusting call sites to share one
	// cached RenderWith(...) result — is what makes every one of them hit
	// cache regardless of how the call site is structured. "\x00" can't
	// appear in a path, so this can't collide two different file sets.
	key := "httpx.RenderWith:" + strings.Join(stripped, "\x00")
	return func(name string, data any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// ut-docs#2162: "content" is this codebase's own established
			// name for "the page's content block, without base.html's
			// chrome" (see RenderContentFragment, whose entire job is the
			// same distinction) — i.e. a fragment-swap response, not a
			// full page. /catalog (catalog/handlers.go) and
			// /catalog/tax-codes (tax_codes_page.go) build their own file
			// sets and call RenderWith directly instead of
			// RenderContentFragment for exactly that reason (extra
			// partials RenderContentFragment's fixed set doesn't include),
			// so they need the same header-write RenderContentFragment
			// already does. "base" (a full standalone page) already gets
			// its <title> from base.html itself; skip it there.
			if name == "content" {
				writePageTitleHeader(w, data)
			}
			t, err := ClonedTemplate(key, "base.html", withHelpHref(funcs, r), stripped...)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := t.ExecuteTemplate(w, name, data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	}
}

var (
	i18nRef       atomic.Value // *common.I18n
	defaultLocale atomic.Value // string
	currencyCode  atomic.Value // string
)

func InitCurrency(code string) { currencyCode.Store(code) }

// minorUnits extracts an amount as integer minor units. Templates pass either
// the typed money.Money basket amounts or int64 display DTOs.
func minorUnits(amount any) (int64, bool) {
	switch v := amount.(type) {
	case moneypkg.Money:
		return v.Minor(), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	}
	return 0, false
}

func toJSON(v any) template.JS {
	b, _ := json.Marshal(v)
	return template.JS(string(b))
}

// jsonVals builds a JSON object literal from alternating key/value pairs, for
// an hx-vals attribute (htmx JSON.parses it). Deliberately returns a plain
// string, matching internal/ui.SearchResult.AddVals (the same fix, for the
// buttons_admin.html case) — this is the general-purpose version for
// templates that don't have a rich view-model struct to hang a bespoke
// Vals() method off of (ut-docs#19: buttons/catalog_variants/suggestions/
// self_order_grid/self_order_cart/basket previously interpolated raw fields
// into a hand-written JSON literal, invalid JSON for any quoted value, same
// class of bug AddVals fixed). NOTE: html/template's contextual escaper
// applies the same attribute-value escaping to a plain string here as it
// would to toJSON's template.JS in this specific attribute context — the
// two aren't observably different for hx-vals='...' today. A plain string is
// still the right choice: it doesn't rely on the escaper correctly
// classifying the surrounding markup as non-script (template.JS's
// "pre-approved" content is only actually safe inside an execution context
// like <script> or an inline event handler), so this can't silently become
// unsafe if a future edit moves a call site there.
func jsonVals(pairs ...any) (string, error) {
	if len(pairs)%2 != 0 {
		return "", fmt.Errorf("jsonVals: odd number of arguments (%d)", len(pairs))
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return "", fmt.Errorf("jsonVals: argument %d is a %T, not a string key", i, pairs[i])
		}
		m[key] = pairs[i+1]
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// InitI18n wires a translator and default locale into the template layer.
func InitI18n(t *config.I18n, fallback string) {
	i18nRef.Store(t)
	defaultLocale.Store(fallback)
}

// SetDefaultLocale updates the till's configured default locale live (e.g.
// a manager changing it in Settings, ut-docs#861) — unlike InitI18n, this
// does NOT touch the wired translator: config.I18n already loads every
// shipped locale's strings at boot, so switching the default is just moving
// which one ResolveLocale()/DefaultLocale() fall back to when no
// request-scoped ?lang=/ut_lang preference is set. Empty locale is a no-op:
// callers are expected to validate against AvailableLocales() first (an
// unconditional store would let an empty/invalid submission silently blank
// the till's configured default for every background job — notification
// email, in particular — that has no request to resolve a locale from).
func SetDefaultLocale(locale string) {
	if locale == "" {
		return
	}
	defaultLocale.Store(locale)
}

var uiScale atomic.Value // float64

// InitUIScale sets the interface scale factor for the POS screen
// (UT_UI_SCALE, e.g. 0.8 for small 1024px tills, 1.3 for large displays).
// Everything is rem-based, so scaling the root font-size scales the whole UI.
func InitUIScale(scale float64) {
	if scale < 0.5 || scale > 2.0 {
		scale = 1.0
	}
	uiScale.Store(scale)
}

var oskMode atomic.Value // string: auto|on|off

// InitOSKMode publishes the on-screen keyboard mode to templates (data-osk
// on <body>). auto = keyboard only on touch screens (pointer: coarse).
func InitOSKMode(mode string) {
	switch mode {
	case "on", "off", "auto":
	default:
		mode = "auto"
	}
	oskMode.Store(mode)
}

func oskModeVal() string {
	if v, ok := oskMode.Load().(string); ok && v != "" {
		return v
	}
	return "auto"
}

// idleLockSecs drives the cosmetic client-side idle timer (data-idle-lock on
// <body>); 0/unset renders no attribute. The server-side check in auth.Service
// is authoritative — pages.Init keeps both in sync.
var idleLockSecs atomic.Int64

// InitIdleLock publishes the idle auto-lock window to templates (minutes;
// 0 disables). Called at startup and when the setting changes.
func InitIdleLock(minutes int) {
	if minutes < 0 {
		minutes = 0
	}
	idleLockSecs.Store(int64(minutes) * 60)
}

func currentUIScale() float64 {
	scale := 1.0
	if v := uiScale.Load(); v != nil {
		if f, ok := v.(float64); ok {
			scale = f
		}
	}
	return scale
}

func uiScalePx() string {
	return strconv.FormatFloat(16*currentUIScale(), 'f', -1, 64)
}

// uiScaleCSS exposes the raw clamped multiplier (not pre-multiplied by a
// fixed 16px) for pages whose root font-size is a CSS-driven fluid/viewport
// value rather than a server-computed px (ut-docs#161's sale screen) — the
// stylesheet combines it with the fluid baseline via
// calc(var(--ui-scale) * var(--fluid-fs)).
func uiScaleCSS() string {
	return strconv.FormatFloat(currentUIScale(), 'f', -1, 64)
}

// IsRTL reports whether a locale reads right-to-left (language prefix match,
// so "fa", "fa-IR", "ar-SA" all qualify).
func IsRTL(locale string) bool {
	lang := strings.ToLower(locale)
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	switch lang {
	case "ar", "fa", "he", "ur", "ps", "ckb", "dv", "yi":
		return true
	}
	return false
}

// NativeLanguageName renders a locale code in its own language ("de" →
// "Deutsch", "fa" → "فارسی") via x/text's CLDR self-names — fully offline, no
// lookup service. Falls back to the raw code when the tag is unknown.
// ut-docs#1125: the one native-name source for this codebase, shared by the
// core-locale picker (setup/settings/menu, this file's "nativelocalename"
// template func) and the plugin-catalog install tiles
// (pages.setupLanguageCatalogEntries) — deliberately not duplicated, so the
// two pickers can never drift on what a code renders as.
func NativeLanguageName(code string) string {
	tag, err := language.Parse(code)
	if err != nil {
		return code
	}
	if n := display.Self.Name(tag); n != "" {
		return n
	}
	return code
}

// translator returns the wired i18n, or nil. The typed assertion matters:
// InitI18n(nil, ...) stores a typed nil *config.I18n, which an interface
// nil-check alone would treat as present and then panic on method call.
func translator() *config.I18n {
	t, _ := i18nRef.Load().(*config.I18n)
	return t
}

// T translates a key for a locale outside templates (handlers building toasts
// or fragments). Falls back to the key itself, mirroring the template func.
func T(locale, key string) string {
	if t := translator(); t != nil {
		return t.T(locale, key)
	}
	return key
}

// genericErrKey is the fallback QueryErrKey renders in place of unrecognised
// `?err=` text. Reuses the existing shared `common.error.server` key rather
// than minting a new one — same reasoning as ut-docs#1663/#1620's own
// error-message migrations: it avoids new-key churn across all four locales
// plus the external ut-plugin-language-{de,es} packs, and no touched page's
// own namespace has a better-fitting existing "something went wrong" key.
const genericErrKey = "common.error.server"

// QueryErrKey reads a page's conventional `?err=` query parameter and
// returns it only if it resolves to a real i18n key; otherwise it returns
// genericErrKey. Several pages pass r.URL.Query().Get("err") straight
// through to `{{ T .errKey }}` for their error banner, and T's own
// fallback-to-key behaviour then renders WHATEVER text follows `?err=`
// verbatim — not exploitable as XSS (html/template still escapes the text
// node) but a spoofing/social-engineering vector: a crafted link can make
// the till's own UI display an attacker-chosen "official-looking" message
// (ut-docs#2148). Centralizing the check here, rather than validating in
// each of the ~10 handlers that read this parameter, is deliberate: it's
// the one choke point every one of them already funnels through.
// No translator wired (a test that never called InitI18n) can't verify a
// key either way, so this preserves T's own existing nil-safety and passes
// the raw value through unchanged rather than failing closed on every such
// test.
func QueryErrKey(r *http.Request) string {
	return queryBannerKey(r, "err")
}

// QueryMsgKey is QueryErrKey's success-banner counterpart (ut-docs#2148
// review finding): fiscal_device_page.go's `?msg=` feeds a "login-ok"
// success banner through the identical `{{ T .msgKey }}` fallback-to-key
// hazard — same fix, different query parameter and banner styling.
func QueryMsgKey(r *http.Request) string {
	return queryBannerKey(r, "msg")
}

// queryBannerKey backs both QueryErrKey and QueryMsgKey: reads the named
// query parameter and returns it only if it resolves to a real i18n key.
func queryBannerKey(r *http.Request, param string) string {
	key := r.URL.Query().Get(param)
	if key == "" {
		return ""
	}
	if t := translator(); t != nil && !t.Has(key) {
		return genericErrKey
	}
	return key
}

// AvailableLocales returns the locales the base translation files define
// (config.I18n.Available() — the same set the UI's own language switcher is
// built from), or nil if no translator is wired (e.g. a test that never
// called InitI18n). For validating a locale value before trusting it for
// something beyond template rendering — e.g. ut-docs#397's issue-report
// capture, which forwards it to a downstream transcription service, so an
// untrusted `?lang=`/cookie value shouldn't reach it unchecked.
func AvailableLocales() []string {
	if t := translator(); t != nil {
		return t.Available()
	}
	return nil
}

// localeCookie is the per-browser language override a ?lang= link leaves
// behind. Its value is "<locale>|<shop default at the time>|<locale
// generation at the time>" — see LocaleOverride for why the two trailing
// fields exist.
const localeCookie = "ut_lang"

// localeGeneration counts the times the shop has EXPLICITLY set its
// language (Settings' Language card, a store.locale write from the
// all-settings table, finishing the setup wizard). It is persisted in
// store.locale_generation and reloaded at boot, so it survives restarts —
// a counter that reset to zero would silently re-validate every override
// it had retired.
var localeGeneration atomic.Int64

// SetLocaleGeneration publishes the shop's current locale generation
// (ut-docs#2135). Called at boot from the persisted value, and again each
// time the shop's language is explicitly set.
func SetLocaleGeneration(n int64) { localeGeneration.Store(n) }

// LocaleGeneration returns the shop's current locale generation.
func LocaleGeneration() int64 { return localeGeneration.Load() }

// ResolveLocale determines the locale from query, cookie, then default.
func ResolveLocale(w http.ResponseWriter, r *http.Request) string {
	// query param takes precedence and sets cookie
	if lang := r.URL.Query().Get("lang"); lang != "" {
		http.SetCookie(w, &http.Cookie{
			Name: localeCookie,
			// Record what this choice was made against, so a later
			// shop-level change can tell a live preference from a stale
			// one (ut-docs#2135).
			Value:    LocaleOverrideValue(lang),
			Path:     "/",
			MaxAge:   31536000, // 1 year
			HttpOnly: false,
		})
		return lang
	}
	return RequestLocale(r)
}

// LocaleOverrideValue builds the ut_lang cookie value for a chosen locale:
// the locale itself, plus the two things it is only valid relative to. The
// single definition of the cookie's format — construct it nowhere else.
func LocaleOverrideValue(lang string) string {
	return lang + "|" + DefaultLocale() + "|" + strconv.FormatInt(LocaleGeneration(), 10)
}

// LocaleOverride returns this request's per-browser language override and
// whether it is still valid.
//
// An override is a preference *relative to* what the shop was showing when it
// was made, so it is honoured only while both of those things still hold
// (ut-docs#2135):
//
//   - the shop default it was recorded against is still the shop default, and
//   - the shop has not explicitly set its language since (the generation).
//
// The generation is what makes Settings → Language authoritative for the
// WHOLE shop rather than only for the browser the save happened to be made
// from. Re-applying the language the shop is already set to leaves the
// default unmoved, so the first check alone would let the override stand —
// and that is exactly the state a confused operator is in: the screen shows
// the wrong language, Settings already shows the right one, and re-applying
// it was their only move. It also means a manager can fix a till from their
// own phone, which a Set-Cookie on the responding request cannot do.
//
// A cookie with fewer than three fields is a pre-#2135 one. It carries no
// evidence of what it was chosen against, so it cannot be trusted over an
// explicit shop setting and is ignored: affected tills heal themselves on
// upgrade instead of waiting for an operator to find a control that, before
// this change, could not clear it anyway. The cost is that a deliberate
// pre-upgrade per-browser choice is forgotten once, and has to be re-picked.
func LocaleOverride(r *http.Request) (string, bool) {
	c, err := r.Cookie(localeCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	// Cut at the FIRST separator, so a ?lang= value containing one of its
	// own cannot forge the trailing fields: "en|de|9" arrives as
	// "en|de|9|<real base>|<real gen>" and fails the field count below.
	lang, rest, ok := strings.Cut(c.Value, "|")
	if !ok || lang == "" {
		return "", false // legacy, pre-#2135
	}
	base, genStr, ok := strings.Cut(rest, "|")
	if !ok || base == "" || genStr == "" {
		return "", false
	}
	gen, err := strconv.ParseInt(genStr, 10, 64)
	if err != nil {
		return "", false
	}
	if base != DefaultLocale() || gen != LocaleGeneration() {
		return "", false // the shop has moved on since
	}
	return lang, true
}

// RequestLocale is ResolveLocale without the side effect: the same
// query-param → cookie → default resolution, but it never writes the
// ut_lang cookie. For a handler that needs the locale BEFORE it calls
// Render (which resolves again, and would otherwise emit a second
// Set-Cookie for the same ?lang= — menu_page.go's label fallback,
// ADR-0088 Decision G).
func RequestLocale(r *http.Request) string {
	if lang := r.URL.Query().Get("lang"); lang != "" {
		return lang
	}
	// cookie — only while it is still a live preference, not a stale one
	// (ut-docs#2135)
	if lang, ok := LocaleOverride(r); ok {
		return lang
	}
	// default
	if v := defaultLocale.Load(); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "en"
}

// DefaultLocale returns the till's configured locale — for output that has
// no request to resolve from (background prints, scheduled jobs).
func DefaultLocale() string {
	if v := defaultLocale.Load(); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "en"
}

var kioskMode atomic.Value // bool

// InitKiosk marks the process as running on a dedicated till (larger touch
// targets, no text selection). Driven by UT_KIOSK=1.
func InitKiosk(on bool) { kioskMode.Store(on) }

// selfOrderMode backs the "selforder" template func — mirrors kioskMode
// above exactly, but for a different axis: the per-till display.mode
// setting (ADR-0020), not the UT_KIOSK window-chrome flag. ut-docs#2099
// (the implementation half of ut-docs#1999, coding-standards.md §10) reads
// it so a shared partial (record_dialog.html) can withhold the status/
// lock/exit affordance for customer containment while a device is in
// self-order kiosk mode, without every page threading the flag through its
// own template dict by hand. Published at boot from the persisted
// display.mode (pages.Init) and live-updated the moment an operator flips
// it (settings_page.go's POST /api/settings/display-mode) — same two call
// sites InitOSKMode/oskModeVal already follow for the on-screen-keyboard
// mode.
var selfOrderMode atomic.Value // bool

// InitSelfOrderMode publishes whether this till is currently in self-order
// kiosk mode (display.mode="self_order") to templates via the "selforder"
// func below.
func InitSelfOrderMode(on bool) { selfOrderMode.Store(on) }

// displayMode backs the "backtosaleurl" template func — same publish
// pattern as selfOrderMode above (kept as a separate atomic rather than
// derived from this one so neither call site's existing behavior changes),
// but carrying the raw display.mode value ("", "backoffice", "self_order")
// rather than a single bool, since RenderError (ut-docs#2154) needs to
// choose between three destinations, not just detect self-order.
// RenderError has no *common.Deps in scope — it's called from ~80 sites
// across many packages — so it can't read display.mode fresh per request
// the way index_page.go/open_orders_page.go do; this cached value is how
// error_page.html's "Back to sale" link gets the same mode-aware
// destination those two pages already have (saleScreenReturnURL,
// internal/pages/index_page.go) without threading Deps through every one
// of those 80 call sites.
var displayMode atomic.Value // string

// InitDisplayMode publishes the current display.mode value for the
// "backtosaleurl" template func below. Published at boot (pages.Init) and
// live-updated at the same call sites InitSelfOrderMode already is.
func InitDisplayMode(mode string) { displayMode.Store(mode) }

// saleScreenReturnURLFor mirrors internal/pages/index_page.go's
// saleScreenReturnURL exactly (same two ADR-driven exceptions: ADR-0018
// backoffice is a landing preference an explicit action opts out of,
// ADR-0020 self-order containment is not) — duplicated rather than
// imported because internal/pages already imports internal/httpx, so the
// reverse import would cycle. Keep both in step if the destinations ever
// change.
func saleScreenReturnURLFor(mode string) string {
	switch mode {
	case "self_order":
		return "/self-order"
	case "backoffice":
		return "/?stay=1"
	default:
		return "/"
	}
}

// assetVersion returns a cache-busting version for a web asset: the file's
// mtime, so browsers pick up redesigns without a manual hard refresh.
// imgVersion appends a cache-busting mtime to a /public/... URL so replacing
// a file (e.g. an item image upload) shows immediately despite browser cache.
func imgVersion(url string) string {
	if rel, ok := strings.CutPrefix(url, "/"); ok && strings.HasPrefix(rel, "public/") {
		return url + "?v=" + assetVersion(rel)
	}
	return url
}

func assetVersion(rel string) string {
	if info, ok := statAsset(rel); ok {
		return strconv.FormatInt(info.ModTime().Unix(), 10)
	}
	return strconv.FormatInt(bootTime, 10)
}

// statAsset looks up a web asset by its path relative to web/ or the public
// data dir. Uploaded assets (item/variant photos, receipt logo) live in the
// stable per-user data dir (see internal/paths), not the cwd-relative release
// tree — check there first so a re-uploaded file wins over a stale built-in
// one. Falls back to the cwd-relative path for built-in assets shipped in web/.
func statAsset(rel string) (os.FileInfo, bool) {
	if info, err := os.Stat(paths.Data(rel)); err == nil {
		return info, true
	}
	if info, err := os.Stat(filepath.Join("web", rel)); err == nil {
		return info, true
	}
	return nil, false
}

var bootTime = time.Now().Unix()

// imgExists reports whether a /public/... URL (the same form imgv takes)
// resolves to a real file the /public/ static handler would actually serve.
// Used to skip rendering an <img> at all for assets that may not exist
// (e.g. an item's thumb.png before any photo is added) — an unconditional
// <img src> makes the browser issue a real, logged, always-doomed request
// for every such item (ut-docs#319).
//
// Deliberately NOT just statAsset: that only checks the stable data dir and
// the CWD-relative release tree, which is exactly right for cache-busting
// (an embedded default's version can't change until the next build/boot
// anyway) but wrong for existence — it would report false for a bundled
// default asset (e.g. a seeded demo item's thumb.png) whenever the process
// isn't running from the repo/install root, which real packaged installs
// routinely aren't (internal/pages/static_page.go's fallbackFS is why /public/
// itself still finds these). So this checks all three tiers /public/ does:
// stable data dir, on-disk release tree, then the binary's embedded default.
func imgExists(url string) bool {
	rel, ok := strings.CutPrefix(url, "/")
	if !ok {
		return false
	}
	if _, ok := statAsset(rel); ok {
		return true
	}
	if _, err := fs.Stat(uiassets.FS, rel); err == nil {
		return true
	}
	return false
}

// FuncsFor builds template funcs for a specific request/locale.
func FuncsFor(locale string) template.FuncMap {
	funcs := template.FuncMap{}
	for k, v := range baseFuncs {
		funcs[k] = v
	}
	// locale-bound: money digits and separators follow the request locale
	// (fa/ar render ۱۲٬۳۴۵), the symbol/word and decimals follow the
	// configured currency (see currency.go).
	funcs["money"] = func(amount any) string {
		cents, ok := minorUnits(amount)
		if !ok {
			return ""
		}
		return FormatMoney(cents, locale)
	}
	// {{ date .IssuedAt }}: date-ordering convention follows the request
	// locale (de-DE renders 06.09.2026, en-US renders 09/06/2026), digit
	// shape follows locale too, same as money above (ut-docs#1130). Accepts
	// either a time.Time or an RFC3339 string (the storage format for
	// stamped-at-write timestamps like invoices.IssuedAt), both rendered in
	// LOCAL time — right for a wall-clock event like "when this was issued
	// at the till". An unparseable string is returned unchanged (not "") so
	// a caller accidentally handed a non-RFC3339 string (e.g. an already
	// "2006-01-02"-shaped date) degrades to its raw form, still visible,
	// rather than a silently blank cell.
	funcs["date"] = func(v any) string {
		switch t := v.(type) {
		case time.Time:
			return FormatDate(t.Local(), locale)
		case string:
			parsed, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return t
			}
			return FormatDate(parsed.Local(), locale)
		default:
			return ""
		}
	}
	// {{ dateUTC .IssuedAt }}: FormatDate's date-ordering/digit-shape
	// convention, WITHOUT the Local() conversion `date` above applies —
	// for a value compared/filtered elsewhere in UTC (e.g. invoices.html's
	// register list, whose from/to range is deliberately compared against
	// the raw UTC IssuedAt string — see invoice_page.go's own comment on
	// why `to` is left open). Using `date`'s Local conversion there would
	// show a calendar date that can legitimately disagree with which
	// from/to bucket the row is actually in.
	funcs["dateUTC"] = func(v string) string {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return v
		}
		return FormatDate(parsed, locale)
	}
	// {{ datetime .CreatedAt }}: FormatDateTime — `date`'s date-ordering/
	// digit-shape convention plus a 24-hour clock, in LOCAL time, for a
	// per-event timestamp where the time of day matters (a journal row, an
	// audit entry) — as opposed to `date`'s date-only rendering (ut-docs#1632).
	// Same accepted-value contract and same-string-back-on-parse-failure
	// behavior as `date` above.
	funcs["datetime"] = func(v any) string {
		switch t := v.(type) {
		case time.Time:
			return FormatDateTime(t.Local(), locale)
		case string:
			parsed, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return t
			}
			return FormatDateTime(parsed.Local(), locale)
		default:
			return ""
		}
	}
	// {{ datetimeUTC .CreatedAt }}: `datetime`'s convention WITHOUT the
	// Local() conversion — same reasoning as `dateUTC` above.
	funcs["datetimeUTC"] = func(v string) string {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return v
		}
		return FormatDateTime(parsed, locale)
	}
	// {{ thousandssep }} / {{ decimalsep }}: the same grouping/decimal
	// convention `money` above follows server-side, exposed for
	// window.utCurrency's client-side money formatting (web/public/app.js)
	// so a de-DE till doesn't show "€1.234,56" server-rendered next to
	// "€1,234.56" client-rendered on the same screen (ut-docs#1130).
	funcs["thousandssep"] = func() string {
		t, _ := numberSeparators(locale)
		return string(t)
	}
	funcs["decimalsep"] = func() string {
		_, d := numberSeparators(locale)
		return string(d)
	}
	funcs["currency"] = ActiveCurrency // {{ currency.Code }} etc.
	funcs["currencies"] = Currencies   // the Settings picker options
	// {{ moneypattern currency.Decimals false }} / {{ moneyplaceholder currency.Decimals 50 }}
	// -- shared decimals-generic pattern/placeholder ATTRIBUTES (the whole
	// `pattern="…"`/`placeholder="…"`, not just the value -- see
	// MoneyPatternAttr's doc comment for why) for a decimal-mode money
	// <input>, so a future 3-decimal currency (KWD/BHD/OMR) doesn't need
	// every call site's own hardcoded {1,2} updated (ut-docs#1274).
	funcs["moneypattern"] = MoneyPatternAttr
	funcs["moneyplaceholder"] = MoneyPlaceholderAttr
	funcs["toJson"] = toJSON
	funcs["assetv"] = assetVersion
	funcs["imgv"] = imgVersion
	funcs["imgExists"] = imgExists
	funcs["kiosk"] = func() bool {
		if v := kioskMode.Load(); v != nil {
			b, _ := v.(bool)
			return b
		}
		return false
	}
	// ut-docs#2099: whether THIS device is currently in self-order kiosk
	// mode (display.mode="self_order", ADR-0020) — the axis record_dialog.html
	// checks to withhold its status/lock/exit affordance (coding-standards.md
	// §10: customer containment is the one place those three must NOT be
	// reachable). Same shape as "kiosk" just above; a different underlying
	// atomic (selfOrderMode, not kioskMode) since the two are unrelated
	// settings that happen to share the word "kiosk" — see selfOrderMode's
	// own doc comment.
	funcs["selforder"] = func() bool {
		if v := selfOrderMode.Load(); v != nil {
			b, _ := v.(bool)
			return b
		}
		return false
	}
	// ut-docs#2154: error_page.html's "Back to sale" link — RenderError has
	// no *common.Deps in scope to read display.mode fresh, so it reads the
	// same cached value InitDisplayMode publishes (see its own doc comment).
	funcs["backtosaleurl"] = func() string {
		mode, _ := displayMode.Load().(string)
		return saleScreenReturnURLFor(mode)
	}
	funcs["uiscalepx"] = uiScalePx
	funcs["uiscale"] = uiScaleCSS
	funcs["oskmode"] = oskModeVal
	funcs["idlelocksecs"] = func() int64 { return idleLockSecs.Load() }
	funcs["barcodesvg"] = BarcodeSVG // scannable CODE39 for receipt numbers
	funcs["locale"] = func() string { return locale }
	// defaultlocale is the shop's configured DEFAULT locale (Settings'
	// Language card, ut-docs#861) — distinct from "locale" above, which is
	// this specific request's resolved locale (?lang=/ut_lang cookie). A
	// manager's own browser can be on a different language than the shop's
	// configured default without the picker showing the wrong selection.
	// Normalized to the bare language prefix (same rule IsRTL already
	// applies) rather than returning DefaultLocale() raw: a till that has
	// never had its language explicitly changed carries the env-derived
	// UT_DEFAULT_LOCALE default, which is a full BCP-47 tag like "en-US" —
	// the picker's own options (AvailableLocales()) are always the bare
	// shipped-locale codes ("en", "ar", ...), so an unnormalized comparison
	// would show NO option selected on a till nobody has ever touched this
	// setting on. httpx.DefaultLocale() itself is left raw for callers that
	// need the real tag (e.g. alerts.go's notification push).
	funcs["defaultlocale"] = func() string {
		lang := strings.ToLower(DefaultLocale())
		if i := strings.IndexAny(lang, "-_"); i > 0 {
			lang = lang[:i]
		}
		return lang
	}
	// dir drives <html dir=…> so RTL locales lay out right-to-left.
	funcs["dir"] = func() string {
		if IsRTL(locale) {
			return "rtl"
		}
		return "ltr"
	}
	funcs["locales"] = func() []string {
		if t := translator(); t != nil {
			return t.Available()
		}
		return []string{"en"}
	}
	// ut-docs#1125: renders a core locale code (e.g. "ar") as its native name
	// ("العربية") for the setup wizard / settings / staff menu page (/menu)
	// language pickers — deliberately no flag (a language isn't a country; see the
	// ticket's recorded research). Left as bare "locales" for callers that
	// need the raw code (comparisons, the ?lang= href).
	funcs["nativelocalename"] = NativeLanguageName
	funcs["T"] = func(key string) string {
		if t := translator(); t != nil {
			return t.T(locale, key)
		}
		return key
	}
	// Locale-bound override of the baseFuncs fallback: the section "?" label
	// translates with the page it sits on.
	funcs["helpLink"] = func(id string) template.HTML { return helpLinkHTML(id, locale) }
	// Locale-bound override of the baseFuncs fallback: a `layout` plugin's
	// re-label falls back to the core label per THIS request's locale
	// (ADR-0088 Decision G). The rail is the one per-request value
	// nav.html needs besides helpHref — bound here rather than beside
	// helpHref in withHelpHref because, unlike the "?" (which needs
	// r.URL.Path), it depends only on the locale FuncsFor already binds
	// and on process-wide plugin state (railAmendmentsSource): every
	// whole-page render path (Render, RenderContentFragment, RenderWith,
	// RenderError) builds its funcs through FuncsFor, so every page that
	// renders nav gets it.
	funcs["railEntries"] = func() []RailEntry { return railEntriesFor(locale) }
	// tenderLabel translates the sentinel values pos.deriveTenderType can
	// itself produce ("unknown" — no payments at all, e.g. a zero-marginal-
	// net partial refund, ut-docs#1579; "split" — more than one distinct
	// payment method on the sale) through the locale table. Any other value
	// is a payment plugin's own MethodID (cash, card, voucher, sumup, ...) —
	// an open-ended set this till doesn't own the vocabulary for — and is
	// rendered as-is, unchanged from before this function existed.
	funcs["tenderLabel"] = func(tenderType string) string {
		var key string
		switch tenderType {
		case "unknown":
			key = "journal.tender.unknown"
		case "split":
			key = "journal.tender.split"
		default:
			return tenderType
		}
		if t := translator(); t != nil {
			return t.T(locale, key)
		}
		return tenderType
	}
	return funcs
}

func NewMux() *http.ServeMux { return http.NewServeMux() }

// renderFiles is the fixed file set every Render() call shares — only the
// page itself varies per call site, so the cache key only needs to vary on
// page (ut-docs#1320).
var renderFiles = []string{
	"ui/layouts/base.html",
	"ui/partials/nav.html",
	"ui/partials/buttons.html",
	"ui/partials/buttons_admin.html",
	"ui/partials/basket.html",
	"ui/partials/plugin_install_modal.html",
	"ui/partials/plugin_manual_import.html",
	"ui/partials/help_topic.html",
	"ui/partials/help_nav.html",
	"ui/partials/bugreport_panel.html",
	// ut-docs#1174: the Settings TSE provisioning block lives in a partial
	// so POST /api/settings/retry-tse-provisioning can re-render exactly the
	// same markup standalone (RenderPartial) for its htmx swap; riding along
	// here is what lets settings.html include it by file name.
	"ui/partials/tse_provisioning_block.html",
	// ut-docs#1613: same reasoning — POST /api/backup/restore's success
	// response AND settings.html's own page render (when a restore is
	// already staged from an earlier visit) both need this exact markup,
	// so it's a partial riding along here rather than duplicated inline.
	"ui/partials/backup_restore_staged.html",
	// ut-docs#1060: same reasoning again — GET /ui/settings/window-mode-status's
	// own htmx poll response AND settings.html's page render both need this
	// exact markup, so it's a partial riding along here too.
	"ui/partials/window_mode_status.html",
	// ut-docs#1950: items.html's own content template includes this by its
	// {{ define "items_rail" }} name (same as help_topic.html/help_nav.html
	// above) — riding along here is what lets that work through the plain
	// httpx.Render("ui/pages/items.html", ...) call site, with no bespoke
	// RenderWith file set of its own.
	"ui/partials/items_rail.html",
	// ut-docs#1957: modifiers.html's own "content"/"modifiers_list" templates
	// call {{ template "modifier_group_admin" ... }} — riding along here is
	// what lets that resolve through the plain httpx.Render("ui/pages/
	// modifiers.html", ...)/RenderContentFragment call sites, same as
	// items_rail.html above.
	"ui/partials/modifier_group_admin.html",
	// ut-docs#2119: the /catalog + /inventory category-filter chip row.
	// /catalog goes through its own bespoke RenderWith file set (see
	// catalog/handlers.go), but /inventory renders through the plain
	// httpx.Render("ui/pages/inventory.html", ...) call site above, same
	// riding-along mechanism as items_rail.html/modifier_group_admin.html.
	"ui/partials/category_filter.html",
	// ut-docs#2010: the app-wide list/edit standard's two partials
	// (ut-docs/reference/list-and-dialog-pattern.md). Same mechanism as
	// items_rail.html above — a page includes them by their {{ define }}
	// names through the plain Render/RenderContentFragment call sites. Note
	// the slot contract this depends on: this set is fixed and only the
	// PAGE varies per call, so every page is parsed into its own template
	// set, and two pages may each {{ define "record_dialog_fields" }} with
	// no collision. record_dialog.html therefore ships NO default for its
	// slots (definition order across ParseFiles would silently pick a
	// winner) — a page that uses it must define both, see the partial.
	"ui/partials/list_header.html",
	"ui/partials/record_dialog.html",
	// ut-docs#2008: web/ui/pages/admin.html includes this by its
	// {{ define "admin_tree" }} name — same riding-along mechanism as
	// items_rail.html above, and the only call site today.
	"ui/partials/admin_tree.html",
}

// ut-docs#2020: web/ui/partials/record_dialog_msg.html is deliberately NOT
// in renderFiles above — unlike list_header.html/record_dialog.html, it is
// never included by name from inside another page's template set (the
// in-dialog message region's wrapper element lives directly in
// record_dialog.html now, so the aria-live node itself is never
// re-rendered — see that partial's own header comment for why). This file
// exists solely to be parsed and executed on its own, standalone, via
// httpx.RenderPartial for a refused mutation's htmx response
// (categories_page.go's renderCategoryDialogError). ClonedTemplate parses
// exactly the one file RenderPartial names, so it needs no place in this
// shared set at all.

// Render full page with layout + page + common partials
func Render(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := stripWebPrefix(tplPath)

		locale := ResolveLocale(w, r)
		files := append([]string{renderFiles[0], page}, renderFiles[1:]...)
		t := template.Must(ClonedTemplate("httpx.Render:"+page, "base.html", withHelpHref(FuncsFor(locale), r), files...))
		if err := t.ExecuteTemplate(w, "base", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// pageTitleHeader is how RenderContentFragment/RenderPartial tell the
// shared client-side htmx:afterSwap listener (web/public/app.js) what
// document.title should become after the fragment they're answering gets
// swapped in — see writePageTitleHeader's own comment for the full "why".
const pageTitleHeader = "X-UT-Page-Title"

// writePageTitleHeader sets pageTitleHeader from data's "title" field, when
// present and non-empty, so an in-panel htmx swap (/items, /admin,
// /help/{topic}) can refresh document.title (ut-docs#2162) — base.html's
// own <title> tag only ever renders on a full-page response, never on a
// bare fragment, so a swap that changes what's showing left the browser
// tab's title stale.
//
// Percent-encoded (net/url.PathEscape), not written raw: browsers' Fetch/
// XHR getResponseHeader() reads header values as Latin-1, not UTF-8, so a
// literal non-ASCII title (this product ships ar/fa/tr locales) would come
// back corrupted on the client. The paired client-side decodeURIComponent
// reverses it.
//
// PathEscape, deliberately NOT QueryEscape: QueryEscape follows
// application/x-www-form-urlencoded and encodes a space as "+", a
// convention only QueryUnescape (or a form/query decoder) reverses —
// JavaScript's decodeURIComponent (the paired client-side call, in
// web/public/app.js) only unescapes "%XX" sequences and leaves a literal
// "+" untouched, so every multi-word title ("Country settings", "Fiscal
// register", "Tax codes", …) would have round-tripped as
// "Country+settings" — a real, visible bug caught in review, not by the
// original tests (they exercised a non-ASCII title with no space in it).
// PathEscape encodes a space as "%20", which decodeURIComponent does
// reverse correctly, and escapes control characters (CR/LF included) the
// same way QueryEscape does, so the no-header-injection guarantee holds.
//
// A no-op for the great majority of RenderPartial's call sites, whose data
// isn't a title-bearing map[string]any at all (dialog messages, OOB
// refreshes, small in-place fragments) — same "absent = do nothing"
// contract as record-dialog.js/category-filter.js's own attribute-driven
// listeners. Must run before the template executes: a header can't be set
// once body bytes have started writing.
func writePageTitleHeader(w http.ResponseWriter, data any) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	title, ok := m["title"].(string)
	if !ok || title == "" {
		return
	}
	w.Header().Set(pageTitleHeader, url.PathEscape(title))
}

// RenderPartial renders just a template fragment (for HTMX responses)
func RenderPartial(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := stripWebPrefix(tplPath)

		writePageTitleHeader(w, data)
		locale := ResolveLocale(w, r)
		t := template.Must(ClonedTemplate("httpx.RenderPartial:"+page, filepath.Base(page), FuncsFor(locale), page))
		if err := t.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// RenderContentFragment renders just the "content" define block of a full
// page template — base.html + page + the same shared partial set Render(...)
// uses — without base.html's surrounding chrome (no nav, no status bar). For
// a route that is ALSO a full standalone page (so a deep link still works,
// unchanged) and additionally needs to answer an htmx fragment request from
// inside ANOTHER page's own panel (ut-docs#1950: /items' five section
// destinations, embedded in its right-hand panel). Deliberately reuses the
// exact same file set/cache key as Render rather than a separate hand-
// written partial template — a forked copy of a page's markup would drift
// from the real page the moment either one changed and the other didn't;
// this can't drift because it IS the same parsed template, just entered at
// "content" instead of "base". See httpx.IsFragmentSwap for the header
// check a caller uses to decide which of Render/RenderContentFragment a
// given request gets (mirrors renderHelpPage's original, older version of
// that same check for /help/{topic}).
func RenderContentFragment(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := stripWebPrefix(tplPath)

		writePageTitleHeader(w, data)
		locale := ResolveLocale(w, r)
		files := append([]string{renderFiles[0], page}, renderFiles[1:]...)
		t := template.Must(ClonedTemplate("httpx.Render:"+page, "base.html", withHelpHref(FuncsFor(locale), r), files...))
		if err := t.ExecuteTemplate(w, "content", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// IsFragmentSwap reports whether r is an ordinary in-page htmx navigation —
// "HX-Request: true" and NOT ALSO "HX-History-Restore-Request: true" — and,
// as a side effect on every call regardless of the result, sets
// "Vary: HX-Request" on w.
//
// The Vary header matters because the two branches this decides between
// return different bodies for the SAME URL: a fragment for an htmx
// navigation, a complete standalone page otherwise. With no Vary header, a
// browser/WebView HTTP cache keys purely on the URL and can serve either
// body to the other kind of request — concretely, a rail swap that fetches
// a page as a fragment and pushes its URL into history, followed by a
// plain navigation back to that URL (e.g. the Android hardware Back
// button), can be served the cached fragment: an unstyled page with no
// <head> (ut-docs#2091). Setting it here, at the one place every dual-mode
// handler already calls to make this decision, means a future dual-mode
// handler inherits the fix automatically instead of having to remember it
// — which is also why renderHelpPage's own former inline copy of this
// exact check (ut-docs#433) was retired in favor of calling this helper
// directly as part of ut-docs#2091, rather than keeping a second place
// this rule could go stale.
//
// The HX-History-Restore-Request exclusion: htmx caps its client-side
// history cache at 10 snapshots, so restoring an older bfcache'd URL makes
// htmx re-request it itself with BOTH headers set, expecting the FULL page
// back (to replace the whole tracked history element) rather than a bare
// swappable fragment — checking HX-Request alone sent the fragment there
// too and left the restored page broken. Shared here so every handler that
// serves both a full standalone page and an htmx fragment of the same
// content at the same route applies the identical rule — /items' five
// section destinations (ut-docs#1950) need it five times over.
func IsFragmentSwap(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Vary", "HX-Request")
	if strings.EqualFold(r.Header.Get("HX-History-Restore-Request"), "true") {
		return false
	}
	return strings.EqualFold(r.Header.Get("HX-Request"), "true")
}

func JSON[In any, Out any](fn func(In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if r.Body != nil {
			defer r.Body.Close()
			_ = json.NewDecoder(r.Body).Decode(&in)
		}
		out, err := fn(in)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}
