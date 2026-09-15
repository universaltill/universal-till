package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till held-sale (parked order) write-through, replica side (ADR-0093,
// ut-docs#1920): hold_api.go's park path (parkCurrentBasket) calls
// heldSaleWriteThrough instead of repo.Upsert/Insert directly, its resume
// path (resumeHeldSale) calls heldSaleDeleteWriteThrough instead of
// repo.Delete, the held/table move handler re-pushes the moved row through
// heldSaleWriteThrough, and the open-orders page's listOpenOrders merges its
// local read with mergeHeldSalesWithPrimary — so on a REPLICA the PRIMARY's
// held_sales (sync_held_sales.go) is the shop-wide copy while reachable: an
// order parked on till A is listed on, and resumable from, till B. On ANY
// failure reaching the primary (not a replica, network error, timeout,
// non-200, malformed body) every one of these falls back — silently — to
// exactly the local-only call the caller made before this ADR, so a till
// that cannot reach the primary parks, lists, moves and resumes precisely
// as it always has (offline-first, ADR-0003; same fallback stance as
// claimTableWriteThrough and voucherRedeemWriteThrough).
//
// Known, accepted limitations, stated plainly (same class as
// tables_claim_proxy.go's own note — bounded to an outage window, not a
// regression on anything that worked before):
//
//   - A park or delete taken via the LOCAL fallback while the primary was
//     unreachable is not queued or replayed. The primary learns of a
//     fallback park on this till's next write to the same order (re-park,
//     move); it never learns of a fallback delete, so a row it still holds
//     for an order this till has since resumed — and possibly paid — is
//     listed shop-wide until something removes it. The delete path
//     therefore logs at Warn (Problems panel) when a replica's primary-side
//     delete fails, exactly as releaseVoucherOnPrimary does for the
//     equivalent orphan (ADR-0084 Decision 4).
//   - The reverse ghost: till A's own local row for an order till B has
//     since resumed stays on A until A next touches it. The merge keeps a
//     local row the primary no longer lists, deliberately — it cannot tell
//     "resumed elsewhere" from "parked here during an outage", and
//     offline-first says a locally parked order is never dropped on a
//     network hint. A re-park of that ghost re-creates it on the primary
//     under the same id; last-writer-wins, per ADR-0093's non-goals.

// heldSaleProxyClient is the replica→primary client for every call in this
// file. Same 800ms budget as tableClaimProxyClient, for the same reason: a
// park sits on the hold tap and the merge sits on a page render a cashier
// is standing in front of — a blackholed primary must degrade to the local
// path in well under a second, never make the till feel frozen.
var heldSaleProxyClient = &http.Client{Timeout: 800 * time.Millisecond}

// postHeldSaleOnPrimary is the shared bearer-authed JSON POST behind the
// two write proxies below: base/api/sync/held-sales/{action}. ok=false on
// ANY failure; a 200 with a decodable body is handed back for the caller to
// read its data object.
func postHeldSaleOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, action string, body, out any) bool {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return false
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/held-sales/"+action, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("held sale proxy: primary unreachable on %s (%v) — using local", action, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("held sale proxy: primary answered %s on %s — using local", resp.Status, action)
		return false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		logging.L().Debugf("held sale proxy: malformed primary response on %s (%v) — using local", action, err)
		return false
	}
	return true
}

// upsertHeldSaleOnPrimary tries POST /api/sync/held-sales/upsert on the
// primary. ok=false on ANY failure — not a replica, network error, timeout,
// non-200, malformed body — and the caller falls back to its local write.
// When ok, applied is the primary's authoritative answer (false: a newer
// version of this order already exists there) and row is the row as the
// primary now holds it, nil only if it vanished between write and
// read-back.
//
// updated_at is deliberately blanked on the wire: this till is making a
// content change NOW, and the primary stamps it on ITS clock
// (UpsertIfNewer), so every write that passes through the primary is
// ordered by one clock and a replica whose clock runs behind can never
// lose a genuine latest write to skew. The one row that carries a stamp
// back is the primary's own answer, which the caller mirrors verbatim.
func upsertHeldSaleOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, h data.HeldSale) (row *data.HeldSale, applied, ok bool) {
	var out struct {
		Data *syncHeldSaleUpsertResult `json:"data"`
	}
	wire := heldSaleToSyncRow(h)
	wire.UpdatedAt = ""
	if !postHeldSaleOnPrimary(ctx, d, client, "upsert", wire, &out) || out.Data == nil {
		return nil, false, false
	}
	if out.Data.Row == nil {
		return nil, out.Data.Applied, true
	}
	r := heldSaleFromSyncRow(*out.Data.Row)
	return &r, out.Data.Applied, true
}

// deleteHeldSaleOnPrimary tries POST /api/sync/held-sales/delete on the
// primary. ok=false on ANY failure, same contract as
// upsertHeldSaleOnPrimary; the caller's local delete proceeds regardless.
func deleteHeldSaleOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, id string) (ok bool) {
	var out struct {
		Data *syncHeldSaleDeleteResult `json:"data"`
	}
	if !postHeldSaleOnPrimary(ctx, d, client, "delete", syncHeldSaleDeleteRequest{ID: id}, &out) || out.Data == nil {
		return false
	}
	return true
}

