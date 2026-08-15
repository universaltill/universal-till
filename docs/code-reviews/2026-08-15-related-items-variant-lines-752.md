# Code review: "frequently bought together" excludes variant sale lines (ut-docs#752)

**Date:** 2026-08-15
**Author (Dev/Tester):** Claude (Sonnet), autonomous SDLC pipeline
**Reviewer:** Claude (Sonnet), independent fresh-context subagent
**Branch:** `pipeline/752-related-items-variant-lines` (not yet pushed at review time)
**Complexity:** easy

## What shipped

Found during independent review of ut-docs#744's fix (PR universaltill/universal-till#357):
`internal/data/related_items_repo.go`'s co-occurrence `Rebuild` query filtered
`WHERE sl.item_id IS NOT NULL` — the only sale-line reporting query in the
codebase that doesn't resolve a variant-only line back to its parent item via
`item_variants`. `pos_repo.go`'s `SalesByDepartment`/`DepartmentsForDay`/
`DeadStock`/etc. (lines ~733, 804, 950, 1063, 1188, 2140) already do
`LEFT JOIN item_variants iv ON iv.id = sl.variant_id` +
`COALESCE(sl.item_id, iv.item_id)`. This wasn't reachable before #744 fixed
variant checkout (a variant line couldn't persist at all); now that variant
sales complete, any sale containing a variant line silently contributed
nothing to "frequently bought together" for that item.

**Fix:** applied the same established `LEFT JOIN item_variants` +
`COALESCE(sl.item_id, iv.item_id)` pattern to the `sale_items` CTE inside
`Rebuild`'s query — a 3-line change (join added, two references updated to
the coalesced id).

**Tests added** (TDD, confirmed failing pre-fix by both Dev and,
independently, the Reviewer):
- `internal/data/related_items_repo_test.go`:
  `TestRelatedItems_VariantSaleLineContributes` — seeds two baskets pairing a
  shirt *variant* (not the bare item) with a belt, asserts `SuggestForBasket`
  returns the belt when the basket contains the parent item — this only
  passes if the variant line resolves to its parent item at all.
- `internal/testsupport/sqlite_catalog.go`: added `variant_id TEXT` to the
  synthetic `sale_lines` table (the real schema already has this column,
  `001_init.sql`; the test-support schema was missing it) and a new
  `SeedCompletedSaleVariant` helper mirroring `SeedCompletedSale`'s shape but
  writing variant-only lines (`item_id` NULL, `variant_id` set — the shape a
  real variant checkout persists per the `sale_lines` CHECK constraint).

## Independent review — findings

Reviewed at Sonnet (fresh context, this card's `complexity:easy` routing),
with an explicit brief to verify correctness independently rather than
confirm the work. Verdict: **PASS, safe to merge as-is.**

1. **Pattern match confirmed against precedent.** The fix reproduces
   `pos_repo.go`'s `SalesByDepartment`/`DepartmentsForDay` join shape exactly.
   (`DeadStock` uses a defensive `COALESCE(NULLIF(sl.item_id,''), v.item_id)`
   guarding against an empty-string `item_id`; the reviewer confirmed the
   `sale_lines` CHECK constraint makes that extra guard unnecessary here —
   not a discrepancy worth matching.)
2. **Edge cases traced through the schema**, independently: item-only lines
   unchanged; variant-only lines now resolve correctly; a variant whose
   parent item was deleted cascade-deletes the variant row too
   (`item_variants.item_id ... ON DELETE CASCADE`), so both ids are
   unavailable and the row is correctly excluded by the `IS NOT NULL` filter
   (no crash); a deactivated-but-not-deleted parent item is unaffected —
   `Rebuild` never filtered on `is_active` even pre-fix (that happens
   downstream in `SuggestForBasket`), so this is consistent, not a
   regression.
3. **No other spot needs the same treatment.** The reviewer traced
   `SuggestForBasket`'s own query independently (not assumed): `related_items`
   is populated only by `Rebuild`, which now always writes resolved
   (COALESCE'd) item ids, never variant ids; the one caller
   (`internal/pages/suggestions_api.go`) feeds `SuggestForBasket` from
   `BasketLine.ItemID`, which every resolver path (`resolveVariant`,
   `resolveVariantSKU`, `resolveVariantNameLike` in `pos_repo.go`) already
   sets to the *parent* item id even for a variant-line basket entry. So
   `SuggestForBasket` itself needs no change.
4. **Style/idiom**: `gofmt -l` on all 3 changed files empty; `go vet` clean;
   comment density/doc-comment style matches the surrounding file.
5. **Scope check**: diff is backend-only (`internal/data`,
   `internal/testsupport`) — no template/UI/locale/help-topic surface
   touched, so the i18n/UX/help-manual obligations don't apply.

No blocker or minor findings. Nothing deferred.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `go test ./...` (full module, no `-race`): 100% pass.
- `gofmt -l` on all 3 changed files: empty.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`:
  all pass (no new raw SQL outside `internal/data`; diff never touches a
  kiosk/self-order route or plugin-menu read).
- **TDD re-verified independently, twice**: Dev reverted
  `related_items_repo.go` alone, saw `TestRelatedItems_VariantSaleLineContributes`
  fail (`expected belt suggested via the variant-line purchases, got []`),
  restored, saw it pass. The Reviewer then independently repeated the same
  stash/revert/restore in place and confirmed the identical failure/pass
  transition, then reran the full `internal/data`/`internal/testsupport`
  suite to confirm no regression from the additive schema/helper change.

## Safe-to-merge verdict

**Yes.** Small, mechanical, precedent-matching fix; independently
re-derived and confirmed correct against the schema's actual constraints
(not just pattern-matched); TDD claim independently reproduced; no
regressions in the full test suite or any of the three CI guards.
