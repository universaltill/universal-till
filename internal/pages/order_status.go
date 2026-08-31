package pages

import (
	"context"
	"encoding/json"
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

// writeOrderStatusFragment renders the current-state fragment the one-tap
// endpoint swaps into the status cell: status label + who/when. It renders
// the POST-write truth whether the write applied or was dropped as stale —
// a dropped write's response simply re-shows the unchanged current state.
func writeOrderStatusFragment(w http.ResponseWriter, locale, status, who, when string) {
	label := httpx.T(locale, orderStatusLabelKey(status))
	fmt.Fprintf(w, `<span class="order-status" data-status="%s">%s</span>`,
		template.HTMLEscapeString(status), template.HTMLEscapeString(label))
	if who != "" || when != "" {
		fmt.Fprintf(w, ` <span class="muted">%s · %s</span>`,
			template.HTMLEscapeString(who), template.HTMLEscapeString(when))
	}
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
		entries = append(entries, data.OrderListEntry{
			ReceiptNo:            row.ReceiptNo,
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

func registerOrderStatus(mux *http.ServeMux, d *common.Deps) {
	// Minimal recent-orders page: list + one-tap buttons, loaded as a
	// fragment so a tap can swap just the row's status cell.
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		httpx.Render("ui/pages/orders.html", map[string]any{
			"title":     "Orders",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		})(w, r)
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
		type orderRow struct {
			ReceiptNo       string
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
		}
		repo := data.NewPOSRepo(d.Db)
		rows := make([]orderRow, 0, len(entries))
		for _, e := range entries {
			linkable := true
			if fromPrimary {
				// Bounded to the page's own 50-row cap; a local existence
				// check per row is a handful of sub-millisecond embedded-
				// SQLite lookups on a 15s poll, not a hot path.
				var err error
				linkable, err = repo.ReceiptExists(r.Context(), e.ReceiptNo)
				if err != nil {
					linkable = false // fail closed to "no link", never a 404
				}
			}
			rows = append(rows, orderRow{
				ReceiptNo:          e.ReceiptNo,
				OrderType:          e.OrderType,
				StatusKey:          orderStatusLabelKey(e.Status),
				Status:             e.Status,
				StatusUpdatedAt:    e.StatusUpdatedAt,
				CreatedAt:          e.CreatedAt,
				KitchenPrintFailed: e.KitchenPrintFailedAt != "",
				ReceiptPrintFailed: e.ReceiptPrintFailedAt != "",
				JournalLinkable:    linkable,
			})
		}
		httpx.RenderPartial("ui/partials/orders_list.html", map[string]any{"Orders": rows})(w, r)
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
			writeOrderStatusFragment(w, locale, res.Status, res.Who, res.When)
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
		writeOrderStatusFragment(w, locale, res.Status, res.Who, res.When)
	})
}