// fetchHeldSalesFromPrimary tries GET /api/sync/held-sales on the primary.
// ok=false on ANY failure — not a replica, network error, timeout, non-200,
// malformed body (a null data array included: "the primary answered but
// said nothing" must not read as "the primary has no parked orders", or a
// half-broken primary would hide every other till's orders from this
// page) — and the caller falls through to its local list. Read-only, no
// side effect on the primary.
func fetchHeldSalesFromPrimary(ctx context.Context, d *common.Deps, client *http.Client) ([]data.HeldSale, bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/sync/held-sales", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("held sale proxy: primary unreachable on list (%v) — using local", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("held sale proxy: primary answered %s on list — using local", resp.Status)
		return nil, false
	}
	var out struct {
		Data *[]syncHeldSaleRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data == nil {
		logging.L().Debugf("held sale proxy: malformed primary response on list — using local")
		return nil, false
	}
	rows := make([]data.HeldSale, 0, len(*out.Data))
	for _, r := range *out.Data {
		rows = append(rows, heldSaleFromSyncRow(r))
	}
	return rows, true
}

// heldSaleWriteThrough is THE persist call behind parking an order
// (parkCurrentBasket, both its first-park and re-park branches) and behind
// the held/table move handler's re-push: on a replica with a reachable
// primary, the primary's write is authoritative — the row it answers with
// (its own stamp, its own contents) is mirrored into the local held_sales
// via repo.Upsert (best-effort: a mirror failure is logged, never fails the
// park — the primary already holds the order, which is what makes it
// visible shop-wide, and this till's own list picks the row back up on its
// next merge). A refused write (applied=false: another till updated this
// order more recently) is NOT an error either — the primary's newer
// version is what gets mirrored, and the cashier's older write is dropped,
// logged at Warn so it lands on the Problems panel: that is ADR-0093's
// last-writer-wins-with-clean-refusal, and with one-second stamps taken on
// the primary's clock it only ever fires on a genuinely concurrent edit
// from a second till. Anything else (not a replica, or any failure) is the
// local write exactly as before this ADR.
//
// That local fallback is repo.Upsert for BOTH of parkCurrentBasket's
// branches, where the first-park branch used to call repo.Insert: for a
// freshly minted id Upsert IS an insert (no conflict is possible, and an
// empty CreatedAt takes the same datetime('now') default), and one fallback
// path instead of two is exactly the "second, slightly different parking
// code path" hazard parkCurrentBasket's own comment warns about.
func heldSaleWriteThrough(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, h data.HeldSale) error {
	row, applied, ok := upsertHeldSaleOnPrimary(ctx, d, heldSaleProxyClient, h)
	if !ok {
		return repo.Upsert(ctx, h)
	}
	if !applied {
		logging.L().Warnf("held sale %s: a newer version of this parked order already exists on the primary — this till's write was dropped and the primary's version adopted (ADR-0093 last-writer-wins)", h.ID)
	}
	mirror := h
	if row != nil {
		mirror = *row
	}
	if err := repo.Upsert(ctx, mirror); err != nil {
		logging.L().Debugf("held sale proxy: local mirror of primary-held %s failed: %v", h.ID, err)
	}
	return nil
}

// heldSaleDeleteWriteThrough is THE delete call behind resuming an order
// (resumeHeldSale): the primary is told first (so the order stops being
// listed on every other till the moment it goes live here), and the local
// row is ALWAYS deleted afterwards regardless of the primary's answer —
// the resume has already restored the basket, and a stale local row is
// the lesser evil exactly as resumeHeldSale's own comment already accepts.
// A failed primary-side delete on a replica is the one outcome here that
// does not self-heal (see the file comment's first limitation), so it is
// logged at Warn rather than the usual Debug.
//
// Detached from the request's cancellation (context.WithoutCancel), same
// as releaseVoucherOnPrimary (voucher_sync_proxy.go) and for the identical
// reason: this sits behind an htmx resume tap, and a cashier's fast
// double-tap or navigation-away must not cancel the in-flight primary
// delete and strand exactly the un-self-healing orphan this comment's
// first sentence describes (independent review, ut-docs#1920 — the first
// draft used the raw request context and could do exactly that).
func heldSaleDeleteWriteThrough(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, id string) error {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), heldSaleProxyClient.Timeout)
	defer cancel()
	if ok := deleteHeldSaleOnPrimary(pctx, d, heldSaleProxyClient, id); !ok {
		if _, _, isReplica := replicaSyncTarget(ctx, d); isReplica {
			logging.L().Warnf("held sale %s: could not remove this parked order from the primary — it stays listed on other tills until it is next re-parked here or a manager resumes it there (ADR-0093)", id)
		}
	}
	return repo.Delete(ctx, id)
}

