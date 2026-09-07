package pages

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
// ever expire ANOTHER till's stale row: ClaimTableForTill's `till_id != ”`
// guard keeps it off every local row, including this till's own. On a replica
// (which only ever holds ” rows locally) and on a standalone till (no tills
// rows at all) it reconciles nothing and is exactly the old ClaimTable.
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
// (fire-and-forget — its answer is ignored, matching every existing release
// site's "never block the basket's state change on bookkeeping" stance), and
// the local row is ALWAYS released afterwards regardless, logged-not-surfaced
// on failure exactly as before.
func releaseTableClaimWriteThrough(ctx context.Context, d *common.Deps, repo *data.POSRepo, tableID string) {
	if tableID == "" {
		return
	}
	_ = releaseTableClaimOnPrimary(ctx, d, tableClaimProxyClient, tableID)
	if err := repo.ReleaseTableClaim(ctx, tableID); err != nil {
		log.Printf("table claim: release %s failed: %v", tableID, err)
	}
}

// tableClaimBootReleaseInitialDelay/tableClaimBootReleaseInterval shape the
// background boot-release retry (ut-docs#1712), same constants-shaped
// convention as basePluginRetryInitialDelay/Interval and
// tseRetryInitialDelay/Interval (setup_base_plugins.go / setup_tse.go) —
// `var`, not `const`, purely so a test can shrink them; production code
// never reassigns them. Long enough past boot that a still-starting primary
// isn't mistaken for unreachable, short enough that a till whose primary
// happened to be down at the exact moment it rebooted still clears its
// stale claims within a few minutes of the network coming back, not at the
// next reboot.
var (
	tableClaimBootReleaseInitialDelay = 30 * time.Second
	tableClaimBootReleaseInterval     = 5 * time.Minute
)

// releaseAllTableClaimsOnPrimary tries POST /api/sync/tables/release-all on
// the primary, sending how long ago (in whole milliseconds) THIS till
// booted — not an absolute timestamp — so the primary can compute the
// cutoff entirely on its OWN clock (see StartTableClaimBootRelease's doc
// comment for why an absolute cross-machine timestamp was wrong).
// bootAt must be a time.Now() value that has never had .UTC()/.Round()/
// .Truncate() applied to it — those strip the monotonic reading Go
// attaches to a fresh time.Now(), and time.Since below needs that reading
// to stay immune to a wall-clock step (NTP correcting after boot) on this
// same machine, not just to skew between the two machines. ok=false on ANY
// failure — not a replica, network error, timeout, non-200, malformed body
// — same contract as claimTableOnPrimary/releaseTableClaimOnPrimary.
func releaseAllTableClaimsOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, bootAt time.Time) (ok bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return false
	}
	elapsedMS := strconv.FormatInt(time.Since(bootAt).Milliseconds(), 10)
	form := url.Values{"elapsed_ms": {elapsedMS}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/tables/release-all", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("table claim boot release: primary unreachable (%v) — will retry", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("table claim boot release: primary answered %s — will retry", resp.Status)
		return false
	}
	var out struct {
		Data *syncTableReleaseResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Debugf("table claim boot release: malformed primary response (%v) — will retry", err)
		return false
	}
	if out.Data == nil {
		logging.L().Debugf("table claim boot release: primary response missing data — will retry")
		return false
	}
	return true
}

// tableClaimBootReleaseTick is one pass of the background boot-release
// retry (ut-docs#1712), reporting how long ago THIS till booted freshly on
// EVERY attempt — deliberately re-derived from the SAME bootAt instant each
// time (never recomputed as "now"), so the reported elapsed keeps growing
// across retries exactly as much real time as has actually passed. Not a
// replica at all (no primary configured) → nothing to release, ever —
// reports done so the caller never ticks again for a standalone till. A
// replica reports done only once the primary has actually confirmed the
// release. Unlike basePluginRetryTick/tseProvisionRetryTick, which
// re-derive a persistent pending-state row on every tick and so must keep
// checking forever, this action carries no state of its own: either the
// primary got told, or it didn't, and a successful call only ever needs
// sending once per boot.
func tableClaimBootReleaseTick(ctx context.Context, d *common.Deps, bootAt time.Time) (done bool) {
	_, _, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return true
	}
	return releaseAllTableClaimsOnPrimary(ctx, d, tableClaimProxyClient, bootAt)
}

