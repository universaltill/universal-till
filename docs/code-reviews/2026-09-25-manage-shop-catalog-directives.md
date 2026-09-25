# Code review: manage-shop catalog directives (PR #1348)

**Date:** 2026-09-25
**Card:** universaltill/ut-docs#2584
**Contract:** ut-docs `reference/manage-shop-catalog-api.md` §3
**Branch:** `feat/manage-shop-catalog-directives` (draft PR universal-till#1348)
**Reviewer:** independent fresh-context review of the branch diff; findings fixed TDD-first by the Dev role

## Scope

The five main-till-only directive types (`save_item`, `save_category`,
`delete_category`, `save_modifier_group`, `delete_modifier_group`), their
repository write paths, migration 041 (`categories.icon`,
`categories.sell_screen_hidden`), the sale screen honouring
`show_on_sale_screen`, the local category editor's toggle and badge, and
catalog snapshot schema 2.

## Findings and fixes

Each fix started with a failing test, seen failing before the change.

1. **Replaying a create with `sku:""` failed** (`catalog_save_repo.go`).
   Once the item existed, a re-served create carrying a blank SKU hit "the
   sku must not be blank", so a create whose result post was lost could
   never resolve. Contract §3.1: blank SKU on create = auto-generate, and a
   re-served create applies as an update. **Fixed:** on `Create` with a
   blank SKU and an existing row, the SKU reads as absent (the generated one
   is kept). Test: `TestSaveItem_ReplayCreateBlankSKUKeepsGenerated`.
2. **Quick buttons of a hidden category left the sale screen**
   (`internal/ui/buttons.go`, `dropHiddenGroups`). Contract §3.2 keeps the
   items sellable by search, scan **and quick buttons**. With the All tab off
   they were unreachable. **Fixed:** an explicit quick button
   (`Button.QuickButton`, set by `LoadWith` for `shortcut_buttons` rows)
   anywhere in a hidden subtree moves to the uncategorised bucket, in its
   global order. An implicit catalog tile still leaves with its category,
   because hiding would mean nothing otherwise, and the category-tabs tiles
   (`BuildCategoryTiles`) are unchanged. Tests:
   `TestBuildCategoryGroups_HiddenCategoryLeftOut` (updated: the moved quick
   buttons are in the bucket and the implicit tile is not) and
   `TestButtonsHTTPList_HiddenCategoryQuickButtonStaysReachable` (All tab on
   and off).
3. **No recovery when the cloud refused the snapshot** (`cloudsync.go`). A
   413 from an older cloud (4 MiB cap) or an oversize catalog re-uploaded
   the whole body every tick with a warning each time, and nothing checked
   the size locally. **Fixed** (`internal/cloudsync/snapshot_guard.go`):
   - A payload over 16 MiB (the schema-2 cap) is never posted. It is logged
     once at warn level with its size, which also puts it in the
     heartbeat's problems digest (`collectProblems` reads the warn/error
     ring).
   - A content refusal (413, 400, 422 and the rest of `rejectedRollup`'s
     set) backs off exponentially: 5 min, doubling, capped at 6 h. It is
     logged once per distinct failure, and a success resets the backoff.
   - A transient failure (5xx, network) still returns the error as before.

   Tests: `TestSnapshotOverSizeCapIsNotPosted`,
   `TestSnapshot413BacksOffExponentially` and `TestSnapshotBackoffBounded`.
4. **`price_minor` had no upper bound** (`catalog_save_repo.go`). **Fixed:**
   a price above 999 999 999 is rejected, the same bound as a modifier
   option's price delta. Test: `TestSaveItem_PriceUpperBound`.
5. **A satellite logged every pending main-till-only directive on every
   tick** (`cloudsync.go`). **Fixed:** the skip is logged once per directive
   id, from a set bounded at 256 ids (cleared when full). Test:
   `TestSatelliteSkipLoggedOncePerDirective`.
6. **The hidden flag was written after the create/update had committed**
   (`categories_page.go`). A failure there left a half-saved category.
   **Fixed:** the flag now goes in the same statement, through the new
   `CatalogRepo.CreateCategoryWithColorHidden` and
   `UpdateCategoryWithHidden` (`COALESCE(?, sell_screen_hidden)`: nil keeps
   the flag). `SetCategorySellScreenHidden` no longer had a caller and was
   removed. Test: `TestCategoryDialog_HiddenFlagFailureLeavesNothingHalfSaved`.
   A trigger forces the failure, and the test checks that no category is
   created and nothing is renamed.
7. **The audit row is written after the commit, and unknown payload fields
   are ignored.** Left as is on purpose. This matches every existing
   directive: the audit is best-effort and logged, and ignoring unknown
   fields keeps the till forward-compatible with a newer cloud.

## Verified sound (no change)

- Each directive applies in one `BEGIN IMMEDIATE` transaction, all or
  nothing. Validation that needs no database runs before the write lock.
- Idempotency: replaying `save_item`, `save_category` or
  `save_modifier_group` leaves the same state, and a delete of something
  already gone reports `applied`.
- Satellite tills skip the five types without posting a result, so the
  directive stays pending for the main till.
- The barcode conflict stays a `*BarcodeConflictError` naming the owner.
  Variant barcodes are untouched by the item-level set replace.
- Category moves: the cycle check walks the ancestors, depth is ≤ 3, and
  `delete_category` re-parents children and moves items before the soft
  delete.
- `save_modifier_group` rejects out-of-range bounds instead of coercing them.
  The full ordered option set is applied, and past sales are unaffected.
- The icon id is format-checked only and renders through the icon registry.
  An unknown or malformed id draws the neutral fallback and never reaches the
  page raw.
- Migration 041 is append-only and pinned, and both columns travel in the
  ADR-0011 LAN sync.
- Snapshot schema 2 includes inactive items (active first) and nests the
  variants, and a variant's stock never folds into its parent's `qty`.

## Checks

`gofmt -l .` clean; `go build ./...` ok; `go test ./...` all ok; the
`ci.yml` build-job guards pass. The docs-shots surface hash was refreshed
with `update-docs-shots-surface-hash.sh`: no rendered pixel changed, because
the `categories_page.go` change only affects the error path. The following
local failures are environmental and also fail on the unchanged branch:

- `golangci-lint`: `typecheck` fails on the untouched `schedule.go`, because
  the local lint binary can't import Go 1.27's `math/rand/v2`.
- `shellcheck`: the local 0.11.0 reports SC2329 info notices and fails
  `guard-shellcheck-version` (0.9.0 expected).
- `guard-competitor-naming_test.sh`: BSD `sed`.
- `prune-stale-worktrees_test.sh`: fails the same way on the unchanged
  branch.

CI runs these on Linux.
