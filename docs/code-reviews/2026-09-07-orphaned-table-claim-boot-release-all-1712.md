# Code review — orphaned live-basket table claim, boot-time release-all (ut-docs#1712)

**Date:** 2026-09-07
**Branch:** `fix/1712-orphaned-table-claim-boot-release-all`
**Reviewer:** independent review pass (Opus, worktree-isolated), did not write
the implementation under review.
**Verdict:** **safe to merge** after the one blocking design flaw (and one
CI-blocking gap) found below were fixed in this branch and pinned by tests.

---

## What shipped

Follow-up from ut-docs#1703's own review (finding 7): `ClaimTableForTill`'s
per-table staleness check only ever runs when someone attempts a *new* claim
on that *same* table, so a replica that claims a table for its live
(not-yet-held) basket, crashes before releasing, and then reboots without
ever re-picking that exact table again keeps the orphaned claim on the
primary forever — the till is "seen" again (`tills.last_seen_at`) the moment
it talks to the primary about anything at all, so the staleness disjunct
never fires.

- **`POSRepo.ReleaseAllTableClaimsForTill(ctx, tillID, keepTableIDs)`**
  (`internal/data/tables_repo.go`) — the till-wide "release everything of
  mine, except…" primitive. Guarded against `tillID == ""` (the primary's
  own local-claim convention everywhere else in this file).
- **`POST /api/sync/tables/release-all`** (`internal/pages/sync_tables_claim.go`)
  — primary-side, bearer-authed via `syncTill`, `keep_table_id` a repeatable
  form field. Added to `internal/auth/middleware.go`'s exempt list, pinned
  in `TestSyncPullPathsAreExempt`.
- **`releaseAllTableClaimsOnPrimary`** (`internal/pages/tables_claim_proxy.go`)
  — replica-side proxy helper, same ok/failure contract as the existing
  claim/release helpers. `postTableClaimOnPrimary` refactored to take a
  `url.Values` form directly (was: a single `table_id` string) so this and
  the two existing callers share one poster.
- **`internal/pages/init.go`** — wired into `Init`'s boot sequence: this
  till's held-order table ids are listed once, passed to release-all as the
  keep list, then reused by the existing boot re-claim step.

---

## Findings

### 1. BLOCKER (fixed) — release-all destroyed held orders' primary-side claims

First draft deleted **every** claim the till owned on the primary,
unconditionally, on the premise that "a replica's live (not-yet-held) basket
never survives a restart, so by construction this till holds nothing valid
on the primary at this exact moment." That premise is false for a **held**
order: ut-docs#1704 deliberately keeps a parked order's `table_claims` row
alive through the whole park — it is the only signal that makes the order's
occupancy visible cross-till — and that row *does* survive a restart,
because `held_sales` itself is durable.

Deleting it and relying on the boot re-claim step to restore it over the
network turned a durable, self-healing row into a delete-then-restore
window with no rollback. **Independent review reproduced this with a real
probe**: a primary whose `/api/sync/tables/claim` briefly fails (its own
reboot, a network hiccup) leaves a held order's table genuinely unclaimed on
the primary between the release-all and the failed re-claim — another till
can seat a party there in the meantime, exactly the cross-till double-
booking ut-docs#1703 exists to prevent.

**Fix:** `ReleaseAllTableClaimsForTill` takes a `keepTableIDs` list; `Init`
builds it from this till's own `held_sales` rows *before* calling
release-all, so a held order's claim is never dropped in the first place.
The boot re-claim step downstream is now a refresh/self-heal, not a
load-bearing restore-from-nothing.

Pinned by `TestReleaseAllTableClaimsForTill_KeepListSurvivesHeldOrders`
(repo), `TestSyncTablesClaim_ReleaseAllKeepsHeldOrdersTable` (HTTP handler),
and — the strongest proof — `TestInit_HeldOrderClaimSurvivesReleaseAllEvenWhenBootReclaimFails`
(`internal/pages/init_test.go`), which reproduces the reviewer's own probe:
a real primary whose `/api/sync/tables/claim` is deliberately broken, a
replica with a held order booting against it. I verified this test fails
(`free=true`) with the keep list disabled, and passes with it restored —
see "What I personally verified" below.

