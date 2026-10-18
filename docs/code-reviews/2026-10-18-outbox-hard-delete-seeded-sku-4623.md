# Code review — cloud outbox: local hard delete of a category / tax code, seeded blank-SKU base (ut-docs#4623)

**Date:** 2026-10-18 · **Author model:** Opus 5.5 · **Reviewer:** Sonnet (independent). Fable was routed and refused (out of usage credits), so the review fell back to Sonnet per MODEL-ROUTING.

## What shipped
Two deferred LOW findings from the ut-docs#4464 review (`2026-10-17-cloud-outbox-4464.md`: L-2 and delta D-1), both in `internal/catalogpull`:

- **L-2: a local hard delete of a linked category or tax code was dropped.** The demo-data removal (`remove_demo*.sql`) is the only path that does this. The cloud kept the row live, and a resync brought it back.
  - The contract already answers it: `delete` is item-only, because "a till deactivates a category or tax code: that is an `update` of `active`". So `sealMark` now uploads `update` with `active: true → false` against the base, and the contract is unchanged.
  - If the base is already inactive (or has no `active`), nothing is sent and the mark is dropped.
  - The accepted answer re-applies the cloud's row as an inactive local row. That is the till's own soft-delete state.
- **D-1: false conflict on a seeded blank-SKU base.**
  - `seedBases` flags every seeded item base with a hidden `sku_seeded`, as it does `sku_kept`; neither is ever uploaded. The seeded `sku` may be one the till generated for a blank cloud SKU.
  - A change touching `sku` waits, with its mark left open, while the flag is set.
  - `apply()` reads the cloud row's own `sku` before the field-wise apply drops a pending edit. It then settles the base with `settleSeededSKU`: a blank cloud SKU becomes base `""` with the seeded SKU as `sku_kept`; any other cloud SKU just clears the flag.
  - The pending SKU edit then uploads with the cloud's value as `before`.
- `docs/data-model.md` (cloud_outbox section) and the `push.go` package doc are updated.

## Findings (Sonnet)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| M-1 | MEDIUM | D-1 was fixed only for updates. An item deleted before the re-pull still sent the seeded SKU in the delete's `before`, which gave a false `delete` conflict and kept the item. It cannot wait like an update: the pull skips a marked row the till deleted (`skip`), so the base would never settle. | **Fixed test-first:** the delete leaves an unsettled seeded `sku` out of `before`. ut-cloud compares only the fields it is sent (`evaluate.go`, `pick(cloud, keys(c.Before))`), and `change.go` accepts any subset of the kind's fields. Test: `TestPushDeleteOnASeededBaseOmitsTheSeededSKU`, which failed first with `Conflicts:1`. |
| L-1 | LOW | The accepted deactivation re-creates the deleted category or tax code locally as an inactive row. If the operator has since made a tax code of the same name, that re-create hits UNIQUE(name) and is recorded as an apply refusal. | **Accepted:** the inactive row is the till's own delete semantics (`delete_category` and tax-code deactivation are soft). The name clash needs a demo tax-code name re-used by hand in the same upgrade window, and it is a recorded refusal, not a crash. |
| L-2 | LOW | A mark waiting on `sku_seeded` waits as long as the item's re-pull row never applies (refused or never returned), and other fields saved in the same mark wait with it. | **Accepted:** bounded by the forced applier-4 re-pull. A refused row is retried by later pulls and settles when it applies. |
| N-1 | NIT | The deactivation's `before` read `base["active"]` unchecked. | **Fixed:** a base without `active` drops the mark (no applied row leaves one). |

## Verification
- **TDD:**
  - Every new test was run before its fix and failed with the real defect: the hard deletes were `Dropped:2` / `Dropped:1`, the seeded SKU edit gave `Conflicts:1` (the false conflict itself), and the seeded delete gave `Conflicts:1`.
  - After each fix, all of them pass.
- **Tests** (`internal/catalogpull/push_4623_test.go`):
  - deactivation of a category and of a tax code, with both rows staying inactive across later pulls and ticks;
  - an already-inactive base;
  - a cloud that deactivated the row since the till last pulled;
  - a SKU edit on a seeded base, with blank and non-blank cloud SKUs: it waits, the re-pull keeps the edit, and it then uploads with the correct `before` and no conflict;
  - a non-SKU edit on a seeded base still uploads at once;
  - a delete on a seeded base.
- **Gate:**
  - `go build ./...`, `go vet` and `golangci-lint` on `internal/catalogpull` are clean.
  - `go test ./internal/catalogpull/` passes, and the full `go test ./...` result is recorded on the PR.
  - These guards pass: data-access, i18n, core-neutral, card-data-schema, mail-addresses, migration-version-collision, compliance-claims.
  - `guard-deadcode-baseline` could not run locally: the container's `deadcode` binary was built with go1.26 and the module needs go1.27. CI runs it.
- **Not checked:** no UI surface is touched, so there was no driven run or screenshots; this is backend sync logic covered against the fake cloud.

## Verdict
Safe to merge.
