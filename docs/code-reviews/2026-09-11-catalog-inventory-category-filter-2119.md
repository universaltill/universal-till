# Code review: category filter for /catalog and /inventory (ut-docs#2119)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2119
**Branch:** `feat/2119-category-filter`
**Pipeline:** Sonnet Dev → independent Opus review (different model, fresh context)

## Summary

Adds a multi-select category-filter chip row to `/catalog` and `/inventory`,
composing with the existing search box (AND), OR across selected
categories, with selecting a parent category including its descendants.
Full BA/Architect/UX rationale is recorded as comments on ut-docs#2119 —
this record covers Dev → Review → fixes only.

## Files changed

New: `web/ui/partials/category_filter.html`, `web/public/category-filter.js`,
`internal/data/pos_repo_stock_category_2119_test.go`,
`internal/pages/catalog/category_filter_2119_test.go`,
`internal/pages/inventory_category_filter_2119_test.go`,
`internal/httpx/category_filter_partial_test.go`,
`e2e/tests/catalog-inventory-category-filter-2119.spec.ts`.

Modified: `internal/data/pos_repo.go` (LowStockItem.CategoryID,
ListStockLevels/variantStockLevels queries), `internal/pages/catalog/handlers.go`,
`internal/pages/inventory_page.go`, `internal/httpx/httpx.go`,
`internal/httpx/sync_banner_test.go`, `internal/pages/catalog/tax_code_display_test.go`,
`web/ui/layouts/base.html`, `web/ui/pages/{catalog,inventory}.html`,
`web/ui/partials/{catalog_row,catalog_table,stock_table}.html`,
`web/public/app.css`, `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,ar,de,fa,tr}/{catalog,inventory}.md` (+ regenerated screenshots),
`scripts/ci/i18n-baseline/help-drift-baseline.json`.

## Independent review findings (Opus, fresh context, working tree not committed by Dev)

Nothing in the money/tax/data-loss/security class. One real functional
defect required a fix before commit; three non-blocking findings; two nits;
one process item.

- **F1 (fixed) — chip row was inert when reached via the `/items` rail.**
  `CategoryFilter.bind()` was gated on a plain `DOMContentLoaded` listener
  in both `catalog.html` and `inventory.html`. That event has already
  fired by the time a rail section-swap (`hx-target="#items-panel"`)
  re-executes the page's inline `<script>` block, so the listener never
  ran on that path — chips rendered with full styling but tapping one did
  nothing, silently. Both new e2e specs only entered via `page.goto()`
  (a full load), which is why this wasn't caught. Fixed with the same
  `document.readyState === 'loading' ? addEventListener(...) : run()`
  idiom already used by `record-dialog.js`/`app.js`/`autofill.js`.
  **Added regression coverage**: two new e2e tests navigate via the rail
  (`/items` → click the Inventory row; `/items` → Inventory → back to
  Catalog) and assert the chips actually narrow the list on that path —
  verified to fail before the fix (revert-then-restore) and pass after.
- **F4 (fixed) — unguarded `window.CategoryFilter.matches(...)` in the
  search hot path.** If `category-filter.js` ever failed to load, every
  keystroke in the search box would throw, taking out pre-existing search
  as a new failure mode. Changed to fail open:
  `!window.CategoryFilter || window.CategoryFilter.matches(...)`.
- **F3 (fixed) — manual said chips render "above the search box"; they
  render below it.** One-word fix (`above` → `below`) across all 5
  locales × 2 topics (10 files) — doesn't change help-drift's structural
  counts, re-verified green after the edit.
- **F2 (not fixed, filed as a follow-up) — a still-active child category
  under a deactivated parent becomes unreachable by the filter.**
  `category_filter.html` only renders a chip for a node with an empty
  `ParentID`; if that parent is later deactivated (allowed today since
  `SetCategoryActive` only blocks on *direct* items, not descendants'
  items), the child has no chip of its own and no reachable ancestor chip
  to expand into it — its items become unfilterable-into once any chip is
  pressed. Low real-world frequency (nesting is import-only in practice
  today, per the BA pass), so left as a Backlog follow-up rather than
  widening this card; see ut-docs#2140.
