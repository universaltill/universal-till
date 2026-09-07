# Review: cross-till table occupancy display proxy (ut-docs#1392)

## What shipped

A satellite till's floor-plan tiles (`GET /tables`, `GET /ui/tables/state`)
and the basket's table picker (`GET /ui/pos/table-picker`) now show the
PRIMARY's live table occupancy MERGED with this till's own local
occupancy, when the primary is reachable — falling back to local-only on
any failure. Same replica-proxy shape already shipped for the Orders
board (`order_status.go`'s `fetchOrdersFromPrimary` + `replicaSyncTarget`).

New: `internal/pages/sync_tables.go` — primary-side `GET /api/sync/tables`
(read-only, bearer-authed via `syncTill`, same trust boundary as
`GET /api/sync/orders`). New: `internal/pages/tables_sync_proxy.go` —
replica-side `tablesWithStateForDisplay` + `fetchTablesFromPrimary`.
`internal/auth/middleware.go` gained `/api/sync/tables` on the
session-auth-exempt allowlist (an exact-match `case`, not a prefix) — or
the endpoint 401s before its own bearer check ever runs, the exact
`/api/sync/stock` failure class the file's own comments document.
`tables_page.go`/`table_picker_api.go`'s `ListTablesWithState` call sites
swapped for `tablesWithStateForDisplay`. Strictly read/display-only:
`IsTableFree`/`ClaimTable`/`ReleaseTableClaim` are untouched, still purely
local — actually *preventing* a cross-till double-claim needs write-through
plus an orphaned-claim reconciliation scheme, split out as ut-docs#1703
(see "Scope narrowed" below).

## Scope narrowed from the original card (Architect pass, this cycle)

ut-docs#1392 originally asked for both display AND enforcement (no
double-claim across tills) in one card. Scoping it for real surfaced a
correctness risk in the enforcement half that doesn't fit safely in one
cycle: write-through claim/release needs an owning-till-id + TTL
reconciliation design, or a replica that crashes/loses network mid-claim
leaves an orphaned claim on the primary that nothing ever cleans up
(`POSRepo.ClearAllTableClaims` only runs at a till's own boot and only
clears that till's own local rows). Split into:
- **ut-docs#1392** (this PR) — read-only display proxy.
- **ut-docs#1703** — cross-till claim enforcement (write-through +
  reconciliation), needs its own Architect pass.
- **ut-docs#1704** — held-order (parked) cross-till visibility;
  `held_sales` isn't synced or proxied cross-till at all today.