### 2. CI-BLOCKING (fixed) — `guard-docs-shots` was red

The diff touches three non-test `internal/pages/**.go` files, inside the
guard's hashed surface. `make docs-shots` regenerated the manual's
screenshots (two images had incidental drift unrelated to this diff — normal
per the guard's own convention) and the manifest; `guard-docs-shots.sh` is
green.

### 3. Non-blocking, applied — inconsistent silent failure

The reviewer noted the original `_ = releaseAllTableClaimsOnPrimary(...)`
discarded its result with no logging, unlike its immediate neighbours
(`ClearLocalTableClaims`, the boot re-claim loop), both of which
`log.Errorf` on failure. Left as `_ =` deliberately in the final version:
`releaseAllTableClaimsOnPrimary` already `Debugf`s its own failure reason
internally (not a replica is the overwhelmingly common case — every
standalone/primary till hits this path at every boot), and the call site
has no meaningful recovery action beyond what's already true (the local
mirror was already cleared regardless). Noted here rather than silently
dropped, per the reviewer's own "non-blocking" framing.

### 4. Accepted as noted, not applied — form field on a poster with no table id

`postTableClaimOnPrimary(..., "release-all", url.Values{}, &out)` when
`keepTableIDs` is empty sends no `table_id` field at all now (the refactor
to a caller-built `url.Values` made this cleaner than the reviewer's
original observation about a stray empty `table_id=`, which no longer
applies post-refactor).

---

## What I personally verified

**Gate (clean):** `gofmt -l .` (no output), `go build ./...`, `go vet ./...`,
full `go test ./...` (every package `ok`, no `FAIL`), `golangci-lint run
./internal/data/... ./internal/pages/... ./internal/auth/...` (0 issues),
`scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`
(all green), `guard-docs-shots.sh` (green after `make docs-shots`).

**TDD claims re-verified myself, revert → fail → restore → pass:**

- `TestInit_ReleasesOrphanedLiveClaimOnPrimaryAtBootEvenWhenTillStaysOnline`
  — commented out the `Init` call site: fails with a real assertion
  (`got free=false`), not a compile error. Restored: passes.
- `TestInit_HeldOrderClaimSurvivesReleaseAllEvenWhenBootReclaimFails` — the
  blocker-1 regression test. Disabled the `keepTableIDs` scoping (forced it
  to `nil`, reproducing the pre-fix unconditional delete): fails with
  `free=true` (the held order's claim was wiped). Restored: passes.
- `TestReleaseAllTableClaimsForTill_DropsOnlyThatTillsClaims` and
  `TestReleaseAllTableClaimsForTill_KeepListSurvivesHeldOrders` — both
  re-verified failing against a neutered repo method, passing against the
  real one.

Also independently confirmed (per the reviewer's own answers, re-checked in
this branch after the keep-list fix): the `tillID == ""` guard is the only
case that must never be wiped (`syncTill` derives `till.ID` solely from the
bearer-hash lookup, never client-supplied); no raw SQL added outside
`internal/data`; `/api/sync/tables/release-all` is on the exempt list and
pinned; no manual/help-topic prose change needed (this makes an
already-documented "~2-minute recovery" promise hold in one more edge case,
it doesn't add or change operator-visible behaviour); no secret-shaped
literal or real shop name in test fixtures.

---

## Safe to merge

Yes. The design now matches the card's own suggested shape (a boot-time
"release everything of mine" call during `internal/pages.Init`) without the
regression the first draft introduced: a held order's cross-till visibility
is never put at risk by this fix, and the true gap (a live basket's orphan
on a table nobody revisits) is closed unconditionally at every boot.
