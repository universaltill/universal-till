package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till held-sale (open order) write-through, replica side (ADR-0093,
// ut-docs#1920): hold_api.go's park / re-park (parkCurrentBasket) and its
// table move (POST /api/pos/held/table, Amendment A F3) call
// heldSaleWriteThrough instead of repo.Insert/Upsert/SetTable directly, and
// its resume (resumeHeldSale) takes the order through heldSaleClaimForResume
// (the primary's atomic claim, ADR-0093 Amendment B) instead of a local
// read-then-delete, so on a REPLICA the PRIMARY's held_sales
// (sync_held_sales.go) is the shop-wide copy while reachable: a parked
// order pushed there is visible to -- and resumable from -- every other
// till reading through the primary, and a primary-applied write is
// mirrored into the local row so this till's own strip / popup / Open
// orders page keep reading it even if the primary drops off afterwards.
// On ANY failure reaching the primary (not a replica, network error,
// timeout, non-200, malformed body) -- and on the primary's predicate
// refusal (a NEWER write for the same id already landed there) -- the
// write proceeds against the LOCAL held_sales row only, silently: the
// pre-this-ADR behaviour, so an offline station keeps working as before
// (offline-first, ADR-0003; same fallback stance as claimTableWriteThrough).
//
// Known, accepted limitation, stated plainly (same class as
// tables_claim_proxy.go's own note): a write taken via the LOCAL fallback
// while the primary was unreachable is not queued or replayed -- the
// primary never learns of it until this till's next successful
// write-through for that id. Bounded to the outage window, not a
// regression.
//
// The write-through is last-writer-wins by updated_at WITH a clean,
// detectable refusal (heldSaleSyncRefused) -- the same depth ADR-0084
// shipped for a voucher balance, not a per-line merge of two tills' edits
// to the same order (explicit ADR-0093 non-goal; the refused till's own
// local copy is kept as-is, and its next write-through for that id is
// stamped by the PRIMARY's own clock -- at or after the value already
// stored there, so it applies; ut-docs#2271).

// heldSaleProxyClient is the replica→primary client for the held-sale
// write-through and the Open orders page's list fetch. Same 800ms budget
// as tableClaimProxyClient, for the same reason: park / re-park / resume
// are taps that re-render the basket in place, and the page render must
// never block on a blackholed primary -- both must degrade to the local
// path in well under a second, never make the till feel frozen.
var heldSaleProxyClient = &http.Client{Timeout: 800 * time.Millisecond}

// heldSaleTimeLayout is the UTC text shape held_sales.created_at /
// updated_at use (SQLite's datetime('now')), so a Go-formatted timestamp
// compares correctly as plain text against a value datetime('now') wrote.
//
// A REPLICA deliberately never formats a wire timestamp with it any more
// (ut-docs#2271 -- that is the clock-skew bug this closed; see
// heldSaleWriteThrough). Its live use is the PRIMARY side:
// sync_held_sales.go's upsert handler stamps a blank incoming updated_at
// with it, which is the same second-resolution UTC text its own
// datetime('now') writes, so the two are directly comparable in the guard.
const heldSaleTimeLayout = "2006-01-02 15:04:05"

// heldSaleSyncOutcome reports how heldSaleWriteThrough landed a write.
type heldSaleSyncOutcome int

const (
	// heldSaleSyncedLocalOnly: not a replica, or ANY failure reaching the
	// primary -- the row was written locally only (the pre-ADR path).
	heldSaleSyncedLocalOnly heldSaleSyncOutcome = iota
	// heldSaleSyncedPrimary: the primary applied the write and the row was
	// mirrored locally.
	heldSaleSyncedPrimary
	// heldSaleSyncRefused: the primary was reached and REFUSED the write --
	// it already holds a newer updated_at for this id. The row was written
	// locally only, same as any other fallback; reported distinctly so a
	// caller can tell "someone else edited this order" from an outage.
	heldSaleSyncRefused
)

// postHeldSaleOnPrimary is the shared bearer-authed JSON POST behind the
// upsert/claim proxies below. ok=false on ANY failure; a 200 with a
// decodable `{"data":{...}}` object is handed back for the caller to read
// its field. JSON rather than tables_claim_proxy.go's form body, since the
// row carries the basket snapshot as a JSON payload string.
func postHeldSaleOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, action string, body any, out any) bool {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return false
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return false
	}
	// ut-docs#2270: bounds only THIS outbound call, when ctx carries the
	// resume/move hot-path marker -- see crossTillHotPathNetCtx's own
	// comment for why this must never be a deadline on ctx itself.
	netCtx, cancel := crossTillHotPathNetCtx(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(netCtx, http.MethodPost, base+"/api/sync/held-sales/"+action, bytes.NewReader(payload))
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
// primary with the full row. ok=false on ANY failure -- not a replica,
// network error, timeout, non-200, malformed body -- and the caller falls
// back to the local write. When ok, applied is the primary's authoritative
// answer: false means a newer write for this id already landed there.
// updatedAt is the value the primary actually stamped/stored (ut-docs#2271:
// the primary is the one clock that decides this, not the caller), so a
// caller mirroring the row locally carries the exact value the primary
// holds rather than re-deriving its own. createdAt (ut-docs#2394) is the
// same idea for the first-park clock read.
func upsertHeldSaleOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, h data.HeldSale) (ok, applied bool, updatedAt, createdAt string) {
	var out struct {
		Data *syncHeldSaleUpsertResult `json:"data"`
	}
	if !postHeldSaleOnPrimary(ctx, d, client, "upsert", heldSaleToSyncRow(h), &out) || out.Data == nil {
		return false, false, "", ""
	}
	return true, out.Data.Applied, out.Data.UpdatedAt, out.Data.CreatedAt
}

