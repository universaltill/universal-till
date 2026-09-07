# Code review: held-order (parked) cross-till table occupancy (ut-docs#1704)

**Date:** 2026-09-07
**Card:** ut-docs#1704 — split from ut-docs#1392
**Repo/branch:** `universal-till`, `fix/1704-held-order-cross-till-occupancy`
**Dev:** Fable (subagent) · **Review:** Opus (subagent, worktree-isolated, `complexity:hard` routing)

## What shipped

`table_claims` is the only occupancy signal that syncs cross-till today (the
already-shipped ut-docs#1703 write-through: `claimTableWriteThrough` /
`releaseTableClaim(WriteThrough)`, proxying to `POST /api/sync/tables/claim|
release` on the primary, merged for display by `tablesWithStateForDisplay`).
`held_sales` (parked orders) deliberately stays unsynced — full replication
was considered and rejected as out of scope for this card, same non-goal the
original #1392 split-out named.

Previously, `POST /api/pos/hold` released the live-basket's table claim the
moment an order was parked, assuming `held_sales.table_id` "took over" the
occupancy locally. Since `held_sales` never syncs, a parked order's table
read **free** to every other till (and to the primary itself, for a
replica's held order) for the entire time it sat parked.

**Fix:** keep the existing `table_claims` row alive through the held/parked
lifecycle stage instead of releasing it at hold-time, and move it when a
held order's table changes. Reuses 100% of the already-tested #1703
write-through/TTL-reconciliation plumbing — no new DB table, no migration,
no new sync endpoint, no i18n keys.

### Changed files

- `internal/pages/hold_api.go` — `POST /api/pos/hold` no longer releases the
  claim; `POST /api/pos/held/table` (move a parked order) gained a
  same-table/both-empty fast no-op path (avoids self-blocking against the
  order's own persisted claim) and now claims the new table *before*
  committing the move, releasing the old table's claim only after the move
  actually commits. `POST /api/pos/resume` — unchanged, verified correct
  (the existing re-claim-if-different logic already degenerates to an
  idempotent no-op once the claim survives the hold).
- `internal/pages/tables_claim_proxy_test.go`,
  `internal/pages/hold_api_test.go`, `internal/data/tables_repo_test.go` —
  new/rewritten regression tests (below).
- `internal/data/tables_repo.go` — `ClearLocalTableClaims`'s boot sweep now
  excludes a claim still backing a genuine `held_sales` row (see Finding 2).
- `internal/pages/init.go`, `internal/pages/pos_api.go`,
  `internal/data/tables_repo.go` — doc-comment accuracy fixes for the three
  places whose prose described the old "held_sales takes over, claim
  released" design (Finding 3 / nits).

## Independent review — findings

Review ran in an isolated worktree (ut-docs#386 discipline), did a
revert-then-restore TDD verification on the diff as Dev delivered it, then
found two real gaps in Dev's version by tracing the new invariant ("a claim
now outlives the live basket") into code the diff didn't originally touch:
the move handler's own commit ordering, the boot sweep, and the go-live
reset. All three were then fixed and re-verified in this same pass.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Blocker** (fixed) | Move handler committed `SetTable` and released the OLD claim *before* confirming the NEW table's claim succeeded. `IsTableFree` is local-only and can't see a table another till holds via the primary; on a claim refusal the order ended up moved with **no claim anywhere** — the #1704 bug reintroduced, plus a live double-booking risk. | Reordered: claim the new table first (write-through, cross-till-authoritative), reject the move outright on failure (same shape as the existing "occupied" rejection), and only release the old table's claim once the move has actually committed. Mirrors `pos_api.go`'s own live-basket table-pick precedent. New regression test: `TestHeldTableHandler_MoveRejectedByPrimaryLeavesOrderOnOldTable` (replica + stub primary that refuses the claim). |
| 2 | **Should-fix** (fixed) | The boot sweep (`ClearLocalTableClaims`, called once at process start) deleted every `till_id=''` claim unconditionally — which, since this diff, now includes a claim backing a **surviving** held order. Every till restart (nightly power-off, an update) silently reproduced the original bug: every parked order's table read free shop-wide again until next moved/resumed. | `ClearLocalTableClaims`'s `DELETE` now excludes any table with a `held_sales` row still parked on it — only a claim with *nothing* held on its table is the "live basket, unclean shutdown" case the sweep exists to clean up. New regression test: `TestClearLocalTableClaims_PreservesHeldOrderClaim`. `init.go`'s boot-sweep comment updated to match. |
| 3 | Should-fix (**deferred, documented, follow-up filed**) | The shop go-live reset (`resetArchiveTables`) archives and deletes `held_sales` but does not clear `table_claims` — pre-diff a parked order held no claim, so a reset left nothing behind; now it can leave a stale claim. Recovery exists (a till restart's sweep — once that table has no `held_sales` row again, S2's fix reclassifies it as sweepable; or a manager's "Free table"), and a shop reset is rare and manager-gated, so accept-and-document rather than widen this card's scope further. | Filed as ut-docs#1726 (Backlog, `p3`). Noted here rather than silently left for the next person to rediscover. |
| — | nit | The held-strip's move-target list (`hold_api.go`'s `renderHeldStrip`) reads local-only `ListTablesWithState`, unlike the table picker / floor plan which use the cross-till-merged `tablesWithStateForDisplay` — so it can still *offer* a target the primary already knows is taken. Finding 1's handler-side reject-on-claim-failure fix means this can no longer corrupt state (the user just gets bounced with the existing rejection UX), so it's a UX/discoverability gap, not a correctness one. | Filed as part of the same follow-up, ut-docs#1726. |
| — | nit | Three doc comments (`pos_api.go`'s `releaseTableClaim`, `tables_repo.go`'s `ReleaseTableClaim` and `IsTableFree`) described the old design. | Fixed in this PR. |
| — | nit | The accepted `claimed=false` log line on `POST /api/pos/resume` for a standalone/primary till re-claiming its own already-held row (an `INSERT OR IGNORE` "already exists" outcome, not a real conflict) — confirmed genuinely harmless: nothing user-facing reads it, and on a replica-with-reachable-primary the same call correctly returns `claimed=true`. | Left as-is; not worth widening this diff for a log-line-only nit. |

## Verification beyond automated tests

- **Revert-then-restore TDD**, done twice independently (once by the
  reviewer on Dev's original diff, once here after the two fixes above):
  reverting the hold-release removal fails
  `TestHoldThenResume_MovesTableClaimBetweenLiveAndHeld` with the exact
  expected message; reverting the same-table fast path fails
  `TestHeldTableHandler_MoveOntoOwnCurrentTableSucceedsWithLiveClaim` and
  nothing else (proving it's the only test pinning that regression);
  reverting the claim-before-commit ordering (Finding 1) is what the new
  `TestHeldTableHandler_MoveRejectedByPrimaryLeavesOrderOnOldTable` pins;
  reverting the boot-sweep exclusion (Finding 2) is what
  `TestClearLocalTableClaims_PreservesHeldOrderClaim` pins. Restoring each
  fix returns the suite to green.
- `gofmt -l` (touched files) — empty. `go build ./...` — clean.
  `go vet ./...` — clean. `golangci-lint run ./internal/pages/...
  ./internal/data/...` — 0 issues.
- `go test ./...` (full repo, `-count=1`, fresh cache) — all green. One
  transient flake was observed in `internal/pages`'s large async-goroutine
  suite on a single run; reproduced clean on two subsequent fresh runs and
  is unrelated to any file this diff touches (async receipt/kitchen-print
  and voucher-tender fixtures elsewhere in the package) — not a false
  green, a known flake category this package already carries.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-kiosk-engine.sh` — all pass.
- No SQL added outside `internal/data` (confirmed — every new/changed call
  goes through existing repo methods; the one new SQL statement, in
  `ClearLocalTableClaims`, is already in `internal/data`). No migration. No
  i18n keys — no user-facing string changed. No money/tax logic touched.
  No real client/shop name used anywhere in the diff or its tests.

## Visual check

Not applicable — this diff changes no visible surface. The floor plan and
held-orders strip already render occupancy from `ListTablesWithState`; this
fix only corrects what that query (and its cross-till proxy) sees. No
screenshot taken; none needed.

## Manual / help topic

No update owed. `web/help/en/tables.md` already documents the intended,
now-actually-correct behaviour verbatim — "It stays occupied while the
order is held and after it's resumed" and "a table is reserved across
**all** of them" — this change makes the product match existing
documentation rather than the reverse. (Its adjacent line, "…frees
automatically when … the held order is deleted," is in mild tension with
deferred Finding 3 above; left as-is since #1726 will need to revisit that
sentence together with the reset-path fix, not in isolation here.)

## Verdict

**Safe to merge.** No blockers remain. One should-fix item (Finding 3) is
deliberately deferred with a filed follow-up and an explicit note here,
per this pipeline's standing "split, don't silently widen" convention —
the same pattern the original #1392→#1703/#1704/floor-plan split already
established for this exact feature area.
