package httpx

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	moneypkg "github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

var baseFuncs = template.FuncMap{
	"div100":          func(cents int64) float64 { return float64(cents) / 100.0 },
	"bpPercent":       func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
	"appversion":      func() string { return buildinfo.Version },
	"updateavailable": func() bool { return updates.Current().Available },
	"latestversion":   func() string { return updates.Current().Latest },
	"canselfupdate":   func() bool { return selfupdate.Supported() },
	"enrolled":        func() bool { return enroll.CurrentStatus().Registered },
	"enrolstore":      func() string { return enroll.CurrentStatus().StoreID },
	"enroldevice":     func() string { return enroll.CurrentStatus().DeviceID },
}

// NewRenderer renders a layout + page (and optional partial) with funcs.
type Renderer struct {
	t *template.Template
}

func NewRenderer(layout string, page string, funcs template.FuncMap, partials ...string) (*Renderer, error) {
	files := []string{layout, page, filepath.Join("web", "ui", "partials", "nav.html")}
	files = append(files, partials...)
	t, err := template.New("base.html").Funcs(funcs).ParseFiles(files...)
	if err != nil {
		return nil, err
	}
	return &Renderer{t: t}, nil
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) error {
	return r.t.ExecuteTemplate(w, name, data)
}

// RenderWith builds a one-off renderer from explicit files and funcs.
func RenderWith(files []string, funcs template.FuncMap) func(name string, data any) http.HandlerFunc {
	return func(name string, data any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			t, err := template.New("base.html").Funcs(funcs).ParseFiles(files...)
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

func uiScalePx() string {
	scale := 1.0
	if v := uiScale.Load(); v != nil {
		if f, ok := v.(float64); ok {
			scale = f
		}
	}
	return strconv.FormatFloat(16*scale, 'f', -1, 64)
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

// T translates a key for a locale outside templates (handlers building toasts
// or fragments). Falls back to the key itself, mirroring the template func.
func T(locale, key string) string {
	if tAny := i18nRef.Load(); tAny != nil {
		return tAny.(*config.I18n).T(locale, key)
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
		if tAny := i18nRef.Load(); tAny != nil {
			return tAny.(*config.I18n).Available()
		}
		return []string{"en"}
	}
	funcs["T"] = func(key string) string {
		if tAny := i18nRef.Load(); tAny != nil {
			return tAny.(*config.I18n).T(locale, key)
		}
		return key
	}
	return funcs
}

func NewMux() *http.ServeMux { return http.NewServeMux() }

// Render full page with layout + page + common partials
func Render(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		layout := filepath.Join("web", "ui", "layouts", "base.html")
		page := filepath.Join("web", tplPath)

		locale := ResolveLocale(w, r)
		t := template.Must(template.New("base.html").Funcs(FuncsFor(locale)).ParseFiles(
			layout,
			page,
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			filepath.Join("web", "ui", "partials", "basket.html"),
			filepath.Join("web", "ui", "partials", "toast.html"),
			filepath.Join("web", "ui", "partials", "plugin_install_modal.html"),
			filepath.Join("web", "ui", "partials", "plugin_manual_import.html"),
		))
		if err := t.ExecuteTemplate(w, "base", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// RenderPartial renders just a template fragment (for HTMX responses)
func RenderPartial(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := filepath.Join("web", tplPath)

		locale := ResolveLocale(w, r)
		t := template.Must(template.New(filepath.Base(page)).Funcs(FuncsFor(locale)).ParseFiles(page))
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
