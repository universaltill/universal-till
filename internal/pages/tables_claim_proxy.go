package pages

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table-claim write-through, replica side (ut-docs#1703): the
// live basket's table pick (pos_api.go's /api/pos/table) and the hold/resume
// re-claim (hold_api.go) call claimTableWriteThrough instead of
// repo.ClaimTable directly, and every release site goes through
// releaseTableClaimWriteThrough, so on a REPLICA the PRIMARY's table_claims
// (sync_tables_claim.go) is the shop-wide arbiter while reachable: the
// primary's claimed/refused answer is authoritative, and a granted claim is
// mirrored into the local row so this till's own floor plan / picker keep
// reading it as occupied even if the primary drops off afterwards. On ANY
// failure reaching the primary (not a replica, network error, timeout,
// non-200, malformed body) the pick falls back — silently — to the local
// claim (the pre-#1703 behaviour, see claimTableWriteThrough), so an offline
// station keeps working as before (offline-first, ADR-0003; same fallback
// stance as applyOrderStatusOnPrimary).
//
// Known, accepted limitation, stated plainly (same class as
// order_status.go's offline-tap note): a claim taken via the LOCAL fallback
// while the primary was unreachable is not queued or replayed — the primary
// never learns of it, so another till can claim the same table there until
// this till next talks to the primary about it (its release still goes
// through, fire-and-forget, once reachable again). This is the pre-#1703
// behaviour, bounded to the outage window, not a regression.

// tableClaimProxyClient is the replica→primary client for the claim/release
// write-through. Same 800ms budget as tables_sync_proxy.go's
// tablesProxyClient, deliberately shorter than order_status.go's 3s: a
// table pick re-renders the basket in place on the floor and the release
// sites sit on tender/reset/hold — a blackholed primary must degrade to the
// local path in well under a second, never make the till feel frozen.
var tableClaimProxyClient = &http.Client{Timeout: 800 * time.Millisecond}

// postTableClaimOnPrimary is the shared bearer-authed form POST behind the
// two proxies below. ok=false on ANY failure; a 200 with a decodable
// `{"data":{...}}` object is handed back for the caller to read its field.
func postTableClaimOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, action, tableID string, out any) bool {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return false
	}
	form := url.Values{"table_id": {tableID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/tables/"+action, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("table claim proxy: primary unreachable on %s (%v) — using local claim", action, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("table claim proxy: primary answered %s on %s — using local claim", resp.Status, action)
		return false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		logging.L().Debugf("table claim proxy: malformed primary response on %s (%v) — using local claim", action, err)
		return false
	}
	return true
}

// claimTableOnPrimary tries POST /api/sync/tables/claim on the primary.
// ok=false on ANY failure — not a replica, network error, timeout, non-200,
// malformed body — and the caller falls back to the local claim. When ok,
// claimed is the primary's authoritative answer: false means another till
// holds the table.
func claimTableOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, tableID string) (ok, claimed bool) {
	var out struct {
		Data *syncTableClaimResult `json:"data"`
	}
	if !postTableClaimOnPrimary(ctx, d, client, "claim", tableID, &out) || out.Data == nil {
		return false, false
	}
	return true, out.Data.Claimed
}

// releaseTableClaimOnPrimary tries POST /api/sync/tables/release on the
// primary. ok=false on ANY failure, same contract as claimTableOnPrimary;
// callers treat it as fire-and-forget.
func releaseTableClaimOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, tableID string) (ok bool) {
	var out struct {
		Data *syncTableReleaseResult `json:"data"`
	}
	if !postTableClaimOnPrimary(ctx, d, client, "release", tableID, &out) || out.Data == nil {
		return false
	}
	return true
}

