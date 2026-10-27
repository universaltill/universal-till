# Review: `set_category_item_order` directive + snapshot `tile_pos` (ut-docs#3105, slice A)

- **Date**: 2026-10-27
- **Card**: universaltill/ut-docs#3105 (slice A; slices B #5103 ut-cloud, C #5104 ut-my-shop)
- **Contract**: ut-docs `reference/manage-shop-catalog-api.md` §1.2, §2.2, §2.7, §3, §3.7, §3.14, §4.4 (`0.35.1`)
- **Built by**: Sonnet (Dev subagent). **Reviewed by**: Opus 5.5 (independent subagent, fresh context).

## What shipped

- `internal/cloudsync`: `decodeCategoryItemOrder`; `set_category_item_order` registered as a
  main-till-only catalog type (an applied one re-pushes the snapshot in the same tick); new
  `Hooks.SetCategoryItemOrder` and `Hooks.ItemTilePositions`; `snapshotItemRow.TilePos`
  (`tile_pos`, omitted when the item has no tile or the positions can't be read).
- `internal/ui`: `redealIntoSlots` factored out of `UpdateOrderSubset` (behaviour unchanged);
  `ButtonStore.UpdateCategoryOrder` re-deals one category's tiles (explicit + implicit) into the
  slots they already hold, no write and no audit when the grid already has the order;
  `ButtonStore.TilePositions` (Load()'s order without price/modifier/photo lookups).
- `internal/data`: `CatalogRepo.ItemCategoryIDs` (batched, chunked at 500 ids).
- `internal/pages`: `cloudSetCategoryItemOrder` (primary-only, category/item/category-membership
  checks before any write, audit `cloud_category_item_order_set` on a real change) and the hook wiring.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Two tiles sharing a code (duplicate SKUs are allowed) made the re-deal move another category's tile, and replays were never a no-op. | **Fixed**: `ErrSharedTileCode` refusal before anything is written; `TestUpdateCategoryOrder_SharedTileCodeRefused` (seen failing with `changed=true err=<nil>`, then passing). Contract §3.14 lists the text. |
| 2a | minor | §3 says each apply is one `BEGIN IMMEDIATE` transaction; here the checks and grid read run before the write. | **Fixed in the doc**: §3.14 states the exception and that the window is last-write-wins, the same as the till's own jiggle reorder route. |
| 2b | minor | Hook comment said "the cloud keeps what it had" on a positions error; §3.7 said absent = unknown. | **Fixed**: §3.7 now says a snapshot with no `tile_pos` at all leaves the cloud's stored values as they were (as for photos/locations); comment aligned. Slice B #5103 told. |
| 3 | minor | A first reorder materialises implicit tiles up to the last moved slot, other categories' included. | **Accepted**: identical to jiggle / All-grid reorder; documented in §3.14. |
| 4 | nit | `IdempotentWritesNothing`'s checksum can't see a same-order rewrite. | **Accepted**: the no-audit assertion and `ImplicitReplayDoesNotMaterialise` cover the replay path; noted. |
| 5 | nit | No till-side cap on id count/length. | **Accepted**: the cloud caps at 1000 (§2.7); `ItemCategoryIDs` chunks under SQLite's variable limit. |
| 6 | nit | §4.4 map spacing. | Accepted: matches the neighbouring `apply_item_option_sets` line. |

## TDD re-verification (reviewer, by mutation in a throwaway copy)

- Re-deal skipped → `MixedExplicitAndImplicitKeepsOtherSlots` failed ("other category moved").
- `same` shortcut dropped → `IdempotentWritesNothing` and `ImplicitReplayDoesNotMaterialise` failed.
- Item-category check / category check / `requirePrimaryDirective` removed → `CloudSetCategoryItemOrder_Refusals` failed each time.
- `tile_pos` always set → `TestSnapshotItemTilePos` failed.

## Gates

`gofmt -l .` empty; `go build ./...`; `go vet ./internal/...`; `golangci-lint run ./...` 0 issues;
full `go test ./...` green (`internal/plugins` needed a longer timeout on the loaded container,
1012s, untouched code); CI-job guards green except two container-only misses
(`guard-deadcode-baseline.sh`: local deadcode binary built with an older Go;
`guard-shellcheck-version.sh`: no shellcheck installed) — CI runs both. `-race` passes for
`internal/ui` and for the new tests run alone; the full `cloudsync`/`pages` race run timed out
under container load — left to CI.

## Not verified

No UI surface changed (backend directive + snapshot field), so no driven/visual run; the
end-to-end my. → cloud → till path needs slices B and C.

**Verdict**: safe to merge.
