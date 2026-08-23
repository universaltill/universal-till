package pages

import (
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Customer order tracking via QR (ut-docs#527), on #526's order-status
// backbone. /o/{token} is the page a customer lands on after scanning the QR
// on the self-order confirmation screen; /o/{token}/status is its 15s HTMX
// poll fragment. Both are auth-exempt (internal/auth/middleware.go — an
// anonymous customer on a personal phone has no session and never will) and
// gated only by the unguessable tracking token, so they expose the order's
// STATUS and receipt number and nothing else: no basket lines, no totals, no
// payment data, and — unlike the operator-facing writeOrderStatusFragment —
// no who-changed-it attribution.
//
// Same anonymous-surface invariant as the /self-order handlers (ADR-0020,
// guard-kiosk-engine.sh): this file must never touch common.Deps.Engine or
// KioskEngine — all reads go through data.NewPOSRepo(d.Db). The guard script
// only pattern-matches /self-order routes, so it won't flag this file;
// hold the line anyway, it is the same class of risk.

// orderTrackingExpiry is how long a tracking link keeps answering after the
// order reaches a terminal status. Computed in Go from the timestamp the
// lookup already returns — no cron, no extra writes, nothing stored.
const orderTrackingExpiry = 2 * time.Hour

// orderTrackingVisible reports whether a token should still resolve. A live
// order (anything not collected/cancelled, including the untracked "") is
// always visible; a terminal one expires orderTrackingExpiry after its last
// status write. An unparseable terminal timestamp fails closed — on an
// anonymous surface a broken row must read as "gone", not "visible forever".
func orderTrackingVisible(o data.TrackedOrder, now time.Time) bool {
	if o.Status != pos.OrderStatusCollected && o.Status != pos.OrderStatusCancelled {
		return true
	}
	at, err := time.Parse(time.RFC3339, o.StatusUpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(at) <= orderTrackingExpiry
}

// orderTrackingStatusData is the template data both the full page and the
// poll fragment render the status block from (the shared
// order_tracking_status define).
func orderTrackingStatusData(o data.TrackedOrder, token string) map[string]any {
	terminal := o.Status == pos.OrderStatusCollected || o.Status == pos.OrderStatusCancelled
	return map[string]any{
		"Token":     token,
		"ReceiptNo": o.ReceiptNo,
		"Status":    o.Status,
		"StatusKey": orderStatusLabelKey(o.Status),
		"UpdatedAt": o.StatusUpdatedAt,
		// Poll re-arms the fragment's own 15s trigger; a terminal status
		// renders without it so polling stops (pairing_wait.html's
		// conditional arming).
		"Poll":    !terminal,
		"Expired": false,
	}
}

// orderTrackingQRView mints the sale's tracking token and builds the QR the
// self-order confirmation screen shows (called from the kiosk checkout
// handler in self_order_shop.go, right after the sale commits). Best-effort
// by contract: the sale is ALREADY COMMITTED when this runs, so any failure
// here — no advertisable host (kiosk browser dialling 127.0.0.1 on a box
// with no LAN address, see advertisableHost's ut-docs#362 story), a token or
// QR-encode error — returns empty views and the confirmation simply renders
// without a QR. It must never fail or delay checkout (offline-first: a QR a
// phone couldn't reach anyway is not worth an error screen). The token is
// still minted before the host check, so the sale keeps one stable token for
// any later surface. qrDataURI is a data:image/png URI, typed template.URL
// because html/template's URL filter rejects data: from a plain string.
func orderTrackingQRView(r *http.Request, repo *data.POSRepo, receiptNo, locale string) (qrDataURI template.URL, trackingURL string) {
	token, err := repo.EnsureOrderTrackingToken(r.Context(), receiptNo)
	if err != nil || token == "" {
		return "", ""
	}
	host, err := advertisableHost(r.Host)
	if err != nil {
		return "", ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// ?lang= bakes the kiosk's locale into the link, so the phone opens the
	// page in the language the customer just ordered in (ResolveLocale's
	// query-param branch — zero extra code on the page side).
	u := scheme + "://" + host + "/o/" + token + "?lang=" + url.QueryEscape(locale)
	png, err := qrcode.Encode(u, qrcode.Medium, 200)
	if err != nil {
		return "", ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)), u
}

func registerOrderTracking(mux *http.ServeMux, d *common.Deps) {
	pageFiles := []string{
		filepath.Join("web", "ui", "pages", "order_tracking.html"),
		filepath.Join("web", "ui", "partials", "order_tracking_status.html"),
	}
	fragmentFiles := []string{
		filepath.Join("web", "ui", "partials", "order_tracking_status.html"),
	}

	// lookup resolves the path token to a still-visible order. Not-found and
	// expired are deliberately the same answer — a guessed token must be
	// indistinguishable from an expired one.
	lookup := func(r *http.Request) (data.TrackedOrder, string, bool, error) {
		token := strings.TrimSpace(r.PathValue("token"))
		o, found, err := data.NewPOSRepo(d.Db).LookupOrderByTrackingToken(r.Context(), token)
		if err != nil {
			return data.TrackedOrder{}, token, false, err
		}
		if !found || !orderTrackingVisible(o, time.Now().UTC()) {
			return data.TrackedOrder{}, token, false, nil
		}
		return o, token, true, nil
	}

	// Full page. ResolveLocale makes the ?lang= the QR bakes in stick via
	// the usual cookie, so the phone follows the kiosk's language.
	mux.HandleFunc("GET /o/{token}", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		o, token, ok, err := lookup(r)
		if err != nil {
			// No error detail on an anonymous surface.
			http.Error(w, "lookup failed", http.StatusInternalServerError)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			httpx.RenderWith(pageFiles, funcs)("order_tracking.html", map[string]any{
				"NotFound": true,
			})(w, r)
			return
		}
		httpx.RenderWith(pageFiles, funcs)("order_tracking.html", orderTrackingStatusData(o, token))(w, r)
	})

	// Poll fragment: just the status block. The expired/unknown answer is a
	// 200 (htmx only swaps 2xx) rendered WITHOUT the trigger, so an open
	// page swaps to "link expired" and stops polling instead of erroring
	// forever. Deliberately NOT writeOrderStatusFragment: that one shows
	// who changed the status, which is operator-facing information this
	// anonymous page must not expose.
	mux.HandleFunc("GET /o/{token}/status", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		o, token, ok, err := lookup(r)
		if err != nil {
			http.Error(w, "lookup failed", http.StatusInternalServerError)
			return
		}
		var viewData map[string]any
		if !ok {
			viewData = map[string]any{"Expired": true, "Poll": false}
		} else {
			viewData = orderTrackingStatusData(o, token)
		}
		httpx.RenderWith(fragmentFiles, funcs)("order_tracking_status", viewData)(w, r)
	})
}
