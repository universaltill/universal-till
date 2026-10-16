# Review: cloud-master pull applies item variants and item barcodes (ut-docs#4521)

- **Date:** 2026-10-16
- **Card:** universaltill/ut-docs#4521 (split from #4491, task of epic #4453, ADR-0162 §4).
- **Author:** Opus 5.5, lane `lane:cloud-54`.
- **Reviewer:** Fable 5.1, an independent subagent. It ran one full round, then a scoped re-check of the fixes.

## What shipped

`internal/catalogpull` (contract `reference/contracts/cloud-catalog-feed.md`) now applies the two remaining variant-side kinds:

- **`item_variant`** goes through the new `CatalogRepo.SaveVariantTx`. `SaveVariant` was split into prepare and a tx write, the same way #4463 split `SaveItemTx`.
  - Fields: `item_cloud_id` (required), `name`, `sku` (blank keeps the current one; blank on create generates a `VAR-` code), `price_minor`, `active`, `retired_with_item`, and `option_value_cloud_ids` → `item_variant_options`.
  - `VariantSave` gains two fields, `RetiredWithItem` and `OptionValueIDs`. Both are nil on the directive path, so `save_item_variant` behaves exactly as before.
  - Tombstone: `DeactivateVariantTx`. A variant is never deleted.
- **`item_barcode`** writes one row per barcode through the new `ResolveBarcode` and `PutBarcodeTx`. Tombstones go through `DeleteOwnedBarcodeTx`.
  - A page's codes are resolved before its first chunk transaction begins, because resolving reads the symbology settings through the pool.
  - `barcode_type` is not read: the till resolves the type itself (ADR-0059 §3).
  - The link's local id is the owner plus the stored key.
- **Dependency order** is now: … items → variants → barcodes → the rest.
- **`ApplierVersion` 2 → 3**, so every till re-pulls once.
- `CloudLinkRepo.HeldByOther` was added.
- The "Cloud catalogue pull" section of `docs/data-model.md` is updated.

There is no migration and no UI, and no `web/help` topic covers the pull.

## Findings (round 1)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Pre-resolution decoded `barcode` into a struct (case-insensitive key match, last value wins), while the applier read the exact key. On a mismatch the barcode was missing from the map, which raised an `infraError`, aborted the chunk and re-fetched the same page forever. | Fixed. Both sides now read the code the same way (`decodeFields`), and a miss is refused instead of aborting. Test: `TestResolveBarcodesReadsTheExactKey`, which failed on the pre-fix code. |
| 2 | major | `Replaces` was deleted from both tables by key alone, so a barcode another item now held could vanish without any refusal. | Fixed. The link's local id is now owner-scoped (`item:<id>:<key>` / `variant:<id>:<key>`), and changes, moves and tombstones delete only from that owner (`DeleteOwnedBarcodeTx`). Test: `TestPullBarcodeOwnershipPrimaryAndDuplicates`, which failed before the fix. |
| 3 | minor | `is_primary` never demoted the owner's other primary barcode. | Fixed. `PutBarcodeTx` demotes it. Covered by the same test. |
| 4 | minor | `option_value_cloud_ids` had no size cap and no check that a value belongs to one of the item's option sets. | Fixed in part. The list is capped at `maxOptionSetAxes`, each value must exist on the till, and only one value per set is allowed. The membership check was dropped in round 2 (see R2-2). Test: `TestPullVariantOptionValuesAreOnePerSetAndBounded`; its cap and one-per-set cases fail on the pre-fix code. |
| 5 | minor | `matchBarcode` swallows a failed settings read and falls back to the defaults. In the pull that would record a permanent refusal. | Fixed. `ResolveBarcode` returns the settings error, and a storage error aborts the chunk so it is retried. |
| 6 | minor | Two cloud rows that resolve to the same key on the same owner both linked to it, so a tombstone on one deleted the other row's barcode. | Fixed. A key that another barcode link already names refuses the row, and a tombstone leaves alone a record that another link still names (`HeldByOther`). Covered by `TestPullBarcodeOwnershipPrimaryAndDuplicates`. |
| 7 | nit | A barcode row's audit `created` field was `!linked` rather than whether a row was actually created. | Fixed. `PutBarcodeTx` now reports whether it created the row. |

Accepted residual: a code swap between two barcode rows in one cloud version, or a move to another owner, refuses both rows (both are recorded as `apply_failed`; nothing is lost). This is the same class as #4491's finding 2 and is tracked on ut-docs#4522.

## Re-check (round 2, scoped to the fixes)

The reviewer confirmed that all 7 fixes work as described. It found two minors and one nit, all addressed:

- **R2-1 (minor):** `ResolveBarcode` propagated every settings error, including the non-storage error that comes back with the defaults when a setting value doesn't parse. That turned every barcode on the page into a refusal that sticks until a resync. Fixed: only a storage error is returned; anything else resolves on the defaults, as `save_item` does.
- **R2-2 (minor):** the rule that a value must belong to one of the item's option sets assumes the cloud clears a variant's `option_value_cloud_ids` when a set is detached. No ut-cloud writer enforces that, so every later edit of such a variant would be refused for good. Relaxed: a value of a set the item no longer uses is kept as provenance. Still refused: an unknown value, two values of one set, and more than two values.
- **R2-3 (nit):** the barcode local id assumes ids contain no `:`. Comment added.

## Verified beyond the automated tests

- **TDD:** the catalogpull tests were written first. Against the unchanged applier, 6 of them failed because the rows were skipped. Each review fix's test was run against the pre-fix snapshot (78e6a9f73) in a separate worktree and failed there, then passed after the fix.
- **Directive path unchanged:** the existing `SaveVariant` tests in `internal/data` pass, and the reviewer traced the SQL and the `Changed` list.
- **Gates (local, after the last edit):** 
  - `gofmt -l .` is clean, and `go build ./...` and `go vet` pass.
  - `golangci-lint run ./...` reports 0 issues.
  - CI's main `go test` set passes (everything except the packages CI runs in separate jobs: plugins, pages, enroll/logging/netreach/app/server).
  - `go test -race` passes for `internal/catalogpull` and `internal/data`.
  - Every `bash scripts/ci/*.sh` step in `ci.yml`'s `build` job passes except `guard-shellcheck-version`, which fails only because this container has no shellcheck binary. CI runs it in full.

## Verdict

Safe to merge.
