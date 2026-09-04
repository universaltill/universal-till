package httpx

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
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
// (ut-docs#1541). Same reason as UpdateInstallBridge above: the update UI is
// now gated on BOTH platform and freshness, and without a seam a test cannot
// render the "an update exists" branch at all — it would depend on whatever
// the real GitHub API happened to say when the suite ran. Restore the original
// value (e.g. via t.Cleanup) after overriding it.
var UpdateAvailable = func() bool { return updates.Current().Available }

var baseFuncs = template.FuncMap{
	"div100":    func(cents int64) float64 { return float64(cents) / 100.0 },
	"bpPercent": func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
	// taxCodeName / categoryName / brandName: safe defaults so any renderer
	// that pulls in catalog_table.html/catalog_row.html without caring
	// about tax codes or lookups (e.g. a test exercising an unrelated part
	// of the page) still parses — Go's html/template requires every
	// function a template text references to be registered at Parse time,
	// whether or not that branch executes. The catalog package's own
	// render call sites (ut-docs#1178, ut-docs#1430) override these with
	// the real ID→name lookups via FuncsFor(locale)'s returned map before
	// rendering.
	"taxCodeName":     func(*string) string { return "" },
	"categoryName":    func(*string) string { return "" },
	"brandName":       func(*string) string { return "" },
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
	"jsonVals":        jsonVals,
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
	// {{ icon "lock" }} — inline SVG rail icons (icons.go, ut-docs#1423).
	"icon": iconHTML,
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
// TCount renders a count with its noun in the right grammatical number:
// keyBase+"_one" for exactly 1, keyBase+"_other" otherwise, with n substituted
// for the key's %d.
//
// This is a TWO-FORM selector, correct for en/de/tr/fa. Arabic has six CLDR
// categories and gets only the one/other split — still a real improvement on
// what it replaces, which was a bare plural noun concatenated after any number
// and therefore said "1 Kassen" and "1 tills" (ut-docs#1539). A full CLDR
// implementation is separate, larger work; do not mistake this for one.
func TCount(locale, keyBase string, n int) string {
	suffix := "_other"
	if n == 1 {
		suffix = "_one"
	}
	form := T(locale, keyBase+suffix)
	// Only substitute when there is something to substitute into. T falls back
	// to the key itself when a key is missing, and Sprintf on a string with no
	// verb appends "%!(EXTRA int=1)" — a Go internal rendered onto a shop's
	// till. Some languages also legitimately omit the numeral from the
	// singular. Neither case should reach the screen.
	if !strings.Contains(form, "%") {
		return form
	}
	return fmt.Sprintf(form, n)
}

func T(locale, key string) string {
	if t := translator(); t != nil {
		return t.T(locale, key)
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

// ResolveLocale determines the locale from query, cookie, then default.
func ResolveLocale(w http.ResponseWriter, r *http.Request) string {
	// query param takes precedence and sets cookie
	if lang := r.URL.Query().Get("lang"); lang != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "ut_lang",
			Value:    lang,
			Path:     "/",
			MaxAge:   31536000, // 1 year
			HttpOnly: false,
		})
		return lang
	}
	// cookie
	if c, err := r.Cookie("ut_lang"); err == nil && c.Value != "" {
		return c.Value
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
}

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

// RenderPartial renders just a template fragment (for HTMX responses)
func RenderPartial(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := stripWebPrefix(tplPath)

		locale := ResolveLocale(w, r)
		t := template.Must(ClonedTemplate("httpx.RenderPartial:"+page, filepath.Base(page), FuncsFor(locale), page))
		if err := t.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
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