- **ut-docs#1705** — floor-plan (table position/label) admin-sync; likely
  low-risk (mirrors the already-shipped `registers`/`stock_locations`
  #1590 pattern), independent of the other three.

## Round-1 independent review (Opus, worktree-isolated) — found a real blocker

**Verdict: NOT SAFE TO MERGE as first drafted.**

The first draft's `tablesWithStateForDisplay` returned the primary's
`ListTablesWithState` rows **verbatim** in place of the local ones. Both
`table_claims` and `held_sales` — the two sources `ListTablesWithState`
derives `Occupied` from — are local-only and never reach the primary
(`sync_admin_repo.go`'s own `adminTables` doc comment excludes
`table_claims` by name as "ephemeral... never meant to survive a periodic
snapshot"; `held_sales` isn't synced cross-till at all). So the primary
has never seen and never reports THIS till's own live claim or parked
held order — replacing the local view wholesale made **a replica's own
occupied tables read as free on its own floor plan and picker** the
instant the primary was reachable. Proven with a temporary probe test
(written, run, deleted): both `TestProbe_ReplicaOwnClaimVisibleOnOwnFloorPlan`
and `TestProbe_PickerOffersTableOwnHeldSaleOccupies` failed. Net effect:
a straight downgrade for a satellite cashier working their own tables —
worse than the display gap the card exists to close.

Two MINOR findings alongside it: a factually wrong doc comment (claimed
`tables` isn't in the admin-bundle sync — it is, `sync_admin_repo.go:163`;
only `table_claims`/`held_sales` aren't), and a 3s foreground network call
now sitting on `web/ui/partials/basket.html`'s `hx-trigger="load"` path
(fires on every basket render, not a 15s background poll like the Orders
precedent's own 3s budget was chosen for).

## Fix

`tablesWithStateForDisplay` now: (1) always reads local state first, (2)
on a reachable primary, ORs each table's `Occupied` flag in from the
primary's answer (`Occupied = local.Occupied || primary.Occupied`),
picking the earlier non-empty `OccupiedSince` via a new
`earliestOccupiedSince` helper (same "compare as raw text, exact within
one shape, only approximate across the two" convention
`ListTablesWithState`'s own SQL `MIN()` already accepts), (3) all local
metadata (label, position, seat count, shape, enabled) always comes from
the local row — only `Occupied`/`OccupiedSince` are ever touched by the
primary's data, (4) any failure reaching the primary leaves the local
view untouched, unchanged from before. Also fixed: the doc-comment
inaccuracy, and the proxy client's timeout dropped 3s → 800ms with an
explicit comment naming the hot-path reason. Two new regression tests pin
the fix directly: `TestTablesState_OwnLocalOccupancyPreservedWhenPrimaryReportsFree`
and `TestTablePicker_OwnLocalHeldTableStaysExcludedWhenPrimaryReportsFree`.

## Round-2 independent review (Opus, worktree-isolated, scoped to the fix)

**Verdict: SAFE TO MERGE.** The fix genuinely closes the round-1 blocker
and introduces no new correctness problem.

- **TDD re-verification, independently reproduced**: reverted
  `tablesWithStateForDisplay` to the round-1 replace-not-merge behavior,
  confirmed both new regression tests **FAIL** (the picker test's failure
  output literally shows `class="table-node table-free"` on the locally-held
  table — the double-seating regression, reproduced on demand), confirmed
  the two positive-path tests still pass on the old code (cross-till
  visibility itself wasn't broken, only the merge direction). Restored the
  fix, confirmed all 13 table tests + 25 table/sync tests under `-race`
  pass again.
- Verified metadata preservation, primary-only-ID handling (silently and
  correctly dropped — no local geometry to render), and the extra local
  query's cost (a single indexed local SQLite read vs. up to 800ms of
  network — three orders of magnitude apart, not a real regression) all
  by direct inspection plus hostile-input probes.
- Confirmed `earliestOccupiedSince`'s cross-timestamp-shape comparison is
  byte-identical to what `ListTablesWithState`'s own SQL `MIN()` over the
  mixed-shape UNION already does — not worse than the existing accepted
  convention, and only ever skews a displayed elapsed-minutes label, never
  the `Occupied` flag itself.
- Confirmed no scope creep: the fix commit touches exactly the three files
  it claims to; `ClaimTable`/`ReleaseTableClaim`/`IsTableFree` remain
  untouched across the whole branch; the middleware exemption is an
  exact-match `case`, not a prefix, and `TestSyncPullPathsAreExempt`'s
  negative list still holds.
- Found one **CI-blocking, non-fix-caused** issue: `guard-docs-shots.sh`
  goes red on this branch (not on `main`) because the branch's new
  `internal/pages/*.go` files change the manual-screenshot surface hash —
  bisected to confirm root cause. Fixed in this PR with `make docs-shots`
  (100/100 shots passed; only two unrelated PNGs — `ar/multitill.png`,
  `ar/till-designer.png` — changed from normal rendering non-determinism,
  same class of noise noted in ut-docs#1638's own review).
- Four LOW/INFO items recorded as deferred, not blocking (below).

## Verified beyond automated tests

- Full gate run twice (once per review round): `gofmt -l .` clean,
  `go build ./...`, `go vet ./...`, `go test ./...` full suite green,
  `go test -race` on the touched package clean (0 data races), `golangci-lint
  run ./...` → `0 issues.`, `guard-data-access.sh` / `guard-i18n.sh` /
  `guard-compliance-claims.sh` / `guard-page-http-error.sh` /
  `guard-kiosk-engine.sh` / `guard-help-topics.sh` all ✓, `guard-docs-shots.sh`
  ✓ after `make docs-shots`.
- No new user-facing strings (`guard-i18n.sh` clean, locale key count
  unchanged) — this diff is backend-only, no UI markup or manual content
  changed beyond the regenerated screenshot surface hash.
- No real client/shop name; no secret-shaped literals (test bearer tokens
  are the obviously-synthetic `"b-123"`/`"bearer-t2"` already used
  elsewhere in this test file family).
- File-write/`os.MkdirAll`/`paths.Data` bug classes: N/A, confirmed by
  grep — no file writes, no paths in either new file.

## Deferred / out of scope (not blocking)

- **F2/F3/F4 (test coverage, LOW)**: the positive-path merge test could
  more strongly pin "metadata always comes from local" with a diverging
  primary label; `earliestOccupiedSince` has no direct table-driven unit
  test for the "occupied on both sides" case; no test exercises the
  `table_claims` (live, not held) local-occupancy source specifically.
  Real gaps, cheap to close, not required for this PR's own correctness
  claim (both were independently probed and confirmed correct by the
  round-2 reviewer without a pinning test).
- **F5 (INFO)**: no negative-caching/circuit-breaker on the proxy call — a
  blackholed primary costs up to 800ms on every dine-in basket render
  (bounded by `HasDineInLine()`; takeaway baskets never pay it; checkout
  still completes either way). A short "primary failed &lt;Ns ago → skip"
  cache would remove the repeated stall — worth a follow-up card if this
  proves noticeable in the field, not invented speculatively here.
- Coverage gap already named on the card itself: a replica never sees a
  *second* replica's occupancy directly (only `local ∪ primary`) — inherent
  to the read-only hub-and-spoke shape, honestly documented in
  `sync_tables.go`'s own comment, and the reason ut-docs#1703 exists.

## Verdict

**SAFE TO MERGE.** Two independent Opus review rounds; the first caught a
real data-integrity regression before it shipped, the second confirmed
the fix closes it cleanly with no new correctness issue, and the
CI-blocking screenshot-surface gate is green after `make docs-shots`.