// mergeHeldSalesWithPrimary folds the primary's live parked-order list into
// this till's own (ADR-0093 Decision 4), for the open-orders page and the
// sale screen's parked-orders popup: a primary row absent locally is
// included; a row present on both sides keeps whichever has the greater
// updated_at (a tie keeps the local copy — nothing to change). ANY failure
// reaching the primary returns local untouched, so the page renders exactly
// the local-only list it always has and never blocks on the primary.
//
// DISPLAY-ONLY, deliberately — this must NEVER write a primary row into
// this till's own held_sales (independent review, ut-docs#1920: an earlier
// draft mirrored every merged-in row locally on every render, which left a
// permanent ghost after the order was resumed and paid elsewhere — the
// origin till's table then read occupied on every OTHER till forever
// [IsTableFree/ListTablesWithState both count local held_sales rows], and
// the ghost stayed tap-to-resume, i.e. tender an already-paid sale a
// second time. Same class of bug the ADR's own non-goals rule out, just
// reached through the merge instead of through a deliberate feature). This
// now matches every sibling cross-till READ in this codebase exactly:
// tablesWithStateForDisplay (tables_sync_proxy.go) ORs occupancy in without
// writing, and fetchOrdersFromPrimary/latestOrderStatusForReceipt
// (order_status.go) never touch this till's own tables either — a render
// must never be a write. Resumability across tills is handled separately,
// on demand, by resumeHeldSaleWithPrimaryFallback below: a row is only ever
// persisted locally at the moment THIS till is actually about to become the
// one holding it, which is also the only moment "this till owns it now" is
// actually true.
//
// The one primary row deliberately skipped is the order currently LIVE in
// this till's own basket (Engine.HeldOrigin): its row is mid-flight by
// definition — deleted here on resume, re-created on re-park — and if the
// primary-side delete failed (the file comment's first limitation) listing
// it would offer the cashier a stale parked copy of the order they are
// ringing up right now.
func mergeHeldSalesWithPrimary(ctx context.Context, d *common.Deps, local []data.HeldSale) []data.HeldSale {
	remote, ok := fetchHeldSalesFromPrimary(ctx, d, heldSaleProxyClient)
	if !ok {
		return local
	}
	liveID := ""
	if d.Engine != nil {
		liveID = d.Engine.HeldOrigin().ID
	}
	byID := make(map[string]int, len(local))
	for i, h := range local {
		byID[h.ID] = i
	}
	out := append([]data.HeldSale(nil), local...)
	for _, r := range remote {
		if r.ID == "" || r.ID == liveID {
			continue
		}
		if i, seen := byID[r.ID]; seen {
			if r.UpdatedAt <= out[i].UpdatedAt {
				continue
			}
			out[i] = r
		} else {
			byID[r.ID] = len(out)
			out = append(out, r)
		}
	}
	// HeldSalesRepo.List's own order, so a merged-in row sits by age like
	// every other rather than tacked on at the end.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// heldSaleFromPrimaryByID finds one row by id in the primary's live list.
// ok=false covers "not a replica"/any transport failure (same contract as
// fetchHeldSalesFromPrimary) AND "reachable but no such id" alike — the
// caller cannot and must not distinguish those, same stance every other
// proxy in this file takes. There is no single-row primary endpoint; this
// is deliberately a list-and-find rather than a fourth route, since the
// only caller (resume's on-demand fallback below) is already an
// off-hot-path, once-per-miss case, not a per-render cost.
func heldSaleFromPrimaryByID(ctx context.Context, d *common.Deps, client *http.Client, id string) (data.HeldSale, bool) {
	remote, ok := fetchHeldSalesFromPrimary(ctx, d, client)
	if !ok {
		return data.HeldSale{}, false
	}
	for _, r := range remote {
		if r.ID == id {
			return r, true
		}
	}
	return data.HeldSale{}, false
}

// resumeHeldSaleWithPrimaryFallback is resumeHeldSale's (hold_api.go) first
// step when the id isn't in this till's own held_sales: before giving up
// with resumeNotFound, ask the primary directly (ADR-0093) — the order may
// have been parked on, or last moved by, a different till and never
// mirrored here (mergeHeldSalesWithPrimary above deliberately never mirrors
// on a render). Found on the primary → the row is upserted locally NOW,
// the one moment this till is genuinely about to become the one holding
// it, and the caller proceeds exactly as if repo.Get had found it. Not
// found there either, or any failure reaching the primary → ok=false,
// unchanged from today's local-only "no such order".
func resumeHeldSaleWithPrimaryFallback(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, id string) (data.HeldSale, bool) {
	row, ok := heldSaleFromPrimaryByID(ctx, d, heldSaleProxyClient, id)
	if !ok {
		return data.HeldSale{}, false
	}
	if err := repo.Upsert(ctx, row); err != nil {
		logging.L().Debugf("held sale proxy: local mirror of primary-held %s failed at resume: %v", id, err)
	}
	return row, true
}
