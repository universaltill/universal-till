package pages

import (
	"context"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// Customer order tracking via QR (ut-docs#527), on #526's order-status
// backbone. /o/{token} is the page a customer lands on after scanning the QR
// on the self-order confirmation screen or, since ut-docs#908, on a counter
// sale's receipt (printed or on screen); /o/{token}/status is its 15s HTMX
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

// orderTrackingVisible reports whether a token should still resolve. The
// rule itself (terminal statuses expire pos.OrderTrackingExpiry after their
// last status write; an untracked "" sale expires
// pos.OrderTrackingUntrackedExpiry after it was created, ut-docs#908;
// unparseable timestamps fail closed) moved to
// internal/pos with the cloud relay (ADR-0070, ut-docs#907) so the cloudsync
// push applies the identical liveness the LAN page does — this wrapper just
// adapts data.TrackedOrder to it.
func orderTrackingVisible(o data.TrackedOrder, now time.Time) bool {
	return pos.OrderTrackingVisible(o.Status, o.StatusUpdatedAt, o.CreatedAt, now)
}

// orderTrackingAwaitingPayment (ut-docs#4206) is the status /o/{token}
// shows for a kiosk pay-at-counter order that is still parked as a held
// sale: the customer has the QR, the cashier has not taken payment yet.
// It exists ONLY on this anonymous page — it is not part of the
// order-status lifecycle (pos.OrderStatus*), is never written to
// sales.order_status, and so never reaches the orders board or the cloud
// relay push (ListLiveTrackedOrders reads sales only). Not terminal, so
// the page keeps polling and picks up the sale's own status once paid.
const orderTrackingAwaitingPayment = "awaiting_payment"

// orderTrackingStatusData is the template data both the full page and the
// poll fragment render the status block from (the shared
// order_tracking_status define).
func orderTrackingStatusData(o data.TrackedOrder, token string) map[string]any {
	terminal := pos.OrderTrackingTerminal(o.Status)
	statusKey := orderStatusLabelKey(o.Status)
	if o.Status == orderTrackingAwaitingPayment {
		// Page-only state, not an order-board status: its label lives
		// under tracking.*, never orders.status.*.
		statusKey = "tracking.awaiting_payment"
	}
	// Wait starts the resume-grace counter on the poll URL while the order
	// waits for payment (see orderTrackingResumeGrace); 0 = no counter.
	wait := 0
	if o.Status == orderTrackingAwaitingPayment {
		wait = 1
	}
	return map[string]any{
		"Wait":      wait,
		"Token":     token,
		"ReceiptNo": o.ReceiptNo,
		"DisplayNo": o.DisplayNo,
		"Status":    o.Status,
		"StatusKey": statusKey,
		"UpdatedAt": o.StatusUpdatedAt,
		// Poll re-arms the fragment's own 15s trigger; a terminal status
		// renders without it so polling stops (pairing_wait.html's
		// conditional arming).
		"Poll":    !terminal,
		"Expired": false,
	}
}

// orderTrackingResumeMaxPolls bounds how many not-found polls a page that
// was showing "waiting for payment" keeps that state for (40 × 15s = 10
// minutes) while the cashier has the order resumed but not yet paid
// (ut-docs#4206).
const orderTrackingResumeMaxPolls = 40

// orderTrackingResumeGrace reads the poll's ?w= counter: 0 when absent,
// malformed or past orderTrackingResumeMaxPolls (then the page expires as
// before).
func orderTrackingResumeGrace(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("w"))
	if err != nil || n < 1 || n >= orderTrackingResumeMaxPolls {
		return 0
	}
	return n
}

// orderTrackingOffered (ut-docs#4206) is the plan gate on customer order
// tracking: a Shop-and-up feature (it is served through our cloud), so the
// kiosk confirmation shows a tracking QR, and a token is minted at all,
// only while this till's cached entitlement allows cloud_sync — the same
// fail-closed EffectivePlan read every paid capability uses (no cache,
// stale past grace, lapsed → Local → no QR). Used by BOTH kiosk checkout
// paths (pay at kiosk, pay at counter). Display-only, never a sale gate
// (ADR-0060 §5): it runs after the sale committed or the order was
// already accepted, and only decides whether a QR is shown — which is why
// it lives here and not in a sale-path file
// (entitlement/salepath_imports_test.go).
func orderTrackingOffered(ctx context.Context, d *common.Deps) bool {
	if d == nil || d.Settings == nil {
		return false
	}
	return entitlement.Allows(entitlement.EffectivePlan(ctx, d.Settings, time.Now()), entitlement.CapCloudSync)
}

