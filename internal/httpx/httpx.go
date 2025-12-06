package httpx

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/config"
)

var baseFuncs = template.FuncMap{
	"div100":    func(cents int64) float64 { return float64(cents) / 100.0 },
	"bpPercent": func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
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

func money(amountCents int64) string {
	code := "GBP"
	if v := currencyCode.Load(); v != nil {
		if s, ok := v.(string); ok && s != "" {
			code = s
		}
	}
	symbol := map[string]string{"GBP": "£", "USD": "$", "EUR": "€"}[code]
	if symbol == "" {
		symbol = code + " "
	}
	return fmt.Sprintf("%s%.2f", symbol, float64(amountCents)/100.0)
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

// FuncsFor builds template funcs for a specific request/locale.
func FuncsFor(locale string) template.FuncMap {
	funcs := template.FuncMap{}
	for k, v := range baseFuncs {
		funcs[k] = v
	}
	funcs["money"] = money
	funcs["toJson"] = toJSON
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