// claimHeldSaleOnPrimary tries POST /api/sync/held-sales/claim on the
// primary (ADR-0093 Amendment B, ut-docs#2712). ok=false on ANY failure --
// not a replica, network error, timeout, non-200, malformed body, or a
// "claimed" answer with no row to resume -- same contract as
// upsertHeldSaleOnPrimary, and the caller falls back to its local row. When
// ok: claimed=true hands back the primary's row, now deleted there and
// tombstoned, so this till alone owns it; claimed=false with known=true
// means another till already resolved it (a claim or delete of its own);
// known=false means nothing there contradicts this till's own copy -- the
// primary never learned of the id, or the only tombstone is this till's own.
func claimHeldSaleOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, id string) (ok, claimed, known bool, row data.HeldSale) {
	var out struct {
		Data *syncHeldSaleClaimResult `json:"data"`
	}
	if !postHeldSaleOnPrimary(ctx, d, client, "claim", syncHeldSaleClaimRequest{ID: id}, &out) || out.Data == nil {
		return false, false, false, data.HeldSale{}
	}
	if out.Data.Claimed {
		if out.Data.Row == nil {
			return false, false, false, data.HeldSale{}
		}
		return true, true, true, heldSaleFromSyncRow(*out.Data.Row)
	}
	return true, false, out.Data.Known, data.HeldSale{}
}

