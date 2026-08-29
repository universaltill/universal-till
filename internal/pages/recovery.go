package pages

import (
	"encoding/json"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
)

// recoverMiddleware wraps the whole request-handling chain (ut-docs#1271):
// internal/pages had no general HTTP panic-recovery — only
// sync_admin.go's syncPullPlugins recovers, and that's scoped to one
// background loop, unrelated to request handling. Without this, an
// unexpected handler panic drops the connection with no response at all;
// on the Android till that's indistinguishable from a plain network
// failure to the operator.
//
// Deliberately the OUTERMOST wrap in Init() (wraps auth.Middleware itself,
// not just mux) so a panic anywhere in the chain — including inside auth
// resolution — still gets a clean response instead of a dropped connection.
//
// Response shape mirrors auth.Middleware's own api-vs-page split: `/api/`
// paths get the same `{"data":null,"error":{code,message}}` JSON envelope
// every API handler uses; everything else (full-page loads and HTMX
// fragment loads alike) gets a localized plain-text 500 via http.Error —
// simpler than auth.Middleware's redirect-vs-fragment branch, since a
// recovery response never needs to navigate anywhere, just show a message
// wherever htmx or the browser renders it.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			logging.L().Errorf("panic recovered: %v\n%s", rec, debug.Stack())

			locale := httpx.ResolveLocale(w, r)
			msg := httpx.T(locale, "common.error.server")

			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": nil, "error": map[string]string{"code": "internal_error", "message": msg},
				})
				return
			}
			http.Error(w, msg, http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