// StartTableClaimBootRelease launches the background half of the boot-time
// claim release (ut-docs#1712): tells the primary to drop every
// table_claims row this till owns, so a till that rebooted and simply never
// revisits a specific table doesn't leave that table blocked for the full
// tillClaimTTL window — the gap ClaimTableForTill's TTL reconciliation
// cannot close on its own, because a till that is syncing normally again is
// "seen" immediately and the staleness disjunct never fires for it (see
// ReleaseAllTableClaimsForTill's doc comment, internal/data/tables_repo.go).
//
// Shape mirrors StartBasePluginRetry/StartTSEProvisionRetry exactly: a
// wg-joined goroutine, a short initial delay, then a ticker, returning on
// ctx.Done() from EITHER select — including from inside the initial delay,
// so a shutdown never waits on it. Unlike those two, which retry forever
// against a persistent state row, this loop stops the moment one attempt
// succeeds: releasing is a one-shot "clear my pre-boot state," not an
// ongoing reconciliation, so nothing is gained by continuing to tick after
// success — and a standalone till (no primary configured) returns after the
// very first tick and never calls out again.
//
// Wired in internal/pages/init.go alongside the other Start*(bgCtx, dp, wg)
// calls, where common.Deps (and the sync settings replicaSyncTarget reads)
// already exist — deliberately NOT at ClearLocalTableClaims's early call
// site (init.go, before dp is built), which is exactly why that local sweep
// can only ever clear this till's own local rows and never reach the
// primary's copy. Must never block Init's return or app boot (offline-
// first, ADR-0003): fire-and-forget from the caller's perspective, exactly
// like every sibling Start* call already is.
//
// bootAt (self-review, 2026-09-07) is captured HERE, synchronously, the
// instant this function is called from Init — i.e. before Init returns and
// before the HTTP server can accept its first request — and reused
// unchanged across every retry, however long they take. That ordering is
// what guarantees any table this till claims for real, from the moment it
// starts serving, is claimed strictly after this instant.
//
// bootAt is sent to the primary as an ELAPSED DURATION (time.Since(bootAt)
// at send time), never as an absolute timestamp — this is deliberate, not
// a style choice (independent review, 2026-09-07, corrected before merge).
// An earlier version sent an absolute `before := time.Now().UTC()` and
// compared it directly against claimed_at, which is stamped by the
// PRIMARY's own clock (ClaimTableForTill, internal/data/tables_repo.go).
// That compares two DIFFERENT machines' wall clocks as if they were one: if
// this till's clock ran even slightly ahead of the primary's, a table it
// legitimately claimed in the first moments after boot would be stamped
// EARLIER than the (relatively-later) absolute cutoff and get swept by this
// very call — reopening the exact cross-till double-claim ut-docs#1703
// closed, on an offline-first product where an unsynced clock (no NTP
// reachable) is an ordinary, expected state, not an edge case. Sending an
// elapsed duration instead removes the replica's clock from the comparison
// entirely: the primary computes cutoff = time.Now().Add(-elapsed) using
// ONLY its own clock, so cutoff and claimed_at are always readings of the
// same clock. bootAt itself must be a bare time.Now() (never .UTC()'d) so
// Go's monotonic reading survives into time.Since — immune to a wall-clock
// step on THIS machine too (e.g. NTP correcting shortly after boot), not
// just to skew between the two machines.
func StartTableClaimBootRelease(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	bootAt := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(tableClaimBootReleaseInitialDelay):
		case <-ctx.Done():
			return
		}
		if tableClaimBootReleaseTick(ctx, d, bootAt) {
			return
		}
		t := time.NewTicker(tableClaimBootReleaseInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if tableClaimBootReleaseTick(ctx, d, bootAt) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