// claimTableWriteThrough is THE claim call for the live basket's table pick
// and the resume re-claim: on a replica with a reachable primary, the
// primary's answer is authoritative — a granted claim is ALSO mirrored into
// the local table_claims row (best-effort: a mirror failure is logged, never
// fails the pick — the primary already holds the claim, which is what stops
// the double-booking), a refused one writes nothing. Anything else (not a
// replica, or any failure) is the local claim.
//
// That local claim is ClaimTableForTill with an EMPTY till id — the ” row
// ClaimTable would have written, plus the same TTL reconciliation the sync
// endpoint applies (independent review, 2026-09-07). It matters on the
// PRIMARY, whose table_claims also holds the live claims of REPLICAS: without
// it, a replica that died holding a table would block the primary's own
// basket from that table indefinitely, since nothing on this path ever
// revisits another till's row — the primary would be the one till in the shop
// that the documented ~2-minute takeover did not apply to. The ” id can only
// ever expire ANOTHER till's stale row — ClaimTableForTill's staleness
// disjunct still requires a non-empty till_id, so it can never touch a ”
// row, this till's own included. It CAN, since ut-docs#1704, re-take its
// OWN ” row (the plain own-claim disjunct, unguarded either way) — that's
// the refresh a held order's resume relies on, not an expiry: no OTHER
// till's re-claim attempt can ever touch it, only a call that is itself
// passing tillID=”. On a replica (which only ever holds ” rows locally) and
// on a standalone till (no tills rows at all) it reconciles nothing beyond
// that self-refresh and is otherwise exactly the old ClaimTable.
//
// If that reconciling form fails for any reason it degrades to the plain
// ClaimTable rather than failing the pick: reconciliation is a bonus on this
// branch, but taking the local claim is the offline-first guarantee, and this
// branch is precisely the one a till runs when nothing else is reachable. It
// must not have become easier to fail than the single statement it replaced.
func claimTableWriteThrough(ctx context.Context, d *common.Deps, repo *data.POSRepo, tableID string) (claimed bool, err error) {
	ok, claimed := claimTableOnPrimary(ctx, d, tableClaimProxyClient, tableID)
	if !ok {
		claimed, err := repo.ClaimTableForTill(ctx, tableID, "", time.Now().Add(-tillClaimTTL))
		if err == nil {
			return claimed, nil
		}
		logging.L().Debugf("table claim: local reconciling claim of %s failed (%v) — using the plain local claim", tableID, err)
		return repo.ClaimTable(ctx, tableID)
	}
	if claimed {
		if _, err := repo.ClaimTable(ctx, tableID); err != nil {
			logging.L().Debugf("table claim proxy: local mirror of primary-granted claim %s failed: %v", tableID, err)
		}
	}
	return claimed, nil
}

// releaseTableClaimWriteThrough is THE release call behind pos_api.go's
// releaseTableClaim helper: a "" tableID is the common no-table case and a
// no-op; otherwise the primary is told to release this till's claim there
// (fire-and-forget for the caller's basket-state-change purposes — its
// answer never blocks or fails what the caller is doing, matching every
// existing release site's "never block the basket's state change on
// bookkeeping" stance), and the local row is ALWAYS released afterwards
// regardless, logged-not-surfaced on failure exactly as before.
//
// primaryReleased reports whether the primary-side release is known to have
// succeeded (false covers "not a replica," every network/timeout/non-200/
// malformed-response failure, AND "no primary configured" alike — the
// caller cannot and must not try to distinguish those). Every EXISTING call
// site still ignores it (a bare `releaseTableClaim(...)` statement, ok in
// Go), preserving the fire-and-forget behaviour everywhere it was already
// accepted. It exists for hold_api.go's held/table move handler
// (ut-docs#1704, independent review 2026-09-07): unlike every other release
// site here, a failed release there leaks the OLD table's claim on the
// primary with no natural next touch to surface or self-heal it (a live
// basket's release sites all sit on a basket that keeps moving and
// re-claiming; a moved-away-from held table just sits there, silently
// unbookable, until a manager notices and taps Free table) — so that one
// call site logs loudly on a primary-release failure specifically, instead
// of the usual silence.
func releaseTableClaimWriteThrough(ctx context.Context, d *common.Deps, repo *data.POSRepo, tableID string) (primaryReleased bool) {
	if tableID == "" {
		return false
	}
	primaryReleased = releaseTableClaimOnPrimary(ctx, d, tableClaimProxyClient, tableID)
	if err := repo.ReleaseTableClaim(ctx, tableID); err != nil {
		log.Printf("table claim: release %s failed: %v", tableID, err)
	}
	return primaryReleased
}