// fetchHeldSalesFromPrimary tries GET /api/sync/held-sales on the primary.
// ok=false on ANY failure -- not a replica, network error, timeout,
// non-200, malformed body -- and the caller falls through to the local
// list; the fallback is silent to the operator by design (Debugf only),
// same as fetchTablesFromPrimary.
func fetchHeldSalesFromPrimary(ctx context.Context, d *common.Deps, client *http.Client) ([]data.HeldSale, bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return nil, false
	}
	// ut-docs#2270: bounds only THIS outbound call, when ctx carries the
	// resume/move hot-path marker -- see crossTillHotPathNetCtx's own
	// comment for why this must never be a deadline on ctx itself.
	netCtx, cancel := crossTillHotPathNetCtx(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(netCtx, http.MethodGet, base+"/api/sync/held-sales", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("held sale proxy: primary unreachable on list (%v) — using local list", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("held sale proxy: primary answered %s on list — using local list", resp.Status)
		return nil, false
	}
	var out struct {
		Data []syncHeldSaleRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Debugf("held sale proxy: malformed primary response on list (%v) — using local list", err)
		return nil, false
	}
	rows := make([]data.HeldSale, 0, len(out.Data))
	for _, row := range out.Data {
		rows = append(rows, heldSaleFromSyncRow(row))
	}
	return rows, true
}

// heldSaleWriteThrough is THE park / re-park write for hold_api.go's
// parkCurrentBasket: on a replica with a reachable primary, the full row
// is pushed to the primary's guarded upsert first; an applied write is
// ALSO mirrored into the local held_sales row, carrying the same
// updated_at the primary now holds (best-effort: a mirror failure is
// logged, never fails the park -- the primary already holds the order,
// which is what makes it visible shop-wide). Anything else -- not a
// replica, any failure, or the primary's refusal -- is the plain local
// Upsert (insert-or-update on the id, created_at honoured on insert and
// left alone on update, ut-docs#1918), exactly the pre-ADR behaviour.
//
// h.UpdatedAt is left exactly as the caller passed it (empty, for every
// real caller -- hold_api.go never sets it before calling in). It is
// deliberately NOT stamped here (ut-docs#2271, closing an ADR-0093
// Decision 2 residual): stamping it with THIS till's own clock is what let
// a genuinely newer edit from a slow-clocked till lose the primary's
// updated_at guard to an older edit from a fast-clocked one. The primary
// is the single serialization point for this table already (its guard is
// what makes the write-through safe at all), so it is also the one clock
// that gets to say "now" -- sync_held_sales.go's upsert handler stamps a
// blank incoming value with ITS OWN clock and hands the applied value back
// on the wire, read below via upsertHeldSaleOnPrimary's updatedAt. The
// local-only fallback (repo.Upsert) is unaffected either way: it always
// stamps its own now() regardless of h.UpdatedAt, same as before.
//
// h.CreatedAt gets the identical treatment for the FIRST-park case
// (ut-docs#2394, closing ut-docs#2389's observed flake): on a first park
// it too is left blank by every real caller, and the primary's own clock
// stamps it and hands the value back via upsertHeldSaleOnPrimary's
// createdAt, read below, so the local mirror lands the exact same value
// instead of a second, independent clock read of its own. A re-park's own
// caller-supplied HeldOrigin.CreatedAt is unaffected (non-blank in, echoed
// back unchanged, ut-docs#1918).
func heldSaleWriteThrough(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, h data.HeldSale) (heldSaleSyncOutcome, error) {
	// ADR-0114 §2: open orders (and the tables they hold) changed on the
	// shop's authority; nudge linked tills. A no-op on a replica (no
	// linked tills there).
	defer d.NudgeLink(fleetlink.ScopeHeldSales, fleetlink.ScopeTables)
	ok, applied, primaryUpdatedAt, primaryCreatedAt := upsertHeldSaleOnPrimary(ctx, d, heldSaleProxyClient, h)
	switch {
	case !ok:
		return heldSaleSyncedLocalOnly, repo.Upsert(ctx, h)
	case !applied:
		logging.L().Infof("held sale proxy: primary refused upsert of %s — a newer write from another till already holds it; keeping this till's copy locally only (ADR-0093)", h.ID)
		// A refusal is itself positive proof the primary holds this id (it
		// refused precisely because it has a newer row for it) -- so this
		// counts as a confirmed sighting exactly like a successful mirror,
		// and must mark primary_synced (Amendment A F11): otherwise this
		// row is indistinguishable from a genuinely outage-taken one and
		// survives ReconcileWithPrimary forever, even after the primary
		// later resolves it on another till.
		h.PrimarySynced = true
		return heldSaleSyncRefused, repo.Upsert(ctx, h)
	}
	// The primary is now authoritative for updated_at too (ut-docs#2271):
	// mirror exactly what it applied, not whatever this till's own clock
	// would have said, so the local copy and the primary's genuinely agree.
	//
	// Guarded on non-blank for the mixed-version window of a rollout: a
	// PRE-#2271 primary applies the write but answers without the
	// updated_at field at all, which decodes to "". The write itself is
	// still correct there -- that older primary's UpsertIfNewer COALESCEs
	// a blank to its OWN datetime('now'), so the guard was already being
	// measured against the primary's clock, which is the whole point --
	// only the reported value is missing. Blanking h.UpdatedAt on that
	// answer would discard a value the caller passed in; leaving it lets
	// mirrorHeldSaleFromPrimary fall back exactly as it did before
	// (UpsertIfNewer COALESCEs a blank to local now).
	if primaryUpdatedAt != "" {
		h.UpdatedAt = primaryUpdatedAt
	}
	// ut-docs#2394: same mixed-version guard as updated_at just above,
	// applied to created_at -- mirror exactly what the primary reports it
	// holds for this row (its own clock's stamp on a genuine first park,
	// since hold_api.go never sets h.CreatedAt itself) rather than let
	// this till's local insert re-derive its own, independent clock read
	// a second or two apart from the primary's. Guarded on non-blank for
	// the identical pre-fix-primary rollout window: an older primary's
	// answer decodes this field to "", and h.CreatedAt (already correct
	// for a re-park's HeldOrigin.CreatedAt, or safely left to the local
	// Upsert's own now() for a first park) must not be blanked by that.
	if primaryCreatedAt != "" {
		h.CreatedAt = primaryCreatedAt
	}
	mirrorHeldSaleFromPrimary(ctx, repo, h)
	return heldSaleSyncedPrimary, nil
}

// mirrorHeldSaleFromPrimary lands a row the primary is known to hold --
// one it just applied (heldSaleWriteThrough) or one it just listed
// (heldSaleForResume) -- into the local held_sales table verbatim, its
// updated_at included, so the local copy and the primary's agree, and
// marks it primary_synced (ADR-0093 Amendment A): this till has now
// confirmed the row exists there, which is what lets a later successful
// list drop it as resolved rather than keep it as outage-taken.
// UpsertIfNewer can only refuse here if the local row somehow carries a
// LATER timestamp than the primary's (a clock that stepped backwards
// between two parks) -- then the plain Upsert still lands the mirror,
// since the primary's state is authoritative and the local copy must
// reflect it. Best-effort: a mirror failure is logged, never fails the
// caller -- the primary already holds the order, which is what makes it
// visible shop-wide.
func mirrorHeldSaleFromPrimary(ctx context.Context, repo *data.HeldSalesRepo, h data.HeldSale) {
	h.PrimarySynced = true
	mirrored, err := repo.UpsertIfNewer(ctx, h)
	if err == nil && !mirrored {
		err = repo.Upsert(ctx, h)
	}
	if err != nil {
		logging.L().Debugf("held sale proxy: local mirror of primary row %s failed: %v", h.ID, err)
	}
}

// heldSaleClaimForResume is resumeHeldSale's lookup (ADR-0093 Amendment B,
// ut-docs#2712): it TAKES the order from whichever database is the shop's
// authority for it, before anything is restored, so no two tills can both
// restore the same order -- replacing Amendment A's list-then-restore-then-
// fire-and-forget-delete, which left a window where they could, and which
// could also hand back a stale copy whose push reply had been lost.
//
//   - Not a replica (standalone, or the primary till itself): this till's
//     own held_sales IS the authority -- ClaimAndTombstone locally, the
//     same transaction a replica's /claim runs, so the primary's own resume
//     is atomic against a replica's claim and leaves the same tombstone.
//   - Replica, primary answers claimed: the primary's row is the content
//     (it is authoritative, Amendment A F1) and it is already gone there.
//   - Replica, primary answers known (resolved on ANOTHER till): the local
//     copy is dropped and the resume refused via the existing not-found path.
//   - Replica, primary answers neither (it never learned of the id, or the
//     only tombstone is this till's own earlier claim -- its re-park of
//     that same order fell back to local-only, so this till holds the
//     newest copy): the local row, under Amendment A's rule unchanged -- a
//     primary_synced mirror the primary no longer has was resolved there
//     (F2: drop, refuse; its tombstone may simply have aged out), anything
//     else is a genuine outage-taken order and resumes locally.
//   - Replica, primary unreachable (any transport failure): the local row,
//     unchanged from before -- the accepted bounded-outage limitation, and
//     resume never blocks on a primary that happens to be off (ADR-0003).
//
// claimed reports that the order was taken from the authority (the first
// two cases), so a resume that then cannot go through must hand it back
// (heldSaleGiveBack) rather than lose it; the fallback cases took nothing.
func heldSaleClaimForResume(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, id string) (held data.HeldSale, found, claimed bool) {
	// ADR-0114 §2: open orders (and the tables they hold) changed on the
	// shop's authority; nudge linked tills. A no-op on a replica (no
	// linked tills there).
	defer d.NudgeLink(fleetlink.ScopeHeldSales, fleetlink.ScopeTables)
	if _, _, isReplica := replicaSyncTarget(ctx, d); !isReplica {
		// "" is the primary's own identity on the tombstone: no enrolled
		// replica ever matches it, so every replica's later claim of an
		// order the primary till itself resumed is refused as resolved.
		h, took, _, err := repo.ClaimAndTombstone(ctx, id, "")
		if err != nil || !took {
			return data.HeldSale{}, false, false
		}
		return h, true, true
	}
	ok, took, known, row := claimHeldSaleOnPrimary(ctx, d, heldSaleProxyClient, id)
	if ok && took {
		return row, true, true
	}
	if ok && known {
		logging.L().Infof("held sale proxy: %s was already resolved on another till — dropping this till's copy and refusing the resume (ADR-0093 Amendment B)", id)
		if err := repo.Delete(ctx, id); err != nil {
			logging.L().Debugf("held sale proxy: dropping resolved copy %s failed: %v", id, err)
		}
		return data.HeldSale{}, false, false
	}
	h, found, err := repo.Get(ctx, id)
	if err != nil || !found {
		return data.HeldSale{}, false, false
	}
	if ok && h.PrimarySynced {
		logging.L().Infof("held sale proxy: %s is no longer open on the primary — resolved on another till; dropping this till's mirror and refusing the resume (ADR-0093 Amendment A)", id)
		if err := repo.Delete(ctx, id); err != nil {
			logging.L().Debugf("held sale proxy: dropping resolved mirror %s failed: %v", id, err)
		}
		return data.HeldSale{}, false, false
	}
	return h, true, false
}

// heldSaleGiveBack puts an order heldSaleClaimForResume claimed back where
// it came from, when the resume that claimed it cannot go through (corrupt
// payload, auto-park failure, already the live order): the claim is
// destructive, and before it a failed resume cost nothing. Written through
// like any re-park -- UpdatedAt blanked so the primary's clock stamps it,
// created_at kept so the order's age survives; its tombstone is harmless,
// since a claim only consults one when the row is absent. Best-effort: a
// failure is logged, never surfaced over the resume's own outcome.
func heldSaleGiveBack(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, h data.HeldSale) {
	h.UpdatedAt = ""
	h.PrimarySynced = false
	if _, err := heldSaleWriteThrough(ctx, d, repo, h); err != nil {
		logging.L().Errorf("held sale proxy: giving back claimed order %s after a failed resume: %v", h.ID, err)
	}
}

// heldSaleForResume was resumeHeldSale's lookup until ADR-0093 Amendment B
// moved resume onto heldSaleClaimForResume; it remains (Amendment A, F10)
// the held-table-move handler's, which must stay non-destructive: any caller that needs this till's
// best current answer for "what does this order actually hold right now,
// and is it still open at all" wants this, not repo.Get's local-only row
// -- a caller that mutates the row (a move) and writes the RESULT through
// must start from the primary's own copy, or it silently overwrites
// whatever another till has added since. On a replica with a reachable
// primary it asks the PRIMARY FIRST (ADR-0093 Amendment A, F1)
// -- the same ordering mergeHeldSales already uses for the Open orders
// page, because the primary is authoritative whenever reachable: the
// local row is at best a mirror of what the primary held when THIS till
// last wrote it, and another till may have added to the order since.
// Reading local-first here resumed that stale mirror while the page had
// just shown the newer copy, silently dropping the added item and
// undercharging the sale. Three outcomes:
//
//   - the primary lists the id: its payload is what gets resumed (mirrored
//     locally first, primary_synced, so the row stays consistent even if
//     the resume then fails on a corrupt payload); the caller's own delete
//     write-through removes it from the primary once it is live here.
//   - the primary answers, and does NOT list the id: if the local row is a
//     mirror (primary_synced) the order was resumed / cashed out /
//     abandoned on another till since -- the mirror is deleted and the
//     resume refused exactly like a genuinely unknown id (F2; no new
//     locale string, the existing not-found path). A local row the
//     primary never learned of (primary_synced = 0, parked while it was
//     unreachable) is the legitimate outage-taken order and resumes from
//     local as before -- "not listed" means "never synced" for it, not
//     "resolved".
//   - the primary is unreachable (not a replica, network error, timeout,
//     non-200, malformed body): the local row, unchanged from before --
//     the same accepted bounded-outage limitation as every other
//     write-through in this file. Resume never blocks or fails on a
//     primary that happens to be off (offline-first, ADR-0003).
func heldSaleForResume(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo, id string) (data.HeldSale, bool) {
	// ADR-0114 §2: open orders (and the tables they hold) changed on the
	// shop's authority; nudge linked tills. A no-op on a replica (no
	// linked tills there).
	defer d.NudgeLink(fleetlink.ScopeHeldSales, fleetlink.ScopeTables)
	rows, primaryAnswered := fetchHeldSalesFromPrimary(ctx, d, heldSaleProxyClient)
	if primaryAnswered {
		for _, h := range rows {
			if h.ID == id {
				mirrorHeldSaleFromPrimary(ctx, repo, h)
				return h, true
			}
		}
	}
	held, found, err := repo.Get(ctx, id)
	if err != nil || !found {
		return data.HeldSale{}, false
	}
	if primaryAnswered && held.PrimarySynced {
		logging.L().Infof("held sale proxy: %s is no longer open on the primary — resolved on another till; dropping this till's mirror and refusing the resume (ADR-0093 Amendment A)", id)
		if err := repo.Delete(ctx, id); err != nil {
			logging.L().Debugf("held sale proxy: dropping resolved mirror %s failed: %v", id, err)
		}
		return data.HeldSale{}, false
	}
	return held, true
}

// mergeHeldSales is the Open orders page's merge (ADR-0093 Decision 3):
// the primary's copy wins per id on a collision (it is authoritative by
// construction -- every reachable-primary write landed there first), a
// row that exists only locally (e.g. taken during an outage) is still
// shown, and the result keeps repo.List's oldest-first order so the page
// and the strip never disagree about ordering.
func mergeHeldSales(local, primary []data.HeldSale) []data.HeldSale {
	merged := make([]data.HeldSale, 0, len(local)+len(primary))
	seen := make(map[string]bool, len(primary))
	for _, p := range primary {
		seen[p.ID] = true
		merged = append(merged, p)
	}
	for _, l := range local {
		if !seen[l.ID] {
			merged = append(merged, l)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].CreatedAt < merged[j].CreatedAt })
	return merged
}

