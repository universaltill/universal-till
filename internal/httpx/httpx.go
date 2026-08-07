package httpx

import (
	"encoding/json"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/manual"
	moneypkg "github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
	uiassets "github.com/universaltill/universal-till/web"
)

var baseFuncs = template.FuncMap{
	"div100":          func(cents int64) float64 { return float64(cents) / 100.0 },
	"bpPercent":       func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
	"appversion":      func() string { return buildinfo.Version },
	"updateavailable": func() bool { return updates.Current().Available },
	"latestversion":   func() string { return updates.Current().Latest },
	"canselfupdate":   func() bool { return selfupdate.Supported() },
	// updatedownloadlink: whether the status-bar chip's fallback (when
	// canselfupdate is false) may show an actionable website link — false on
	// a unix kiosk, where that link is a dead end (ut-docs#147/#159). Mirrors
	// internal/pages/update_api.go's updateUnavailableHTML via the shared
	// selfupdate.DownloadLinkActionable predicate.
	"updatedownloadlink": func() bool { return selfupdate.DownloadLinkActionableNow() },
	"enrolled":           func() bool { return enroll.CurrentStatus().Registered },
	"enrolstore":         func() string { return enroll.CurrentStatus().StoreID },
	"enroldevice":        func() string { return enroll.CurrentStatus().DeviceID },
	"jsonVals":           jsonVals,
	// Default target for the nav's contextual "?" — the manual's index.
	// Render() overrides this per request with the topic documenting the page
	// actually being rendered; fragment renderers that also parse nav.html
	// (internal/ui, RenderWith) keep this fallback rather than failing to
	// parse, which is why it lives in the base map at all.
	"helpHref": func() string { return "/help" },
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
	return func(name string, data any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			t, err := template.New("base.html").Funcs(withHelpHref(funcs, r)).ParseFS(uiassets.FS, stripWebPrefixes(files)...)
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
	// Uploaded assets (item/variant photos, receipt logo) live in the stable
	// per-user data dir (see internal/paths), not the cwd-relative release
	// tree — check there first so a re-uploaded file gets a fresh ?v= and
	// isn't served stale from the browser cache. Falls back to the
	// cwd-relative path for built-in assets shipped in web/.
	if info, err := os.Stat(paths.Data(rel)); err == nil {
		return strconv.FormatInt(info.ModTime().Unix(), 10)
	}
	if info, err := os.Stat(filepath.Join("web", rel)); err == nil {
		return strconv.FormatInt(info.ModTime().Unix(), 10)
	}
	return strconv.FormatInt(bootTime, 10)
}

var bootTime = time.Now().Unix()

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
	funcs["toJson"] = toJSON
	funcs["assetv"] = assetVersion
	funcs["imgv"] = imgVersion
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
	funcs["T"] = func(key string) string {
		if t := translator(); t != nil {
			return t.T(locale, key)
		}
		return key
	}
	return funcs
}

func NewMux() *http.ServeMux { return http.NewServeMux() }

// Render full page with layout + page + common partials
func Render(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		layout := "ui/layouts/base.html"
		page := stripWebPrefix(tplPath)

		locale := ResolveLocale(w, r)
		t := template.Must(template.New("base.html").Funcs(withHelpHref(FuncsFor(locale), r)).ParseFS(uiassets.FS,
			layout,
			page,
			"ui/partials/nav.html",
			"ui/partials/buttons.html",
			"ui/partials/buttons_admin.html",
			"ui/partials/basket.html",
			"ui/partials/plugin_install_modal.html",
			"ui/partials/plugin_manual_import.html",
			"ui/partials/help_topic.html",
			"ui/partials/bugreport_panel.html",
		))
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
		t := template.Must(template.New(filepath.Base(page)).Funcs(FuncsFor(locale)).ParseFS(uiassets.FS, page))
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
