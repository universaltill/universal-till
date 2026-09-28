# Review — every new catalog item gets a SKU (ut-docs#3087)

- **Date:** 2026-09-28
- **Branch:** `fix/3087-generated-item-skus`
- **Author:** lane:cloud-54 build cycle (Opus 5.5); **reviewer:** independent Fable subagent (different model)
- **Card scope this cycle:** AC 2, 4, 5, 7 and the commit half of AC 1. Split to follow-ups: backfill + till "Generate missing SKUs" (ut-docs#3097), import preview shows/edits generated SKUs (ut-docs#3098), my. action (ut-docs#3099).

## What shipped

- `internal/data/item_sku.go`: `nextItemSKU`. If the category's items already use plain numeric SKUs (at most 7 digits), the new SKU is the highest + 1 at the same width (30096 → 30097). Otherwise it is `PREFIX-NNNN`: the first 3 ASCII letters/digits of the category name, with accents stripped, or `ITEM` when there is no category or the name has no Latin letters. The generator skips any candidate already in use as an item SKU, a variant SKU or a barcode. It never uses a UUID (#1176). Longer digit strings (GTIN length) never start a sequence, so +1 cannot invent another product's barcode.
- `CreateItem` and `CreateItemTx` both go through `insertItemRow`, the only production insert into `items`. It fills a blank or whitespace SKU, and on a clash on `items.sku` it generates again (up to 5 attempts). An explicit SKU that is already taken still returns `ErrSKUExists`. This covers the import commit, the cloud/my. push, the catalog form, the API and `SaveItem`, which dropped its `ITEM-<hex>` generator and now passes its category along.
- Guard test `TestItemInsertPathsAreCovered`: production code in `internal/data` has exactly one literal items INSERT, and the demo seed rows carry non-blank SKUs.
- Help: step 3 of `catalog.md` explains generated SKUs, in en/de/tr/ar/fa. The document structure is unchanged, so the drift guard stays green.
- e2e: `sell-null-sku-tile-3072` now expects the generated SKU (`NUL-0001`) as the tile code. The legacy NULL-SKU resolve path keeps its Go test (`pos_repo_resolve_test.go`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `SaveItem` create passed the category into the INSERT before validating it. An unknown id returned a raw `FOREIGN KEY constraint failed` instead of the contract's "category … does not exist on this till" message. | **Fixed.** The category is now validated before the INSERT. Regression test `TestSaveItem_CreateUnknownCategoryIsRefusedCleanly` failed before the fix and passes after it. |
| 2 | nit | `insertItemRow` retried on any UNIQUE violation, so a duplicate id was reported as "SKU in use". | **Fixed.** It retries and maps to `ErrSKUExists` only for `items.sku`; other violations return the wrapped error. |
| 4 | should-fix (found by the full e2e run) | The item pickers on `/modifiers` and `/inventory` resolve only the datalist label `name (SKU)`. Before this change, typing a bare name worked only for SKU-less items. Now that every new item has a SKU, a typed name no longer resolved (`osk-decimal-sale-catalog-fields-1284` failed; it passes on `main`). | **Fixed.** A bare name also resolves when exactly one item has that name; a shared name still needs the full label. The spec and 51 related picker/inventory specs pass. |
| 3 | nit | The guard sees only literal INSERT text. The generic sync replica upsert builds its SQL at runtime. | **Fixed.** A comment on the guard now says this. That path copies the primary's SKU unchanged. |

The reviewer also checked these and found them correct:
- SQLite `substr`/GLOB semantics and their case sensitivity.
- Overflow bounds.
- The transaction stays usable after a statement-level UNIQUE abort.
- Concurrency (8×10 concurrent blank-SKU creates, 3 runs, all distinct).
- #1839 re-import dedupe is unaffected.
- `item:<id>` tile codes are still produced only for legacy NULL rows.
- The translations match the English.

## Verified beyond unit tests

- TDD re-verified by the reviewer: with the production change reverted, 12 new or updated tests fail for the claimed reason (NULL sku, old hex shape, INSERT count). Restoring the change makes them pass.
- Real driven run (Playwright, real till binary): a CSV row with no SKU in category "NullSku3072 Cat …" was imported as `NUL-0001`. Its sell tile carries that code and rings up one basket line with no stale toast.
- Full gate: `gofmt`, `go build`, `go vet`, `go test ./...` and `golangci-lint` (0 issues) all pass, as do the data-access, i18n, help-drift, help-topics, core-neutral, compliance, competitor-naming and kiosk-engine guards. `guard-deadcode-baseline` fails identically on `main` in this container, because it has no GTK headers. Full default e2e project: 753 passed, 2 failed. `money-comma-2925` timed out under load and passes on rerun. `osk-…-1284` was a real regression (finding 4), now fixed and green.
- No locale key changed, so there are no language-pack follow-ups. The manual screenshots were regenerated with `make docs-shots` (the catalog topic text changed); `guard-docs-shots` now passes.

## Verdict

Safe to merge.

## Deferred

- ut-docs#3097, #3098, #3099 (see the scope line above). The import's inventory-connector `StockAdjusted` event still carries the source row's SKU, which is blank for a generated one. This is noted on #3098.
