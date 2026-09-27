# Review: held-sale give-back keeps a stale primary_synced (ut-docs#2723)

- **Branch:** `fix/2723-held-sale-giveback-unsynced`
- **Card:** universaltill/ut-docs#2723 (`complexity:easy`) — the accepted
  residual from #2712's review (Finding 4 in
  `2026-09-25-held-sale-claim-tombstone-2712.md`).
- **Author:** Sonnet (Dev). **Reviewer:** Opus 5.5 (independent, fresh
  context, different model).

## The bug

`heldSaleGiveBack` hands an order back after a resume fails post-claim. When
its write-through to the primary also failed, the fallback `repo.Upsert`
wrote `primary_synced = MAX(old, new)`, so a replica row that was already a
confirmed mirror (`primary_synced = 1`) stayed at 1 even though the primary
no longer held the id (it had just been claimed). The next successful
`ReconcileWithPrimary` deleted it as "resolved elsewhere", losing the only
copy of an open order. The card's option (a) was implemented.

## What shipped

- `HeldSalesRepo.MarkLocalOnly(id)` (`internal/data/held_sales_repo.go`):
  `UPDATE held_sales SET primary_synced = 0`. It is the one write allowed to
  lower the flag. `Upsert`'s MAX semantics are unchanged.
- `heldSaleGiveBack` (`internal/pages/held_sale_sync_proxy.go`) calls it
  when the write-through outcome is `heldSaleSyncedLocalOnly`, including
  when the local fallback write itself errored (review finding 3).
- Tests:
  - `TestHeldSalesRepo_MarkLocalOnly_SurvivesReconcileWithPrimary` (data).
  - `TestResumeOnReplica_GiveBackAfterUpsertFailureDoesNotStickPrimarySynced`
    (the card's AC). The primary claims, the payload is corrupt, and
    `/upsert` returns 500. The local row must end unsynced and survive
    reconcile.
  - `TestResumeOnReplica_GiveBackAppliedOnPrimaryStaysConfirmedMirror`
    (review finding 5). When the primary applies the give-back, the local
    copy stays a confirmed mirror.
- `web/help/img/manifest.json`: surface hash refreshed. No rendered pixel
  changed (Go handler logic only), so the commit has a
  `Docs-Shots-Unchanged: true` trailer.

## TDD re-verification (orchestrator, by hand)

- **Card-AC test:** with `held_sale_sync_proxy.go` reverted to `main` it
  fails with `PrimarySynced:true` still set on the given-back row. It passes
  with the fix.
- **Applied-path test:** mutated the condition to `if true || …`. The test
  fails (`PrimarySynced:false`), then the code was restored.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | A lost reply on the give-back's upsert (the primary applied it, but the reply timed out) leaves this till's copy demoted, so it is not dropped when another till resolves the order. | Accepted. This is the same bounded-outage class as a first park's lost reply. A resume here still claims on the primary first, and the tombstone refuses it. Losing an order is worse than a stale copy the tombstone guards. |
| 2 | Low | `Upsert` and `MarkLocalOnly` are two statements, so a concurrent reconcile could run in between. | Deferred to a follow-up card (a single atomic local-only upsert). The window is microseconds and needs a primary that fails `/upsert` but serves the list, and an equal-or-wider window already exists across the claim. |
| 3 | Low | An early return on a local write error skipped the demotion. | Fixed. |
| 4 | Low | Same MAX-stickiness on the successful-resume path: the local `Delete` fails, then a re-park falls back to local-only. | Deferred to the same follow-up card. |
| 5 | Info | No test covered the "applied" direction. | Fixed (new test, mutation-checked). |

Checked and fine: raw SQL only in `internal/data`; no user-facing strings,
money or file writes; offline-first unchanged (the demotion is best-effort
and logged, never surfaced).

## Gate

- `gofmt -l .` printed nothing.
- `go build ./...`, `go vet ./...` and `golangci-lint` (0 issues) are clean.
- `go test ./... -race` is green.
- Every `ci.yml` guard passes except three that are environment-only:
  - `guard-shellcheck-version`: no shellcheck binary in the container.
  - `guard-deadcode-baseline`: flags `internal/logging` symbols this diff
    does not touch, because the desktop root was skipped for missing GTK
    headers. CI analyses all roots.
  - `guard-docs-shots`: passes after the surface-hash refresh.
- No UI surface was touched, so there was no UX phase and no screenshots.

**Verdict:** safe to merge.