- **F5 (nit, not fixed) — `stripDataAttrs` test-helper's new `<script>`
  strip is broader than the one call site that needed it.** Legitimate
  and doesn't weaken any existing assertion (verified: the item-edit
  `<option>` pin it protects is untouched and still passes), but blanks
  every inline script rather than just the `categoryNodes` assignment.
  Left as-is per the reviewer's own "nit, not fix-now."
- **F6 (observation only) — a third `.chip`-shaped visual pattern now
  exists** (`.cat-chip` already covers this same "All + per-category"
  shape elsewhere). No action this card.
- **F7 (process, tracked) — `lang-pack-drift` will go red on `main` until
  `ut-plugin-language-{de,es}` get the three new `filter.*` keys.** Per
  this pipeline's "the lane that merges owns the follow-up" rule, filed
  and will be landed in the same cycle as this merge; see close-out
  comment on ut-docs#2119 for the tracking issue.

## TDD re-verification (real revert-then-restore, not assertion)

- Tester covered `TestListStockLevels_CarriesCategoryID` and the original
  e2e spec.
- Reviewer independently covered two different tests:
  `TestCatalogPage_RendersCategoryFilterChipRow` (stashed
  `handlers.go` → 500, "no such template `category_filter`") and
  `TestInventoryPage_RendersCategoryFilterChipRowAndRowCategoryID`
  (stashed `inventory_page.go` → the partial's own `req` guard fires by
  name, "required key `categories` is missing"). Both restored cleanly.
- This session re-verified the two F1 regression tests fail without the
  fix (reverted `catalog.html`/`inventory.html`, both new rail-swap tests
  failed with a 5s timeout locating the chip row / a 30s bind timeout)
  and pass with it restored.

## Gate (final, after the F1/F3/F4 fixes)

```
gofmt -l .                          → (no output)
go build ./...                      → OK
go vet ./...                        → OK
go test ./...                       → every package ok, 0 FAIL
golangci-lint run ./...             → 0 issues
guard-i18n.sh                       → ✓ 1652 keys resolve, all locales match
guard-data-access.sh                → ✓ no inline SQL outside internal/data
guard-compliance-claims.sh          → ✓ no forbidden claims
guard-help-topics.sh                → ✓ no route conflicts, coverage complete
guard-help-drift.sh                 → ✓ (pre-existing tracked drift only)
guard-docs-shots.sh                 → ✓ 31 topics × 4 locales fresh
guard-e2e-fixtures-import.sh        → ✓ 111 specs
```

Full e2e regression sweep across every catalog/inventory-tagged spec:
**103 passed, 0 failed** (includes the original 3 + 2 new F1-regression
tests from this card, plus every pre-existing catalog/inventory spec).

## Could not verify

- Real touchscreen hardware (headless Chromium only — chip tap is a
  simple click/pointer event, lower risk than a drag/scroll gesture).
- A real long German category name (demo seed categories are English
  only) — verified structurally instead (`inline-size: auto;
  max-inline-size: none; white-space: normal` in the new CSS, confirmed
  in the diff, no fixed width to truncate against).
- Self-hosted NAS translation endpoint — unreachable from this sandbox
  (confirmed independently by Dev, Tester, and Reviewer). The three new
  `filter.*` keys and the catalog/inventory manual tips ship as English
  literals in ar/fa/tr/de, per this repo's own `blocked:env` convention
  for this exact, previously-hit wall.

## Product-owner note (carried from BA)

An item with no category is excluded whenever any category filter is
active — no "Uncategorised" pseudo-option in v1. Implemented as designed,
but flagged again here as a real product decision worth a nod, not purely
an engineering default.
