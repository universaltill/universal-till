package httpx

import (
    "encoding/json"
    "html/template"
    "net/http"
    "path/filepath"
)

func NewMux() *http.ServeMux { return http.NewServeMux() }

func Render(tplPath string, data any) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        layout := "web/ui/layouts/base.html"
        tpl := template.Must(template.ParseFiles(layout, filepath.Join("web", tplPath), "web/ui/partials/nav.html"))
        _ = tpl.ExecuteTemplate(w, "base", data)
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