// orderTrackingQRView mints the sale's tracking token and builds the QR the
// self-order confirmation screen shows (called from the kiosk checkout
// handler in self_order_shop.go, right after the sale commits, and only when
// orderTrackingOffered). Best-effort by contract: the sale is ALREADY
// COMMITTED when this runs, so any failure here returns empty views and the
// confirmation simply renders without a QR (see orderTrackingQRForToken).
// The token is still minted before the link check, so the sale keeps one
// stable token for any later surface.
func orderTrackingQRView(r *http.Request, repo *data.POSRepo, receiptNo, locale string) (qrDataURI template.URL, trackingURL string) {
	token, err := repo.EnsureOrderTrackingToken(r.Context(), receiptNo)
	if err != nil || token == "" {
		// No token in the message (it is the customer's capability); the
		// receipt number is what an operator looks the sale up by.
		logging.L().Warnf("order tracking: no tracking QR for sale %s: token: %v", receiptNo, err)
		return "", ""
	}
	return orderTrackingQRForToken(token, locale)
}

// orderTrackingCloudBase is the base URL of the customer tracking page a
// tracking link opens. Order tracking is cloud-only (Shop and up): a link
// must never point at the till itself — a customer's phone is not on the
// shop network, and the link would hand every customer the till's own
// address (ut-docs#4260). The cloud page does not exist yet (#4238), so
// there is no base and no surface offers a tracking QR; #4238 returns the
// cloud page's URL here.
func orderTrackingCloudBase() string { return "" }

// orderTrackingLinksAvailable reports whether a tracking link can be built
// at all — the receipt designer greys its tracking box out while it can't.
func orderTrackingLinksAvailable() bool { return orderTrackingCloudBase() != "" }

// orderTrackingLink is the one place a customer tracking link is built:
// the cloud page for token, with locale baked in (?lang=) so the phone
// opens it in the language the order was placed in. ok is false while
// there is no cloud page (orderTrackingCloudBase).
func orderTrackingLink(token, locale string) (link string, ok bool) {
	base := orderTrackingCloudBase()
	if base == "" || token == "" {
		return "", false
	}
	return strings.TrimSuffix(base, "/") + "/o/" + token + "?lang=" + url.QueryEscape(locale), true
}

// orderTrackingQRForToken builds the tracking link and its QR for an
// already-minted token — shared by the pay-at-kiosk path (a sale's token)
// and the pay-at-counter path (a held order's token, ut-docs#4206).
// Best-effort: it must never fail or delay checkout (offline-first). No
// link (no cloud page yet, ut-docs#4260) or a QR-encode error returns empty
// views and the screen renders without a QR. qrDataURI is a
// data:image/png URI, typed template.URL because html/template's URL filter
// rejects data: from a plain string.
func orderTrackingQRForToken(token, locale string) (qrDataURI template.URL, trackingURL string) {
	u, ok := orderTrackingLink(token, locale)
	if !ok {
		return "", ""
	}
	png, err := qrcode.Encode(u, qrcode.Medium, 200)
	if err != nil {
		logging.L().Warnf("order tracking: no tracking QR: encode: %v", err)
		return "", ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)), u
}

// receiptTrackingQROffered (ut-docs#908) is the one gate on a counter
// sale's receipt carrying the customer order-tracking QR, printed or on
// screen: a normal sale (never a return/refund — there is no order to
// follow), the receipt setting on (rd.ShowTrackingQR), and the plan gate
// (orderTrackingOffered — Shop and up). Display-only, like the plan gate it
// wraps: it runs after the sale committed and only decides whether a QR is
// shown, so it lives here, never in a sale-path file
// (entitlement/salepath_imports_test.go).
//
// Two more exclusions (ut-docs#908 review): a sale that is not completed
// (a reprinted voided sale is no order anyone will prepare), and any till
// that follows a main till. On an additional till the Orders screen writes
// the status on the main till, and the main till never holds this sale's
// token — the LAN sale journal does not carry it (ADR-0070 amendment 1 §3,
// #4238) — so the link would never leave "untracked". Until #4238 moves
// tracking to the cloud, only the main till (or a single till) prints it.
func receiptTrackingQROffered(ctx context.Context, d *common.Deps, saleType, saleStatus string, rd receiptDesign) bool {
	if saleType != "sale" || saleStatus != "completed" || !rd.ShowTrackingQR {
		return false
	}
	if d != nil && d.SyncPrimaryURL(ctx) != "" {
		return false
	}
	return orderTrackingOffered(ctx, d)
}

