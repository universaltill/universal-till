# 2026-09-12 — Category filter: promote an orphaned active child to its own chip (ut-docs#2140)

## What shipped

`web/ui/partials/category_filter.html` only renders a filter chip for a
category node with an empty `ParentID` — a nested child folds into its
parent's chip instead of getting a second one (ut-docs#2119's own "selecting
a parent includes its children" decision). `CatalogRepo.SetCategoryActive`
only blocks deactivating a category when items point **directly** at it, not
descendants, so a top-level category with no direct items but an active
child **can** be deactivated. Once deactivated it disappears from
`ListActiveCategories`, but its still-active child keeps a non-empty
`ParentID` pointing at a category that no longer appears in that list — so
the child never gets its own chip either, and its items become permanently
unreachable through the category filter on both `/catalog` and `/inventory`.

Fix:

- New `data.TopLevelForFilterChips(nodes []CategoryNode) []CategoryNode` in
  `internal/data/catalog_repo.go`, right after `ListActiveCategories`: for
  chip-rendering purposes, treats a node whose `ParentID` is non-empty but not
  present in the same active-only slice as top-level (clears its
  `ParentID`). Pure Go, no SQL, returns a new slice — input never mutated.
- Both call sites (`internal/pages/catalog/handlers.go`'s `/catalog` handler
  and `internal/pages/inventory_page.go`'s `/inventory` handler) now run
  `ListActiveCategories`'s result through this function before it's used as
  both `CategoryFilterOptions` (chip rendering) and the source for
  `CategoryNodesJSON` (the full flat list `category-filter.js`'s `expand()`
  walks for parent-includes-children matching).
- `internal/testsupport/sqlite_catalog.go`: new `SeedInactiveCategoryTree`
  helper (mirrors `SeedCategoryTree`, `is_active=0`).
- Regression tests: a pure-function suite for `TopLevelForFilterChips`
  (`internal/data/catalog_repo_top_level_filter_2140_test.go`) plus one
  handler-level test on each of `/catalog` and `/inventory` seeding exactly
  the deactivated-parent/active-child shape.
- `web/help/img/manifest.json` / `web/help/img/en/sell.png` regenerated via
  `make docs-shots` — required because `guard-docs-shots.sh` hashes whole
  non-test `.go` files under `internal/pages/**` that register a
  screenshotted route, and both edited handler files register one
  (`/catalog`, `/inventory`). No template/CSS/JS changed; this is a
  same-content re-hash, not a visual change.

## TDD

Confirmed real, not just claimed: the two handler-level tests were run
against the code with only the two `categoryFilterOptions =
data.TopLevelForFilterChips(...)` integration lines removed (helper function
and tests left in place) — both failed with
`expected Hot Drinks promoted to its own chip once its parent was
deactivated; got: ...`. Restored, both passed again. Independently
re-verified by the reviewer subagent (see below) with the identical method.

## Independent review

Fresh-context Sonnet subagent (this is a `complexity:easy` card — Sonnet
built it, Sonnet reviews it in a clean instance, per the pipeline's model-
routing rules), run in an isolated git worktree.

Verified for real, not taken on trust:
- `gofmt`, `go build`, `go vet` clean.
- The new/changed tests individually, then the broader `internal/data` +
  `internal/pages/...` suites (2404 tests in the root `internal/pages`
  package alone) — all green, no collateral damage.
- Independently repeated the TDD revert/restore itself and got the same
  failure signature before restoring.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh` — all
  green.
- `golangci-lint run` on the touched packages — 0 issues.
- Read `TopLevelForFilterChips` and reasoned through a 3-level chain
  (grandparent deactivated → parent deactivated → active grandchild): the
  function only needs to check *immediate* parent presence in the
  already-active-filtered slice, which is sufficient at any depth, since a
  deactivated ancestor further up the chain is already absent from that same
  slice.
- Checked whether two independently-orphaned siblings could collide:
  they can't — category IDs are unique, so each promoted node just becomes
  its own independent top-level chip.
- Read `web/public/category-filter.js`'s `expand()` directly (not taken on
  the diff's own doc-comment claim) and confirmed it only ever walks a
  node's *children* via `parentId`, never a node's own parent chain upward —
  so clearing an orphan's own `ParentID` cannot affect how its descendants
  match.
- Checked the other two consumers of `ListActiveCategories`
  (`kitchen_stations_page.go`'s flat routing grid, and
  `internal/ui/buttons.go`'s `BuildCategoryGroups` for the sale-screen tab
  bar) for the same latent bug — the first renders every active category
  flat with no top-level filtering (unaffected), the second already
  self-heals a missing parent via its own `byID[c.ParentID], ok` fallback to
  root. This bug and its fix are correctly scoped to just the two
  chip-rendering call sites this diff touches.
- Confirmed no new user-facing string (money/offline-first: not
  applicable), no real client/shop name in test data, no file-write/path
  issues (diff has no file-write handlers).

**Verdict: SAFE TO MERGE. No findings** — no correctness, simplification,
or efficiency issues.

## Verified beyond automated tests

The chip element itself carries zero new markup or CSS — the fix only
changes which of the *already-existing*, already-screenshotted chip buttons
renders for one additional (rare, import-only-today) data shape. The
handler-level tests exercise the real `html/template` rendering through the
real mux (not a mock), so the rendered markup for the newly-promoted chip is
byte-identical to any other top-level chip already visually verified across
`en`/`fa`/`ar`/`tr` in prior cards (ut-docs#2119). A dedicated browser
screenshot of this specific edge case was **not** taken — noted explicitly
per the Tester skill's "say so if you didn't look" rule, since the risk is
judged low (no new visual pattern, only an existing one applying to one more
case) rather than zero.

## Deferred / out of scope

None — this card's acceptance criteria (chip render fix only) are fully
covered. `CatalogRepo.SetCategoryActive`'s direct-items-only deactivation
block is an intentional, separate design (ut-docs#1898) and stays unchanged
per the card's own non-goals.
