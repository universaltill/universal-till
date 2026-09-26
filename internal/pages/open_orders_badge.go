package pages

import (
	"fmt"
	"html"
	"net/http"
	"net/url"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerOpenOrdersBadge serves the count badge on the sale screen's Open
// orders icon button (ut-docs#2702). The compact tender panel dropped the
// held-sales strip, so the badge is what tells the cashier at a glance that
// orders are parked; the popup behind the button lists them.
//
// Counted from the same source the popup lists (heldSalesForDisplay: this
// till's held sales, merged with the primary's when one is reachable), so
// the badge and the popup never disagree. Fetched on load and re-fetched
// on hold_api.go's `held-changed` event -- never during the sale screen's
// own render, which must not wait on a cross-till hop.
//
// Cost and freshness (ut-docs#2702 review): every sale-screen load now
// fires this fetch, and heldSalesForDisplay on a replica means a GET to the
// primary (800ms budget) plus ReconcileWithPrimary over the local rows --
// work the sale screen never did before the strip went. Do NOT add an
// `every Ns` poll to the badge's hx-trigger without weighing that cost per
// till per interval against the primary. The flip side, accepted for now:
// A change made on ANOTHER till reaches an open sale screen through the
// held generation instead (ut-docs#2858): every held_sales link nudge,
// sent or received, moves d.HeldToken; each badge render also re-seeds the
// hidden #open-orders-watch span (out of band) with the token read BEFORE
// the list, and that span polls GET /ui/open-orders-badge/watch -- an
// in-memory compare, no DB and no cross-till hop -- every
// openOrdersWatchEvery while the page is visible. A mismatch answers
// HX-Trigger: held-changed, and the badge re-fetches itself the normal way.
// So the badge's cost per interval stays one loopback request, and the
// badge itself is only re-fetched when something actually changed. The
// till that made a change also gets the main till's nudge about it back,
// which costs it one redundant badge re-fetch -- accepted over teaching the
// hub who caused each nudge.
//
// Held sales only for now; ut-docs#2703 extends "open orders" to include
// pay-at-counter kiosk orders and will extend this count with them.
func registerOpenOrdersBadge(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewHeldSalesRepo(d.Db)
	mux.HandleFunc("GET /ui/open-orders-badge", func(w http.ResponseWriter, r *http.Request) {
		// Read before the list: a change landing while the list is fetched
		// then leaves the watcher one token behind, so it re-fetches once
		// more rather than missing that change.
		token := d.HeldToken()
		n := 0
		if items, err := heldSalesForDisplay(r.Context(), d, repo); err != nil {
			// A badge is a hint, not a record: on a read failure show none
			// rather than erroring the fragment (the popup itself reports
			// its own failure loudly, see GET /ui/parked-orders).
			logging.L().Warnf("open-orders badge: list held sales: %v", err)
		} else {
			n = len(items)
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, openOrdersBadgeHTML(n, locale))
		_, _ = fmt.Fprint(w, openOrdersWatchHTML(token))
	})

	// The watcher's poll (ut-docs#2858). Always 204, so htmx never swaps;
	// htmx still fires an HX-Trigger header on a 204 (1.9's
	// handleAjaxResponse reads it before the swap decision). A missing v is
	// not a mismatch: firing on it could loop.
	mux.HandleFunc("GET /ui/open-orders-badge/watch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if v := r.URL.Query().Get("v"); v != "" && v != d.HeldToken() {
			w.Header().Set("HX-Trigger", "held-changed")
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// openOrdersWatchEvery is how often an open, visible sale screen asks
// whether the shop's held sales changed. Each ask is one in-memory compare
// on this till's own server.
const openOrdersWatchEvery = "3s"

// openOrdersWatchHTML is the hidden watcher, swapped out of band over
// index.html's #open-orders-watch placeholder by every badge render, so it
// always carries the token the badge on screen was counted at.
func openOrdersWatchHTML(token string) string {
	return fmt.Sprintf(`<span id="open-orders-watch" data-testid="open-orders-watch" hidden aria-hidden="true" hx-swap-oob="true" hx-get="/ui/open-orders-badge/watch?v=%s" hx-trigger="every %s [document.visibilityState==='visible']" hx-swap="none"></span>`,
		html.EscapeString(url.QueryEscape(token)), openOrdersWatchEvery)
}

// openOrdersBadgeHTML is the badge element every fetch swaps in (index.html
// renders an empty placeholder with the same hx-get, triggered on load).
// Zero renders hidden, not absent, so it stays the swap target. hx-target=
// "this" because the badge sits inside the Open orders button, whose own
// hx-target (#parked-orders-body) htmx would otherwise inherit.
func openOrdersBadgeHTML(n int, locale string) string {
	hidden := ""
	label := fmt.Sprintf("%d", n)
	if n == 0 {
		hidden = " hidden"
	} else if n > 99 {
		label = "99+"
	}
	// Locale digits (ut-docs#2649): a fa/ar till shows ۲, not 2, next to
	// a basket that already renders Persian/Arabic-Indic digits.
	label = httpx.LocalizeDigits(label, locale)
	return fmt.Sprintf(`<span class="count-badge" data-testid="open-orders-badge" data-count="%d" hx-get="/ui/open-orders-badge" hx-trigger="held-changed from:body" hx-target="this" hx-swap="outerHTML"%s>%s</span>`, n, hidden, label)
}
