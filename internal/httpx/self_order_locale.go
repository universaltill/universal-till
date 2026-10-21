package httpx

import (
	"net/http"
	"slices"
	"strings"
)

// SelfOrderLocaleCookie carries a self-order kiosk customer's language pick
// for the length of one order (ut-docs#4200). It is deliberately not
// ut_lang: that cookie is the till's per-browser preference and lasts a
// year, so a customer's pick written there would greet the next customer
// too. This one is a browser-session cookie that the walk-up start screen
// clears, and only the self-order routes read it (isSelfOrderPath), so the
// till's own screens and a staff session on the kiosk keep their language.
const SelfOrderLocaleCookie = "ut_self_order_lang"

// WriteSelfOrderLocale records a customer's pick. The caller checks the code
// against InstalledLocale first. Path "/" for the same reason as
// ut_self_order_session: the basket API under /api/self-order/* must
// receive it, and that path does not start with /self-order.
func WriteSelfOrderLocale(w http.ResponseWriter, locale string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SelfOrderLocaleCookie,
		Value:    locale,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSelfOrderLocale drops the pick, back to the shop's language.
func ClearSelfOrderLocale(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SelfOrderLocaleCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// InstalledLocale reports whether locale is one the till can render
// (AvailableLocales). The self-order routes are auth-exempt and the cookie
// is client-controlled, so nothing else is trusted as a locale.
func InstalledLocale(locale string) bool {
	return locale != "" && slices.Contains(AvailableLocales(), locale)
}

// isSelfOrderPath matches the routes registerSelfOrder and the self-order
// shop register: /self-order, /self-order/..., /api/self-order/...
func isSelfOrderPath(path string) bool {
	return path == "/self-order" || strings.HasPrefix(path, "/self-order/") || strings.HasPrefix(path, "/api/self-order/")
}

// SelfOrderLocale returns this request's kiosk customer pick, if it is a
// self-order request carrying a pick for an installed language. The
// checkout records it for the customer's printed receipt (ut-docs#4705).
func SelfOrderLocale(r *http.Request) (string, bool) {
	if !isSelfOrderPath(r.URL.Path) {
		return "", false
	}
	c, err := r.Cookie(SelfOrderLocaleCookie)
	if err != nil || !InstalledLocale(c.Value) {
		return "", false
	}
	return c.Value, true
}
