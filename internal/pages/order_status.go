package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Order lifecycle status (ut-docs#526): one-tap new/preparing/ready/collected
// (+ terminal cancelled) on completed sales — the backbone the Kitchen
// Display (#516), pagers (#517) and customer tracking (#528/#527) will build
// on. Same shape as registerKitchenPrintAPI: operate on a receipt_no, no
// manager gate (marking an order ready is normal floor work), HTMX fragment
// response, journaled.
//
// The /orders page itself is a deliberately minimal placeholder surface —
// it proves the mechanism end to end; the real KDS/live-order view is #516's
// card, built on the same repo methods and broadcaster.
//
// Cross-till (ut-docs#1350): on a REPLICA (sync.primary_url set), both the
// list fragment and the one-tap write first try the primary's
// /api/sync/orders* endpoints (sync_orders.go) so every till sees and drives
// the whole shop's one live board; on ANY failure they silently fall back to
// the local-DB path below — an offline station keeps working exactly as
// before (offline-first, ADR-0003), same silent-retry stance as
// syncPushTick's "primary unreachable".
//
// Known, accepted limitation (ut-docs#1350 review — stated precisely, not
// glossed over): a tap applied via the LOCAL fallback (primary unreachable)
// is NOT queued or pushed anywhere — it only ever reaches the primary's
// board if that same till later proxies a FURTHER status change for the
// same receipt. Concretely, once the primary comes back, the very next
// 15s poll replaces the board with the primary's rows, which never saw the
// offline tap — so a status set while offline can visibly REVERT on that
// till's own screen (not just "fail to propagate elsewhere"). This is a
// real, till-visible behavior this feature introduces, not a carry-over of
// a pre-existing gap — the pre-feature local-only view never got
// overwritten by anything. Queuing/replaying the offline write is
// deliberately out of scope here (it would need exactly the general
// bidirectional sales-sync mechanism this design was scoped to avoid) —
// tracked as a follow-up, not silently accepted. Separately, and smaller: a
// sale rung up on a replica is invisible on ITS OWN /orders board (proxied
// to the primary, which hasn't seen the sale yet) until syncPushTick lands
// the journal — normally a few seconds via the SyncPushNow nudge, unbounded
// if a push is rejected.

// orderStatusLabelKey maps a stored status (” = untracked) to its locale
// key. Keys exist in every file under web/locales/ (guard-i18n.sh).
func orderStatusLabelKey(status string) string {
	if status == "" {
		return "orders.status.none"
	}
	return "orders.status." + status
}

// orderAction is one candidate action button an orders-board row may offer.
// orderActionCandidates below is the FIXED list, in display order — never
// duplicated as a second source of truth; allowedOrderActions is the one
// place that decides which of them apply to a given current status.
type orderAction struct {
	TargetStatus string
	Key          string // i18n key, present in every web/locales/*.json file
	Danger       bool
}

var orderActionCandidates = []orderAction{
	{pos.OrderStatusPreparing, "orders.status.preparing", false},
	{pos.OrderStatusReady, "orders.status.ready", false},
	{pos.OrderStatusCollected, "orders.status.collected", false},
	{pos.OrderStatusCancelled, "orders.btn.cancel", true},
}

// allowedOrderActions returns, in display order, the subset of
// orderActionCandidates that pos.OrderStatusAllowed(current, _) actually
// permits (ut-docs#1964) — reusing the SAME conflict-rule predicate the
// write path already enforces, so a row can never offer a button its own
// tap would silently no-op. Shared by the full-list render (orderRowsFor)
// and the post-write OOB actions fragment (writeOrderStatusFragment) below,
// so the two can never drift on which buttons a given status shows.
func allowedOrderActions(current string) []orderAction {
	out := make([]orderAction, 0, len(orderActionCandidates))
	for _, a := range orderActionCandidates {
		if pos.OrderStatusAllowed(current, a.TargetStatus) {
			out = append(out, a)
		}
	}
	return out
}

// renderOrderActionsButtons writes just the <button> markup for actions,
// shared between the actions-cell OOB fragment below and (indirectly, via
// the same allowedOrderActions call) the full-list template's own render —
// the template renders its own copy from orderRow.Actions using {{ T }},
// so this Go-side copy exists only for the OOB fragment write path, which
// isn't going through html/template.
func renderOrderActionsButtons(w http.ResponseWriter, locale, receiptNo string, actions []orderAction) {
	for _, a := range actions {
		class := "btn"
		if a.Danger {
			class += " danger"
		}
		fmt.Fprintf(w, `<button class="%s" hx-post="/api/orders/%s/status" hx-vals='{"status":"%s"}' hx-target="#order-status-%s" hx-swap="innerHTML">%s</button>`,
			class, url.PathEscape(receiptNo), a.TargetStatus,
			template.HTMLEscapeString(receiptNo), template.HTMLEscapeString(httpx.T(locale, a.Key)))
	}
}

// writeOrderActionsFragment renders the OOB actions-cell swap
// (order-actions-{receiptNo}, the list/kitchen-display board's own id) —
// a <div>, never <tr>/<td>, for the same standalone-parsing reasoning
// writeOrderStatusFragment's row-delete marker below documents. Safe to
// always emit regardless of which page posted: htmx no-ops on a missing
// target, and this id only exists on orders_list.html's rendering (shared
// by /orders and /kitchen-display/{station}) — order_view.html uses its
// own separate id (writeOrderCollectFragment) precisely so it never
// inherits this page's full Preparing/Ready/Cancel button set.
func writeOrderActionsFragment(w http.ResponseWriter, locale, receiptNo, status string) {
	fmt.Fprintf(w, `<div id="order-actions-%s" class="btn-actions" hx-swap-oob="outerHTML">`,
		template.HTMLEscapeString(receiptNo))
	renderOrderActionsButtons(w, locale, receiptNo, allowedOrderActions(status))
	fmt.Fprint(w, `</div>`)
}

// writeOrderCollectFragment renders order_view.html's OWN, narrower OOB
// swap (order-collect-{receiptNo}) — that page deliberately offers only a
// Collect button, never the full ladder, so it cannot reuse
// writeOrderActionsFragment's id/content (ut-docs#1964). Safe to always
// emit alongside it, same no-op-on-missing-target reasoning.
func writeOrderCollectFragment(w http.ResponseWriter, locale, receiptNo, status string) {
	fmt.Fprintf(w, `<span id="order-collect-%s" hx-swap-oob="outerHTML">`,
		template.HTMLEscapeString(receiptNo))
	if pos.OrderStatusAllowed(status, pos.OrderStatusCollected) {
		fmt.Fprintf(w, `<button class="btn" hx-post="/api/orders/%s/status" hx-vals='{"status":"collected"}' hx-target="#order-status-line" hx-swap="innerHTML" data-testid="collect-btn">%s</button>`,
			url.PathEscape(receiptNo), template.HTMLEscapeString(httpx.T(locale, "orders.status.collected")))
	}
	fmt.Fprint(w, `</span>`)
}

// writeOrderStatusFragment renders the current-state fragment the one-tap
// endpoint swaps into the status cell: status label + who/when. It renders
// the POST-write truth whether the write applied or was dropped as stale —
// a dropped write's response simply re-shows the unchanged current state.
//
// /orders is the active work queue (ut-docs#1389): once status is terminal
// (collected/cancelled — pos.IsTerminalOrderStatus), the row itself must
// leave the board immediately, not just on the next 15s poll. The status
// cell swap alone can't remove its own ancestor row, so the response also
// carries an htmx out-of-band "delete" targeting the row's id
// (order-row-{receiptNo}, set on the <tr> in orders_list.html) — htmx
// no-ops on a missing target, so this is safe to always emit for a
// terminal status, including when THIS write was itself a dropped no-op
// (e.g. cancel-after-collected, TestOrderStatusPost_CancelAfterCollectedStillRemovesRow):
// the order is terminal either way, so the row must not linger.
//
// The OOB element itself is a <div>, NOT a <tr> — deliberately, found by a
// real-browser e2e run, not just the Go fragment tests (which only proved
// the server SENT the marker, not that a browser could act on it). htmx's
// fragment parser only wraps the response in a <table> context when the
// FIRST tag in the whole response is table-related (thead/tr/td/…); here
// the response starts with the <span> status label, so a bare <tr> is
// parsed "in body" instead, where the HTML spec has the browser silently
// drop an out-of-context <tr> before htmx's own JS ever sees it — the swap
// target is matched purely by id, so the OOB element's own tag doesn't
// need to (and here must not) be <tr>; a <div> carries the same id/attribute
// and parses standalone with no table-context requirement.
func writeOrderStatusFragment(w http.ResponseWriter, locale, receiptNo, status, who, when string) {
	label := httpx.T(locale, orderStatusLabelKey(status))
	fmt.Fprintf(w, `<span class="order-status" data-status="%s">%s</span>`,
		template.HTMLEscapeString(status), template.HTMLEscapeString(label))
	if who != "" || when != "" {
		fmt.Fprintf(w, ` <span class="muted">%s · %s</span>`,
			template.HTMLEscapeString(who), template.HTMLEscapeString(when))
	}
	if pos.IsTerminalOrderStatus(status) {
		fmt.Fprintf(w, `<div id="order-row-%s" hx-swap-oob="delete"></div>`,
			template.HTMLEscapeString(receiptNo))
	}
	// ut-docs#1964: always carry BOTH pages' own actions-cell OOB swap in
	// the same response as the status cell — a tap that applies must make
	// its own (now-invalid) button disappear immediately, not wait for the
	// next 15s poll/SSE push. Each targets a different id
	// (order-actions-{receiptNo} for the list/kitchen-display board,
	// order-collect-{receiptNo} for order_view.html's own narrower
	// Collect-only scope) and htmx no-ops on whichever id the current page
	// didn't render, so it's safe to always emit both regardless of which
	// page's tap this was.
	writeOrderActionsFragment(w, locale, receiptNo, status)
	writeOrderCollectFragment(w, locale, receiptNo, status)
}

// orderStatusOutcome is the structured result of one guarded status write —
// what both the HTML fragment (human one-tap) and the JSON sync endpoint
// (sync_orders.go) render from.
type orderStatusOutcome struct {
	BadStatus bool // next isn't in the fixed vocabulary — nothing was attempted
	Found     bool // the receipt exists
	Applied   bool // the write moved the ladder forward (journaled + broadcast)
	Tracked   bool // a journal event exists — Status/Who/When are meaningful
	Status    string
	Who       string
	When      string
}

// applyOrderStatusCore is THE guarded status write, shared by the human
// one-tap handler below and the machine-to-machine sync endpoint
// (sync_orders.go, ut-docs#1350) so the two can never drift: validate the
// status → ApplyOrderStatus under pos.OrderStatusAllowed → journal audit +
// broadcast only on an APPLIED write (ut-docs#526 item 4) → read back
// LatestOrderStatus as the post-write truth (applied or dropped alike).
//
// actorID attributes the journal event and the broadcast — a users.id when
// the caller is (or resolved to) a known operator, the calling till's name
// only as a last resort when it isn't (order_status_events.actor_id has no
// FK, and the who/when fragment falls back to ActorID when it isn't a known
// user). auditActorID is the audit_log actor and MUST satisfy audit_log's
// users-FK — a resolved real operator id, or "system" (applyJournal's
// convention) when the sync caller's actor_id didn't resolve to a known
// user. sourceTill is recorded in the audit payload whenever the write came
// in over the sync surface (ut-docs#1350), regardless of whether the
// operator resolved — which till relayed the tap is worth keeping even when
// who tapped it is also known.
func applyOrderStatusCore(ctx context.Context, d *common.Deps, receiptNo, next, actorID, auditActorID, sourceTill string) (orderStatusOutcome, error) {
	if !pos.ValidOrderStatus(next) {
		return orderStatusOutcome{BadStatus: true}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	repo := data.NewPOSRepo(d.Db)
	applied, found, err := repo.ApplyOrderStatus(ctx, receiptNo, next, actorID, now,
		func(current string) bool { return pos.OrderStatusAllowed(current, next) })
	if err != nil {
		return orderStatusOutcome{}, err
	}
	if !found {
		return orderStatusOutcome{}, nil
	}
	if applied {
		payload := map[string]any{"status": next}
		if sourceTill != "" {
			payload["source_till"] = sourceTill
		}
		_ = repo.InsertAudit(ctx, nil, auditActorID, "sale", receiptNo, "order_status_changed",
			payload, now, "")
		if d.OrderStatus != nil {
			d.OrderStatus.Publish(pos.OrderStatusChanged{ReceiptNo: receiptNo, Status: next, ActorID: actorID, At: now})
		}
	}
	out := orderStatusOutcome{Found: true, Applied: applied}
	ev, ok, err := repo.LatestOrderStatus(ctx, receiptNo)
	if err != nil {
		return orderStatusOutcome{}, err
	}
	if !ok {
		// Sale exists but was never tracked (only possible when the write
		// was dropped): Tracked stays false — render the untracked state.
		return out, nil
	}
	out.Tracked = true
	out.Status = ev.Status
	out.Who = ev.ActorName
	if out.Who == "" {
		out.Who = ev.ActorID
	}
	out.When = ev.CreatedAt
	return out, nil
}

// orderProxyClient is the replica→primary client for the FOREGROUND order
// proxy (ut-docs#1350). Deliberately its own short timeout, not
// syncPushTick's 30s: /ui/orders is polled every 15s and the one-tap POST is
// a live tap on the floor — a slow/absent primary must degrade to the local
// path in a moment, never make the page feel stuck.
var orderProxyClient = &http.Client{Timeout: 3 * time.Second}

// replicaSyncTarget reports whether this till is a replica that can call its
// primary: base URL (no trailing slash) + bearer, ok=false when either is
// missing (a half-enrolled till behaves local-only, silently).
func replicaSyncTarget(ctx context.Context, d *common.Deps) (base, bearer string, ok bool) {
	primary := d.SyncPrimaryURL(ctx)
	if primary == "" || d.Settings == nil {
		return "", "", false
	}
	b, _, _ := d.Settings.Get(ctx, "sync.bearer")
	b = strings.TrimSpace(b)
	if b == "" {
		return "", "", false
	}
	return strings.TrimSuffix(primary, "/"), b, true
}

// fetchOrdersFromPrimary tries GET /api/sync/orders on the primary. ok=false
// on ANY failure — not a replica, network error, timeout, non-200, malformed
// body — and the caller falls through to the local list; the fallback is
// silent to the operator by design (Debugf only: at a 15s poll cadence an
// Info per miss would flood the Problems ring).
func fetchOrdersFromPrimary(ctx context.Context, d *common.Deps, client *http.Client) ([]data.OrderListEntry, bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/sync/orders", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("orders proxy: primary unreachable (%v) — using local list", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("orders proxy: primary answered %s — using local list", resp.Status)
		return nil, false
	}
	var out struct {
		Data []syncOrderRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Debugf("orders proxy: malformed primary response (%v) — using local list", err)
		return nil, false
	}
	entries := make([]data.OrderListEntry, 0, len(out.Data))
	for _, row := range out.Data {
		// ut-docs#1817: an older primary (pre-#1817) omits display_no from
		// the JSON entirely (its own syncOrderRow lacks the field), which
		// decodes as "" here -- fall back to ReceiptNo explicitly, the same
		// COALESCE(NULLIF(display_no,''),receipt_no) every SQL-layer reader
		// already applies, since this proxy path bypasses that SQL layer
		// completely (it reads the primary's JSON, never its DB).
		displayNo := row.DisplayNo
		if displayNo == "" {
			displayNo = row.ReceiptNo
		}
		entries = append(entries, data.OrderListEntry{
			ReceiptNo:            row.ReceiptNo,
			DisplayNo:            displayNo,
			OrderType:            row.OrderType,
			Status:               row.Status,
			StatusUpdatedAt:      row.StatusUpdatedAt,
			CreatedAt:            row.CreatedAt,
			KitchenPrintFailedAt: row.KitchenPrintFailedAt,
			ReceiptPrintFailedAt: row.ReceiptPrintFailedAt,
		})
	}
	return entries, true
}

// latestOrderStatusForReceipt is ut-docs#1818's replica-safe single-order
// status read, used by the scan router (pos_api.go) and the order view
// above. order_status_events is deliberately NOT part of the LAN-sync
// journal -- sync_admin_repo.go's exclusion list calls it a "live KDS
// status-change event stream" and says a periodic snapshot of it "would
// actively misbehave" -- and ADR-0079's SSE bridge only tells a replica to
// re-poll the primary, it never writes the replica's own tables. So a pure
// local repo.LatestOrderStatus call would silently report "never tracked"
// for every order on a replica, regardless of its real status -- exactly
// the wrong answer for a feature whose whole point is not dropping the
// operator onto the refund screen for a still-active order (an earlier
// draft of this function claimed local-only was fine here; it wasn't --
// review finding, ut-docs#1818).
//
// Mirrors fetchOrdersFromPrimary's own proxy-then-local-fallback shape: on
// a reachable replica, search the primary's active-orders list (the same
// bounded, non-terminal list /ui/orders and the sync bridge already use)
// for this receipt. A hit there is unambiguous -- an active order can only
// be new/preparing/ready. A miss is NOT proof the order was never tracked
// (a collected/cancelled order also leaves that list immediately,
// ut-docs#1389, and an old-enough order can fall off the bound), so a miss
// falls through to the local read exactly as the primary/standalone path
// always has. Known, accepted gap: a replica cannot distinguish
// collected/cancelled/never-tracked/too-old for an order that isn't
// currently active -- all four already resolve to "refund" (today's
// unchanged behaviour) or an empty local read, so this can never make a
// scan LESS safe than before ut-docs#1818, only fail to improve that
// already-narrow edge case.
func latestOrderStatusForReceipt(ctx context.Context, d *common.Deps, repo *data.POSRepo, receiptNo string) (status string, tracked bool) {
	if entries, ok := fetchOrdersFromPrimary(ctx, d, orderProxyClient); ok {
		for _, e := range entries {
			if e.ReceiptNo == receiptNo && e.Status != "" {
				return e.Status, true
			}
		}
	}
	if ev, ok, err := repo.LatestOrderStatus(ctx, receiptNo); err == nil && ok {
		return ev.Status, true
	}
	return "", false
}

// applyOrderStatusOnPrimary tries POST /api/sync/orders/{receipt_no}/status
// on the primary. ok=false on ANY failure — including the primary answering
// 404 (a sale that exists here but hasn't journaled there yet) or 400 — and
// the caller falls back to applying the write locally, exactly as an offline
// till does. actorID (this till's own session user — getSessionUserID's
// "system" fallback under UT_AUTH=off means this is never actually empty
// in practice, but the empty check below is harmless defense in depth)
// rides along so the PRIMARY's audit trail can attribute the real operator
// instead of just "some till changed this" (ut-docs#1350 review) — the
// primary only honors it once it resolves to a real row in ITS OWN users
// table (see sync_orders.go), never an unvalidated string.
func applyOrderStatusOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, receiptNo, next, actorID string) (orderStatusOutcome, bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return orderStatusOutcome{}, false
	}
	form := url.Values{"status": {next}}
	if actorID != "" {
		form.Set("actor_id", actorID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/sync/orders/"+url.PathEscape(receiptNo)+"/status",
		strings.NewReader(form.Encode()))
	if err != nil {
		return orderStatusOutcome{}, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("orders proxy: primary unreachable (%v) — applying status locally", err)
		return orderStatusOutcome{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("orders proxy: primary answered %s — applying status locally", resp.Status)
		return orderStatusOutcome{}, false
	}
	var out struct {
		Data *syncOrderStatusResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data == nil {
		return orderStatusOutcome{}, false
	}
	return orderStatusOutcome{
		Found:   true,
		Applied: out.Data.Applied,
		Tracked: out.Data.Tracked,
		Status:  out.Data.Status,
		Who:     out.Data.Who,
		When:    out.Data.When,
	}, true
}

// orderStreamHeartbeat is the SSE comment-line cadence (ADR-0079). A var,
// not a const, only so the stream tests can shorten it; 20s is well inside
// any sane proxy/NAT idle cutoff, and the replica bridge's own idle cutoff
// (orderStreamBridgeIdleCutoff) is sized against it — keep them in step.
var orderStreamHeartbeat = 20 * time.Second

// streamOrderStatus is THE order-status SSE writer loop (ADR-0079,
// ut-docs#1571), shared by the session-authed browser endpoint
// (GET /api/orders/stream, below) and the bearer-authed replica endpoint
// (GET /api/sync/orders/stream, sync_orders.go) so the two framings can never
// drift — each caller does its own auth first, then hands off here.
//
// One `event: order-status` frame per pos.OrderStatusChanged published on
// d.OrderStatus (data = its snake_case JSON), flushed immediately; a `: ping`
// comment every orderStreamHeartbeat so an idle connection survives proxies
// and the bridge can tell "quiet shop" from "dead primary". Returns — and
// unsubscribes — the moment the client goes away (r.Context().Done()) or
// the broadcaster is closed at process shutdown (init.go wires that to
// bgCtx; without it every open stream would hold http.Server.Shutdown to
// its full timeout). Same defensive stance as the rest of this package: a
// writer that can't flush, or no broadcaster wired, is a 500 up front rather
// than a stream that looks connected and never delivers.
func streamOrderStatus(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "orders",
			errors.New("order status stream: response writer cannot flush"))
		return
	}
	if d.OrderStatus == nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "orders",
			errors.New("order status stream: no broadcaster wired"))
		return
	}
	// Subscribe BEFORE the headers go out: a client that has seen 200 must
	// not miss an event published in the gap.
	ch, unsubscribe := d.OrderStatus.Subscribe()
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // any buffering reverse proxy in front: pass frames through
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(orderStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return // broadcaster closed — process shutting down
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue // cannot happen for four strings; never let it kill the stream
			}
			if _, err := fmt.Fprintf(w, "event: order-status\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// orderRow is one rendered row of ui/partials/orders_list.html — shared by
// the shop-wide /ui/orders board and the per-station /ui/kitchen-display
// fragment (ut-docs#544), so the two can never drift in shape.
type orderRow struct {
	ReceiptNo string
	// DisplayNo (ut-docs#1817) is the short customer-facing order number
	// the template shows instead of ReceiptNo — already resolved to
	// ReceiptNo when the sale has none (data.OrderListEntry.DisplayNo's own
	// COALESCE). ReceiptNo above stays the row's real identity for the
	// /journal/{receipt_no} link and the /api/orders/{receipt_no}/status
	// target.
	DisplayNo       string
	OrderType       string
	StatusKey       string
	Status          string
	StatusUpdatedAt string
	CreatedAt       string
	// ut-docs#517a: latest kitchen/receipt print attempt failed —
	// surfaced as an inline warning next to the status.
	KitchenPrintFailed bool
	ReceiptPrintFailed bool
	// ut-docs#1350 review round 2: a row sourced from the PRIMARY's
	// board may name a receipt this till's own DB has never heard of
	// (sales only ever journal replica→primary, never back down) —
	// linking it to /journal/{receipt_no} would 404. This is
	// checked PER ROW, not assumed false for the whole primary-
	// sourced batch: a replica's OWN sale journals to the primary
	// too, so once it's landed there, a row for a sale THIS till
	// actually took is both primary-sourced AND locally resolvable
	// — a blanket "primary-sourced ⇒ no link" would wrongly kill a
	// working link on the till's own orders every time the primary
	// is reachable (caught by review: the help docs would then be
	// describing behavior the code didn't actually have).
	JournalLinkable bool
	// Actions (ut-docs#1964) is the subset of orderActionCandidates this
	// row's current Status actually allows next, in display order —
	// computed once here via allowedOrderActions so the template never
	// re-derives the rule itself.
	Actions []orderAction
}

// orderRowsFor maps repo entries to rendered rows. fromPrimary=true runs the
// per-row local-existence check described on orderRow.JournalLinkable;
// entries read from this till's own DB are always linkable.
func orderRowsFor(ctx context.Context, repo *data.POSRepo, entries []data.OrderListEntry, fromPrimary bool) []orderRow {
	rows := make([]orderRow, 0, len(entries))
	for _, e := range entries {
		linkable := true
		if fromPrimary {
			// Bounded to the page's own 50-row cap; a local existence
			// check per row is a handful of sub-millisecond embedded-
			// SQLite lookups on a 15s poll, not a hot path.
			var err error
			linkable, err = repo.ReceiptExists(ctx, e.ReceiptNo)
			if err != nil {
				linkable = false // fail closed to "no link", never a 404
			}
		}
		rows = append(rows, orderRow{
			ReceiptNo:          e.ReceiptNo,
			DisplayNo:          e.DisplayNo,
			OrderType:          e.OrderType,
			StatusKey:          orderStatusLabelKey(e.Status),
			Status:             e.Status,
			StatusUpdatedAt:    e.StatusUpdatedAt,
			CreatedAt:          e.CreatedAt,
			KitchenPrintFailed: e.KitchenPrintFailedAt != "",
			ReceiptPrintFailed: e.ReceiptPrintFailedAt != "",
			JournalLinkable:    linkable,
			Actions:            allowedOrderActions(e.Status),
		})
	}
	return rows
}

func registerOrderStatus(mux *http.ServeMux, d *common.Deps) {
	// Minimal recent-orders page: list + one-tap buttons, loaded as a
	// fragment so a tap can swap just the row's status cell.
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		httpx.Render("ui/pages/orders.html", map[string]any{
			"title":     "Order status",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		})(w, r)
	})

	// Single-order view (ut-docs#1818): the destination for a scanned
	// receipt whose order isn't collected yet (see resolveReceiptScanDestination
	// in pos_api.go) -- lines, total and status, with a one-tap Collect
	// action that posts through the SAME /api/orders/{receipt_no}/status
	// endpoint and OrderStatusAllowed guard the /orders board already uses
	// (no new mutation path). Not-found vs. not-completed mirrors
	// /refund/{receipt}'s own split (refund_page.go): an unknown receipt
	// goes to the /journal LIST (nothing to link to), a real but
	// non-completed sale goes to /journal/{receipt} (a real page there).
	mux.HandleFunc("GET /orders/{receipt}", func(w http.ResponseWriter, r *http.Request) {
		receipt := strings.TrimSpace(r.PathValue("receipt"))
		repo := data.NewPOSRepo(d.Db)
		detail, found, err := repo.GetSaleDetail(r.Context(), receipt)
		if err != nil || !found {
			http.Redirect(w, r, "/journal", http.StatusSeeOther)
			return
		}
		if detail.SaleType != "sale" || detail.Status != "completed" {
			http.Redirect(w, r, "/journal/"+url.PathEscape(receipt), http.StatusSeeOther)
			return
		}
		status, _ := latestOrderStatusForReceipt(r.Context(), d, repo, receipt)
		switch status {
		case pos.OrderStatusCollected:
			// Nothing left to collect -- the scan router already sends a
			// collected order straight to refund; a direct/stale hit on
			// this URL (e.g. a reload after tapping Collect) follows suit.
			http.Redirect(w, r, "/refund/"+url.PathEscape(receipt), http.StatusSeeOther)
			return
		case pos.OrderStatusCancelled:
			http.Redirect(w, r, "/orders", http.StatusSeeOther)
			return
		}
		httpx.Render("ui/pages/order_view.html", map[string]any{
			"title":     "Order",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Sale":      detail,
			"Status":    status,
			"StatusKey": orderStatusLabelKey(status),
		})(w, r)
	})

	// Live order-status push for the browser (ADR-0079, ut-docs#1571):
	// orders.html opens a native EventSource here and turns every event into
	// an immediate re-load of the same /ui/orders fragment the 15s poll
	// already renders — SSE only changes how FAST the board is asked to
	// refresh, never what it shows, and the poll stays as the offline-first
	// fallback. Session-authed like every other page route (the auth
	// middleware gates it; nothing special here). On a replica, the events
	// arriving on d.OrderStatus include those the background bridge
	// (order_status_stream_bridge.go) republished from the primary — a page
	// never needs to know which till it is on.
	mux.HandleFunc("GET /api/orders/stream", func(w http.ResponseWriter, r *http.Request) {
		streamOrderStatus(w, r, d)
	})

	mux.HandleFunc("GET /ui/orders", func(w http.ResponseWriter, r *http.Request) {
		// Replica: the primary's board is the live truth while reachable
		// (ut-docs#1350); ANY failure falls through to the local list.
		entries, fromPrimary := fetchOrdersFromPrimary(r.Context(), d, orderProxyClient)
		if !fromPrimary {
			var err error
			entries, err = data.NewPOSRepo(d.Db).ListRecentOrders(r.Context(), 50)
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "orders", err)
				return
			}
		}
		rows := orderRowsFor(r.Context(), data.NewPOSRepo(d.Db), entries, fromPrimary)
		httpx.RenderPartial("ui/partials/orders_list.html", map[string]any{
			"Orders":      rows,
			"FragmentURL": "/ui/orders",
			"EmptyKey":    "orders.empty",
			// ut-docs#2098: the shop-wide board resends every destination,
			// same as before — explicit "" (not an absent key) so the
			// template's {{ .StationID }} never has to guess.
			"StationID": "",
		})(w, r)
	})

	// One-tap status change. Any operator may fire it (same reasoning as
	// the kitchen-ticket print: prep progress is floor work, not a manager
	// action). The conflict rule (pos.OrderStatusAllowed, decided per
	// ADR-0011's fixed-and-simple philosophy) makes a stale/backward tap a
	// silent 200 no-op showing the unchanged current state — never an error,
	// never a visible regression.
	mux.HandleFunc("POST /api/orders/{receipt_no}/status", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		receiptNo := strings.TrimSpace(r.PathValue("receipt_no"))
		next := strings.TrimSpace(r.Form.Get("status"))
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fail := func(status int, key string) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, key))
		}
		if !pos.ValidOrderStatus(next) {
			fail(http.StatusBadRequest, "orders.err.bad_status")
			return
		}
		actorID := getSessionUserID(r)
		// Replica: apply the tap on the PRIMARY's board while reachable
		// (ut-docs#1350) and render its post-write truth; ANY failure falls
		// back to the local write below, exactly as an offline till.
		if res, ok := applyOrderStatusOnPrimary(r.Context(), d, orderProxyClient, receiptNo, next, actorID); ok {
			writeOrderStatusFragment(w, locale, receiptNo, res.Status, res.Who, res.When)
			return
		}
		res, err := applyOrderStatusCore(r.Context(), d, receiptNo, next, actorID, actorID, "")
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "orders", err)
			return
		}
		if !res.Found {
			fail(http.StatusNotFound, "orders.err.not_found")
			return
		}
		// Render the post-write truth (applied or dropped alike) from the
		// journal's newest event — its actor/time is the current state's
		// who/when. Tracked=false (sale exists but was never tracked, only
		// possible when the write was dropped) renders the untracked state
		// via the outcome's zero Status/Who/When.
		writeOrderStatusFragment(w, locale, receiptNo, res.Status, res.Who, res.When)
	})
}