// receiptTrackingPrint builds the printed receipt's tracking block for a
// sale that passed receiptTrackingQROffered: the sale's (idempotently
// minted) token, its tracking link (orderTrackingLink — never this till's
// own address, ut-docs#4260), that link as a GS v 0 raster, and the caption
// in the shop locale (English when the printer charset can't encode the
// translation — kitchenTicketText's rule). Best-effort by contract: any
// failure (token error, no link yet, encode error) returns empty values, so
// the receipt prints without the QR; it logs a warning but never the token,
// which is the customer's capability. The token is minted before the link
// check, so the sale keeps one stable token for any later surface or
// reprint.
func receiptTrackingPrint(ctx context.Context, d *common.Deps, receiptNo, locale, charset string) (qr []byte, caption, trackingURL string) {
	token, err := data.NewPOSRepo(d.Db).EnsureOrderTrackingToken(ctx, receiptNo)
	if err != nil || token == "" {
		logging.L().Warnf("order tracking: no tracking QR on printed receipt %s: token: %v", receiptNo, err)
		return nil, "", ""
	}
	u, ok := orderTrackingLink(token, locale)
	if !ok {
		return nil, "", ""
	}
	png, err := qrcode.Encode(u, qrcode.Medium, 240)
	if err != nil {
		logging.L().Warnf("order tracking: no tracking QR on printed receipt %s: encode: %v", receiptNo, err)
		return nil, "", ""
	}
	raster := print.RasterLogo(png)
	if raster == nil {
		logging.L().Warnf("order tracking: no tracking QR on printed receipt %s: raster encode failed", receiptNo)
		return nil, "", ""
	}
	return raster, kitchenTicketText(locale, charset, "receipt.tracking.caption"), u
}

// receiptTrackingView is the on-screen receipt's tracking block
// (receipt.html, data-testid="receipt-tracking-qr"); nil = no block.
// QRDataURI is template.URL for the same reason as
// receiptFiscalView.QRDataURI (ut-docs#906): html/template strips a data:
// URI held in a plain string.
type receiptTrackingView struct {
	QRDataURI template.URL
	URL       string
}

// receiptTrackingScreenView is the on-screen counterpart of
// receiptTrackingPrint, for the cashier's post-tender receipt: gate, then
// orderTrackingQRView (same idempotent token and link as the kiosk
// confirmation). nil when the gate fails or the QR could not be
// built — the receipt renders without it.
func receiptTrackingScreenView(r *http.Request, d *common.Deps, receiptNo, saleType, locale string, rd receiptDesign) *receiptTrackingView {
	if !receiptTrackingQROffered(r.Context(), d, saleType, "completed", rd) {
		return nil
	}
	qr, u := orderTrackingQRView(r, data.NewPOSRepo(d.Db), receiptNo, locale)
	if qr == "" {
		return nil
	}
	return &receiptTrackingView{QRDataURI: qr, URL: u}
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
		repo := data.NewPOSRepo(d.Db)
		o, found, err := repo.LookupOrderByTrackingToken(r.Context(), token)
		if err != nil {
			return data.TrackedOrder{}, token, false, err
		}
		if !found {
			// ut-docs#4206: no sale holds the token yet — it may be a
			// pay-at-counter order still parked as a held sale, its token
			// in the payload until the tender hands it to the sale. Same
			// not-found answer as before when no held order matches.
			held, heldFound, herr := repo.LookupHeldOrderByTrackingToken(r.Context(), token, time.Now().Add(-pos.OrderTrackingExpiry))
			if herr != nil {
				return data.TrackedOrder{}, token, false, herr
			}
			if !heldFound {
				return data.TrackedOrder{}, token, false, nil
			}
			held.Status = orderTrackingAwaitingPayment
			return held, token, true, nil
		}
		if !orderTrackingVisible(o, time.Now().UTC()) {
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
			// No error detail on an anonymous surface. Not RenderError:
			// this is a customer's own phone (real browser chrome, own
			// Back button), rendered without the operator base layout —
			// see this file's own top-of-file comment.
			http.Error(w, "lookup failed", http.StatusInternalServerError) // page-error:allow anonymous customer surface, no operator layout
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
			http.Error(w, "lookup failed", http.StatusInternalServerError) // page-error:allow htmx poll fragment on the same anonymous customer surface
			return
		}
		var viewData map[string]any
		switch {
		case ok:
			viewData = orderTrackingStatusData(o, token)
		case orderTrackingResumeGrace(r) > 0:
			// The page was showing "waiting for payment" and the held row
			// is gone: the cashier has most likely resumed the order and
			// is taking payment, so for a short while neither a held row
			// nor a sale holds the token (ut-docs#4206). Keep showing the
			// waiting state and polling, bounded by the counter, instead
			// of telling the customer the link expired. A forged ?w= on a
			// guessed token gets the same answer whether or not the token
			// ever existed, so not-found stays indistinguishable.
			viewData = orderTrackingStatusData(data.TrackedOrder{Status: orderTrackingAwaitingPayment}, token)
			viewData["Wait"] = orderTrackingResumeGrace(r) + 1
		default:
			viewData = map[string]any{"Expired": true, "Poll": false}
		}
		httpx.RenderWith(fragmentFiles, funcs)("order_tracking_status", viewData)(w, r)
	})
}
