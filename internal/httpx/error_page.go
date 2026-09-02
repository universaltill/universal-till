package httpx

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/logging"
)

// RenderError renders a full-page (layout + nav rail) error response, in
// place of a raw http.Error(...)/common.LocalizedError(...) call on a
// top-level page GET route. A bare http.Error body replaces the ENTIRE
// WebView document — no nav rail, no way back — which is a lock-up on the
// pinned Android kiosk (no browser Back button); see ut-docs#1455's live
// report against /tables. RenderError keeps the nav rail (and its own "?"
// contextual help link, which still resolves off r.URL.Path — the original
// failing request) so a "Back to sale" link is always reachable.
//
// msgKey is a web/locales/*.json translation key for the operator-facing
// message; err is the real underlying Go/SQL error (nil when there isn't
// one, e.g. a plain permission gate) and is logged server-side ONLY — it
// must never reach the response body, the same contract
// internal/pages/common.LogAndLocalizedError already keeps for the
// call sites this replaces. Logging can't go through
// internal/pages/common (which already imports internal/httpx — importing
// it back here would cycle), so the same 5xx-Error/4xx-Info split is
// duplicated here against internal/logging directly instead.
func RenderError(w http.ResponseWriter, r *http.Request, status int, msgKey string, err error) {
	logPageError(r, status, err)

	locale := ResolveLocale(w, r)
	message := T(locale, msgKey)

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	data := map[string]any{
		"title":   message,
		"message": message,
	}
	if rerr := renderFullPage(w, r, locale, "ui/pages/error.html", data); rerr != nil {
		// Status + Cache-Control are already written to the wire at this
		// point (WriteHeader above), so there's no clean way to fall back to
		// http.Error here without either a "superfluous WriteHeader" warning
		// or — worse — leaking a second, unrelated error into a body that's
		// supposed to carry only the translated message. Log it and stop;
		// the embedded error.html template is not expected to ever fail to
		// parse/execute in production.
		logging.L().Errorf("[page-error] %s %s: rendering error page: %v", r.Method, r.URL.Path, rerr)
	}
}

// logPageError is RenderError's server-side-only logging half: same
// 5xx-Error/4xx-Info split internal/pages/common.LogAndLocalizedError uses,
// so a genuine server problem still lands in logging.Recent() (the
// cloud-sync heartbeat's Problems ring, ADR-0018) while a routine
// operator-triggered 4xx (a permission gate, a bad request) stays out of
// it. err is optional — several RenderError call sites (e.g. a 403
// manager-gate) have no underlying Go error to log.
func logPageError(r *http.Request, status int, err error) {
	logf := logging.L().Infof
	if status >= http.StatusInternalServerError {
		logf = logging.L().Errorf
	}
	if err != nil {
		logf("[page-error] %s %s %d: %v", r.Method, r.URL.Path, status, err)
		return
	}
	logf("[page-error] %s %s %d", r.Method, r.URL.Path, status)
}
