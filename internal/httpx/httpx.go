package httpx

import (
	"encoding/json"
	"html/template"
	"net/http"
	"path/filepath"
)

// NewMux returns a plain ServeMux.
func NewMux() *http.ServeMux { return http.NewServeMux() }

// Render loads the base layout + the requested page + common partials,
// then executes the "base" template.
func Render(tplPath string, data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		layout := filepath.Join("web", "ui", "layouts", "base.html")
		page := filepath.Join("web", tplPath)

		// Include common partials; buttons partial is rendered via its own handler.
		tpl := template.Must(template.ParseFiles(
			layout,
			page,
			filepath.Join("web", "ui", "partials", "nav.html"),
		))

		if err := tpl.ExecuteTemplate(w, "base", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// JSON wraps a handler that takes a JSON request body and returns JSON.
// Usage:
//
//	mux.HandleFunc("/api/x", httpx.JSON(func(in Req) (Resp, error) { ... }))
func JSON[In any, Out any](fn func(In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if r.Body != nil {
			defer r.Body.Close()
			_ = json.NewDecoder(r.Body).Decode(&in) // best-effort; empty body -> zero value
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