// heldSalesForDisplay is the Open orders page's reader: the local list,
// merged with the primary's live list when this till is a replica and the
// primary answers within heldSaleProxyClient's budget. ANY failure reaching
// the primary -- same exhaustive list as fetchHeldSalesFromPrimary -- is
// the local list alone, silently: the page renders either way, never a
// blocking error over a primary that happens to be off (offline-first).
//
// A SUCCESSFUL primary fetch first reconciles the local table against it
// (ADR-0093 Amendment A, F2; HeldSalesRepo.ReconcileWithPrimary): a local
// mirror (primary_synced) the primary no longer lists was resolved on
// another till and is dropped -- otherwise mergeHeldSales, which rightly
// keeps every local-only row to protect a genuinely outage-taken order,
// would keep showing a ghost this till could re-ring after the money
// already moved. A local row the primary never learned of stays, exactly
// as before. Reconciled BEFORE the local read so the merge sees the
// cleaned table; a reconcile failure is logged and the merge proceeds on
// whatever is there -- never a blocking error for the page.
func heldSalesForDisplay(ctx context.Context, d *common.Deps, repo *data.HeldSalesRepo) ([]data.HeldSale, error) {
	primary, ok := fetchHeldSalesFromPrimary(ctx, d, heldSaleProxyClient)
	if ok {
		ids := make([]string, 0, len(primary))
		for _, p := range primary {
			ids = append(ids, p.ID)
		}
		if dropped, err := repo.ReconcileWithPrimary(ctx, ids); err != nil {
			logging.L().Debugf("held sale proxy: reconcile against primary list failed: %v", err)
		} else if dropped > 0 {
			logging.L().Infof("held sale proxy: dropped %d local mirror(s) the primary no longer lists — resolved on another till (ADR-0093 Amendment A)", dropped)
		}
	}
	local, err := repo.List(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return local, nil
	}
	return mergeHeldSales(local, primary), nil
}
