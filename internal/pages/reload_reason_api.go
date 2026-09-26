package pages

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2788: the Android till "refreshed" (a whole-page reload) now and
// then from a background event, and nothing on the till said why. Every
// reload the page itself triggers now goes through UT.reload(reason)
// (web/ui/layouts/base.html), which stashes the reason in sessionStorage;
// the next document load posts it here, and so does a reload nobody
// announced (performance navigation type "reload" with no stash:
// "unattributed" -- a native WebView reload, pull-to-refresh, F5). One log
// line per reload is what the next on-device capture needs to name the
// cause.
//
// Auth tier: the same as POST /api/window/input-heartbeat
// (window_state_api.go) -- the plain signed-in-session tier, NOT in
// auth.exempt() and NOT PIN-gated. Every base.html page already carries
// the session cookie; a reload that landed on /login (no session) simply
// isn't reported, which is fine because the auth middleware logs its own
// HX-Redirect line for that case. Best-effort: answers 204 and never
// anything the page must act on.

const (
	reloadReasonMaxBody   = 2 << 10 // 2 KiB is ample for three short fields
	reloadReasonMaxReason = 120
	reloadReasonMaxPath   = 200
	// reloadReasonMaxPerWindow caps log lines per reloadReasonWindow so a
	// reload LOOP -- the very bug this diagnoses -- can't flood the till
	// log. Excess reports still get 204; only the line is dropped.
	reloadReasonMaxPerWindow = 30
	reloadReasonWindow       = time.Minute
)

var reloadNavTypes = map[string]bool{
	"": true, "navigate": true, "reload": true, "back_forward": true, "prerender": true,
}

type reloadReasonRequest struct {
	Reason  string `json:"reason"`
	Path    string `json:"path"`
	NavType string `json:"nav_type"`
}

func registerReloadReason(mux *http.ServeMux) {
	mux.Handle("POST /api/diag/reload-reason", newReloadReasonHandler(time.Now))
}

// newReloadReasonHandler returns the handler with its own rate-cap state;
// now is injectable for the rate-cap test.
func newReloadReasonHandler(now func() time.Time) http.HandlerFunc {
	var mu sync.Mutex
	var windowStart time.Time
	var count int

	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, reloadReasonMaxBody)
		var req reloadReasonRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reloadReasonRefuse(w, "invalid body")
			return
		}
		if !validReloadReason(req.Reason) {
			reloadReasonRefuse(w, "invalid reason")
			return
		}
		path, ok := cleanReloadPath(req.Path)
		if !ok {
			reloadReasonRefuse(w, "invalid path")
			return
		}
		if !reloadNavTypes[req.NavType] {
			reloadReasonRefuse(w, "invalid nav_type")
			return
		}

		t := now()
		mu.Lock()
		if windowStart.IsZero() || t.Sub(windowStart) >= reloadReasonWindow {
			windowStart, count = t, 0
		}
		count++
		n := count
		mu.Unlock()

		switch {
		case n <= reloadReasonMaxPerWindow:
			// %q on already-validated values: belt and braces against a
			// reason ever smuggling a line break into the log.
			logging.L().Infof("page reload: reason=%q path=%q nav=%q", req.Reason, path, req.NavType)
		case n == reloadReasonMaxPerWindow+1:
			logging.L().Infof("page reload: rate cap reached (%d per %s), dropping further reports until the window resets",
				reloadReasonMaxPerWindow, reloadReasonWindow)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func reloadReasonRefuse(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"data": nil, "error": msg})
}

// validReloadReason allows only [a-z0-9:_./ -], 1..120 chars. The client
// (UT.reload / the report in base.html) lowercases and sanitises before
// sending, so anything else is not our page talking.
func validReloadReason(s string) bool {
	if s == "" || len(s) > reloadReasonMaxReason {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case strings.ContainsRune(":_./ -", c):
		default:
			return false
		}
	}
	return true
}

// cleanReloadPath requires a same-origin absolute path ("/…", not "//…"),
// strips any query/fragment, caps the length and refuses control or
// non-ASCII bytes.
func cleanReloadPath(p string) (string, bool) {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || len(p) > reloadReasonMaxPath {
		return "", false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] >= 0x7f {
			return "", false
		}
	}
	return p, true
}
