# Review — held sales: stale primary list must not confirm a later local-only write (ut-docs#3038)

- **Card:** universaltill/ut-docs#3038 (found in the #3034 review, finding 2)
- **Branch:** `fix/3038-reconcile-stale-list`
- **Author:** pipeline, Opus 5.5 (lane:cloud-24). **Reviewer:** independent Fable subagent, fresh context.
- **Docs:** ADR-0093 Amendment A step 3 refined in ut-docs (same cycle).

## What shipped

`HeldSalesRepo.ReconcileWithPrimary` now takes `fetchStartedAt`, the time
the caller started fetching the primary's list (`heldSalesForDisplay`
captures it before the request). The mark-synced step only raises rows
with `primary_synced = 0 AND updated_at < fetchStartedAt`. So a row this
till wrote local-only after the fetch started (a failed resume's give-back
via `UpsertLocalOnly`) is no longer confirmed by a list that predates it.
Before this change, the next successful reconcile then dropped the only
copy of an open order.

The drop branch is unchanged. It only touches rows already at 1, and on a
replica those come only from primary-confirmed writes (mirror / refusal).
Those rows carry the primary's clock, so guarding the drop by timestamp
would compare two clocks and, under skew, only delay ghost drops (F2).

Strict `<` on second-truncated text is intended: a write in the same
second as the fetch start is deferred to the next reconcile. That errs
toward keeping the row, never toward dropping it.

## Tests

- `internal/data/held_sales_reconcile_stale_list_test.go`:
  - The card's interleaving (mirror → fetch start → `UpsertLocalOnly` →
    stale reconcile → empty-list reconcile). The order must stay at 0 and
    survive.
  - The guard still confirms older never-synced rows and never touches
    already-confirmed rows whatever their `updated_at` (this one guards
    against over-restriction, so it is not fail-first).
- `internal/pages/held_sale_reconcile_stale_list_test.go`: the same
  scenario end to end through `heldSalesForDisplay`. The fake primary
  performs the give-back inside its list handler, then serves the stale
  list.
- Existing call sites were updated. `TestHeldSalesRepo_ReconcileWithPrimary`
  now passes a fetch start after its seeds.

**Fail-first**, verified independently by the author and the reviewer:
with only the SQL guard reverted, both scenario tests fail
(`primary_synced` raised to true). With it restored they pass. The
reviewer ran them 25x (data) and 10x (pages): green, race-clean.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `HeldSale.PrimarySynced` doc still said reconcile raises every listed id | **Fixed** |
| 2 | nit | Field comments mention an `Insert` method the repo no longer has | Pre-existing, left as is |
| 3 | residual | A wall clock stepped backwards between fetch start and a local-only write reopens the window for one render | **Accepted**, documented in the function comment (closing it needs a monotonic generation column) |
| 4 | — | Drop branch unguarded: a row mirrored at 1 after fetch start and absent from the stale list is deleted locally, but the primary still holds it, so the next render lists it and resume claims it from the primary | Correct, no loss (pre-existing Amendment A behaviour) |
| 5 | — | Audit: every `primary_synced = 0` row on a replica carries a local-clock `updated_at` (Upsert/UpsertLocalOnly stamp `datetime('now')`; UpsertIfNewer on a replica only lands mirrors at 1; reconcile never runs on a primary/standalone) | Confirmed |

## Verified beyond automated tests

Backend-only change, with no UI surface and no locale keys. No driven UI
run: the reconcile path has no visible change beyond an open order no
longer disappearing, and the handler-level test drives it through the real
reader with a real migrated DB.

## Gate

`gofmt -l .` clean; `go build ./...`; `go test ./... -race`; the reviewer ran
`golangci-lint` on the touched packages (0 issues); the CI guards passed
locally. The one exception is `guard-deadcode-baseline`, which flags
`logging.Stderr`. That function is used only by `cmd/unitill-desktop`,
which the guard skips locally because the GTK headers are missing; CI
analyzes that root.

**Verdict: safe to merge.**
