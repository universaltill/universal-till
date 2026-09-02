package httpx

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/logging"
)

// RenderError renders a translated error page through the normal layout
// (rail + a "Back to sale" link) in place of a raw http.Error(...) call, for
// use on handlers registered on a PAGE route (ut-docs#1455). A bare
// http.Error response replaces the WebView's entire document with plain
// text — no rail, no way back — which on a pinned Android kiosk is a
// permanent lock-up: there is no browser chrome to press Back with, and the
// kiosk hides Android's own navigation bar. RenderError renders the same
// base.html layout every other page uses, so the operator always has a way
// back even when the page itself failed to load.
//
// API/htmx-fragment routes should keep using LocalizedError/
// LogAndLocalizedError (internal/pages/common) instead — they need a short
// body that swaps cleanly into an existing page, not a full HTML document.
// A standalone page that intentionally skips the base layout (setup's
// first-boot wizard, the anonymous customer order-tracking page) isn't a
// fit either — RenderError assumes the operator-facing rail/currency/
// enrolled state the layout renders are meaningful, which isn't true
// before enrollment or on a customer's own phone.
//
// The real error is logged server-side, tagged by route/method/status so
// it's grepable — this is the "not logged anywhere" gap #1455 found
// alongside the bare-text bug — and never reaches the response body. Status
// >= 500 logs at Error (feeds logging.Recent(), same as
// LogAndLocalizedError); anything else logs at Info, mirroring that
// function's status-based level split.
//
// Cache-Control: no-store, so a transient failure (e.g. one bad request
// mid-outage) is never replayed from a cache on retry.
func RenderError(w http.ResponseWriter, r *http.Request, status int, msgKey string, err error) {
	if err != nil {
		if status >= http.StatusInternalServerError {
			logging.L().Errorf("[page-error] %s %s %d: %v", r.Method, r.URL.Path, status, err)
		} else {
			logging.L().Infof("[page-error] %s %s %d: %v", r.Method, r.URL.Path, status, err)
		}
	} else if status >= http.StatusInternalServerError {
		logging.L().Errorf("[page-error] %s %s %d", r.Method, r.URL.Path, status)
	} else {
		logging.L().Infof("[page-error] %s %s %d", r.Method, r.URL.Path, status)
	}

	// Resolve locale up front — T() below needs it either way, and
	// ResolveLocale may set the ut_lang cookie, which has to happen before
	// WriteHeader (headers can't change after that).
	locale := ResolveLocale(w, r)

	// Build the template BEFORE writing the status/headers: this is the
	// last-resort error renderer, so a parse/clone failure here must never
	// leave the response half-written (a WriteHeader already sent, then a
	// panic — worse than the bare http.Error this replaces). Fall back to
	// a plain http.Error in that case instead of template.Must's panic.
	page := stripWebPrefix("ui/pages/error_page.html")
	files := append([]string{renderFiles[0], page}, renderFiles[1:]...)
	t, terr := ClonedTemplate("httpx.RenderError:"+page, "base.html", withHelpHref(FuncsFor(locale), r), files...)
	if terr != nil {
		logging.L().Errorf("[page-error] building error page template: %v", terr)
		http.Error(w, T(locale, msgKey), status)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	data := map[string]any{
		"title":   "Error",
		"Message": T(locale, msgKey),
	}
	if rerr := t.ExecuteTemplate(w, "base", data); rerr != nil {
		// Status/headers are already written — nothing more to send the
		// client at this point, just make the render failure itself visible.
		logging.L().Errorf("[page-error] rendering error page: %v", rerr)
	}
}
