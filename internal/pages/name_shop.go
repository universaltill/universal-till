package pages

// "Name your shop" gate (ut-docs#4391): a manager on an unnamed MAIN till who
// opens an admin page is first asked for whichever of the shop name / this
// till's name is missing, then taken on to the page they opened.
//
// The gate is a nudge, never a lock-out and never part of the sale flow:
//   - only a session holding the settings grant is redirected (a cashier
//     never is); with UT_AUTH=off there is no session and no gate;
//   - the sale screen and every selling surface are exempt (R2), as are
//     assets, APIs, fragments and polls: only a real page navigation (or a
//     boosted shell navigation, R1 / ADR-0098) is redirected;
//   - a till that follows a main till never asks (D3);
//   - a settings read error passes the request through (fail open).

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const nameShopPath = "/name-shop"

// nameShopGate redirects a manager on an unnamed main till to /name-shop.
func nameShopGate(next http.Handler, d *common.Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !nameShopGateCandidate(r) {
			next.ServeHTTP(w, r)
			return
		}
		// No session (anonymous, or auth off): the auth middleware already
		// decided; the gate never exists without a user.
		if _, ok := auth.FromContext(r.Context()); !ok || !canPerform(d, r, "settings") {
			next.ServeHTTP(w, r)
			return
		}
		shop, till, err := missingShopNames(r.Context(), d)
		if err != nil {
			logging.L().Errorf("name-shop gate: %v", err)
			next.ServeHTTP(w, r)
			return
		}
		if !shop && !till {
			next.ServeHTTP(w, r)
			return
		}
		nameShopRedirect(w, r, nameShopPath+"?next="+url.QueryEscape(r.URL.RequestURI()))
	})
}

// nameShopRedirect: a boosted shell navigation gets HX-Redirect (htmx then
// does a full browser navigation); anything else a 303.
func nameShopRedirect(w http.ResponseWriter, r *http.Request, dest string) {
	if httpx.IsBoosted(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// nameShopGateCandidate is the pure request-shape filter (no DB).
func nameShopGateCandidate(r *http.Request) bool {
	if r.Method != http.MethodGet || nameShopGateExempt(r.URL.Path) {
		return false
	}
	boosted := httpx.IsBoosted(r)
	if r.Header.Get("HX-Request") == "true" && !boosted {
		return false // fragments, polls, history restore
	}
	if !boosted {
		if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "document" {
			return false // img/script/iframe/fetch
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			return false // assets / JSON / fetch() callers
		}
	}
	return true
}

var nameShopExactPaths = map[string]bool{
	"/": true, // the sale screen, whatever the device profile (/?stay=1 too)
	// Sign-in, lock, setup and health.
	"/login": true, "/pin": true, "/lock": true, "/logout": true, "/setup": true,
	"/healthz": true, "/version": true, "/csp-report": true, "/favicon.ico": true,
	// Selling surfaces besides "/" (R2).
	"/tables": true, "/open-orders": true, "/orders": true, "/self-order": true,
	// The gate's own page and the manual.
	nameShopPath: true, "/help": true,
}

var nameShopExemptPrefixes = []string{
	"/api/", "/ui/", "/v1/", "/notices/", // APIs, fragments, polls
	"/setup/", "/help/",
	"/self-order/", "/kitchen-display/", "/o/", "/refund/", "/plugin/", // selling / customer-facing / plugin-owned (R2)
	"/public/", "/themes/", "/plugin-icons/", "/plugin-fonts/", "/ext/", // assets
	"/settings/cloud/move/", // move approval must never wait on a name prompt
}

func nameShopGateExempt(path string) bool {
	if nameShopExactPaths[path] {
		return true
	}
	for _, p := range nameShopExemptPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// missingShopNames reports which names a MAIN till still needs. A till that
// follows a main till never asks (D3).
func missingShopNames(ctx context.Context, d *common.Deps) (shop, till bool, err error) {
	if tillFollowsMain(ctx, d) {
		return false, false, nil
	}
	v, ok, err := d.Settings.Get(ctx, common.KeyStoreName)
	if err != nil {
		return false, false, err
	}
	// Same "unset is not shown" rule as shop_name_notice.go.
	shop = ok && config.IsPlaceholderStoreName(v)
	till = enroll.DeviceName(ctx, d.Settings) == ""
	return shop, till, nil
}

// safeNameShopNext keeps only a same-origin relative path (D5).
func safeNameShopNext(raw string) string {
	const fallback = "/settings"
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.Contains(raw, `\`) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fallback
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Path == nameShopPath {
		return fallback
	}
	return raw
}

type nameShopView struct {
	AskShop, AskTill bool
	Next             string
	ShopValue        string
	TillValue        string
	ShopErr, TillErr string
}

func renderNameShop(w http.ResponseWriter, r *http.Request, d *common.Deps, view nameShopView, status int) {
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
	}
	httpx.Render("ui/pages/name_shop.html", map[string]any{
		"title":     httpx.T(httpx.RequestLocale(r), "page.title.name_shop"),
		"theme":     d.CurrentState().Theme,
		"menuItems": d.MenuSnapshot(),
		"view":      view,
	})(w, r)
}

func registerNameShop(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /name-shop", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		next := safeNameShopNext(r.URL.Query().Get("next"))
		shop, till, err := missingShopNames(r.Context(), d)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		if !shop && !till {
			nameShopRedirect(w, r, next)
			return
		}
		renderNameShop(w, r, d, nameShopView{AskShop: shop, AskTill: till, Next: next}, http.StatusOK)
	})

	mux.HandleFunc("POST /name-shop", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !requirePage(d, w, r, "settings") {
			return
		}
		next := safeNameShopNext(r.PostForm.Get("next"))
		shop, till, err := missingShopNames(r.Context(), d)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		if !shop && !till {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		view := nameShopView{AskShop: shop, AskTill: till, Next: next}
		var name, tn string
		bad := false
		if shop {
			view.ShopValue = r.PostForm.Get("store_name")
			var nerr error
			if name, nerr = config.NormalizeStoreName(view.ShopValue); nerr != nil {
				view.ShopErr = storeNameErrorKey(nerr)
				bad = true
			}
		}
		if till {
			view.TillValue = r.PostForm.Get("till_name")
			var terr error
			if tn, terr = validateTillName(view.TillValue); terr != nil {
				view.TillErr = "name_shop.error.till_name_invalid"
				bad = true
			} else {
				taken, err := tillNameTaken(r.Context(), d, tn)
				if err != nil {
					httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
					return
				}
				if taken {
					view.TillErr = "sync.error.name_taken"
					bad = true
				}
			}
		}
		if bad {
			renderNameShop(w, r, d, view, http.StatusUnprocessableEntity)
			return
		}
		// requirePage passed, so this is always `allowed`; it supplies ActorID.
		elev := checkOrElevate(d, r, "settings", "")
		var serr error
		if shop {
			serr = applyStoreName(r, d, posRepo, elev, name)
		}
		if serr == nil && till {
			serr = applyOwnTillName(r, d, posRepo, elev, tn)
		}
		if serr != nil {
			if !respondSettingsSyncPage(w, r, serr) {
				httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", serr)
			}
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	})
}
