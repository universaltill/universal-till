# Review: color-coded nested category grid on the sale screen (ut-docs#44)

**Date:** 2026-08-01 · **Branch:** `feat/44-category-grid` · **Card:** universaltill/ut-docs#44 (p2)

## What shipped

The sale-screen shortcut grid (`/ui/buttons`, `internal/ui/buttons.go` +
`web/ui/partials/buttons.html`) was a 100% flat list with zero category
awareness, even though `categories` already supported an arbitrary-depth
tree (`parent_id`, `sort_order` — `internal/db/migrations/001_init.sql`).
Confirmed empty (grep, recent `docs/code-reviews/`, no prior card) before
scoping.

1. **`internal/db/migrations/025_category_color.sql`** — nullable
   `categories.color` (hex), append-only, next free version.
2. **`internal/data`**: `CatalogRepo.ListCategories` (flat category rows,
   `parent_id`/`color` intact); `ShortcutsRepo.LoadButtons` now also
   selects the item's `category_id`.
3. **`internal/ui/buttons.go`**: `BuildCategoryGroups(buttons, cats)` — a
   pure function that nests buttons under their category by walking
   `ParentID`, prunes branches with no buttons anywhere in their subtree,
   and buckets uncategorized/dangling-category-ref buttons into a trailing
   synthetic group (`ID == ""`). `resolveCategoryColor` uses an explicit
   `#RRGGBB` color if set, else a deterministic FNV-hash auto-color from a
   fixed 8-color palette — every till is color-coded with zero admin
   configuration required. `ButtonsHTTP.List` renders `Groups` instead of a
   flat `Buttons` list.
4. **`web/ui/partials/buttons.html`** — rewritten to recursively render
   nested `category-group` sections (color via `style="--cat-color: ..."`);
   the actual tile markup factored into an unchanged `product-tile`
   sub-template.
5. i18n: `products.uncategorized` added to all four locales (en/fa/ar/tr).
6. CSS: `.category-group`/`.category-header` in `web/public/app.css`,
   logical properties only.

## Independent review

Spawned an independent `opus`-model review (different from the implementing
model), briefed to actually run the build/tests/guards and try to break the
change, not just read the diff. Verdict: **yes, with fixes** — nothing
blocking. All fixes below applied and re-verified before merge.

- **Real bug found and fixed before the reviewer even finished (self-caught
  via the reviewer's in-progress scratch test file, then independently
  confirmed by the reviewer's own mutation testing): a malformed/cyclic
  `parent_id` chain (a category whose parent is itself, or a 2-node loop)
  silently dropped every button assigned to it off the sale screen.** Root
  cause: such a category always "found" a valid parent (itself, or its
  cycle-mate) during tree-building, so it was never added to `roots` and
  thus never reachable from anything `pruneEmptyCategoryGroup` traverses —
  its buttons were attached to a node the render tree never visits, not
  even landing in the uncategorized bucket. Fixed with `isCategoryAncestor`
  — refusing to attach a category as a child of its own descendant, instead
  surfacing it as a root and breaking the cycle without losing buttons.
  Regression tests: `TestBuildCategoryGroups_SelfParentCycleDoesNotDropButtons`,
  `TestBuildCategoryGroups_TwoNodeCycleDoesNotDropButtons`. Reviewer
  independently mutation-tested the fix (removing the guard fails both
  tests) and confirmed no hang/panic up to a 20,000-deep chain.
- **Should-fix — `LoadCategories` error was silently discarded**
  (`cats, _ := ...`), degrading the whole grid to one ungrouped bucket with
  no log line on a till whose migration hadn't applied or hit a transient
  SQLite error. Fixed: logged via `logging.L().Errorf`.
- **Should-fix — every existing till (nothing has categories configured
  yet) would show one pointless "Uncategorized" header above its entire
  grid**, a visible regression for the whole current install base. Fixed:
  `buttons.html` renders flat (no header, no wrapper) when the *only*
  group is the synthetic uncategorized bucket — same as pre-feature
  behavior. Regression test: `TestButtonsHTTPList_FlatWhenNoCategoriesConfigured`
  (mutation-tested: reverting the template's flat-fallback branch fails
  it).
- **Should-fix — zero test coverage of the actually-rendered HTML**, the
  highest-risk part given `html/template`'s CSS-context escaping can
  silently rewrite a style value to `ZgotmplZ` with no error. Added
  `TestButtonsHTTPList_RendersNestedColorCodedGroups` (asserts the real
  rendered output: explicit color survives verbatim, nested document
  order, no `ZgotmplZ`) and the flat-fallback test above, both driving
  `ButtonsHTTP.List` through the real embedded templates.
- Nitpicks accepted as-is, not fixed (explicitly out of scope / genuinely
  low priority): `O(n·depth)` grouping allocation pattern (fine for
  realistic café category counts — reviewer measured 500 categories/depth-2
  at 125µs; only degenerate synthetic chains in the tens-of-thousands are
  slow); test-failure diagnostics printing struct pointers; pre-existing
  `internal/testsupport` schema drift (`is_active` on the hand-rolled
  `categories` table, which the real schema doesn't have) — flagged so
  nobody "fixes" `ListCategories` with an `is_active` filter that would
  break in production; `ui.Button.CategoryID`'s `json:"categoryId"` tag
  (camelCase) — vestigial, `Button` is never marshalled to any HTTP
  response.

## Verified beyond automated tests

- **Real running app**: built the actual binary, ran it against a real
  SQLite DB, seeded a nested category tree (explicit + auto color) plus an
  uncategorized item via the demo catalog's existing seed, and fetched
  `/ui/buttons` directly. Confirmed: correct nested `<section>` structure,
  explicit and auto-assigned colors both render, the pre-existing demo
  catalog's own "Food > Dairy" tree (previously invisible in the flat
  grid) now renders nested for free, uncategorized bucket works, `?lang=fa`
  correctly renders "دسته‌بندی‌نشده" for the uncategorized header.
- **Real Playwright e2e suite**: `sale-screen-213.spec.ts` (6 tests) +
  `tender-panel-reachable.spec.ts` (2 tests) run against the real built
  server — all 8 pass; the `.products` container geometry these specs
  check is unaffected by the grid's internal restructuring.
- **CSS injection, independently verified twice** (implementer + reviewer):
  `resolveCategoryColor`'s `^#[0-9a-fA-F]{6}$` whitelist rejects anything
  else (no trailing-newline bypass), and even a value that somehow bypassed
  it is independently caught by `html/template`'s own CSS-context escaping
  inside the `style="--cat-color: ..."` attribute (confirmed: a
  `background:url(javascript:...)` value renders as `ZgotmplZ`, never
  executes).
- **Migration interaction with existing upgrade-simulation tests**: adding
  a new non-idempotent `ALTER TABLE ADD COLUMN` (025) broke two existing
  tests that rewind `schema_migrations` to simulate a pre-023 upgrade
  (`TestSeedBarcodeChecksumsFixedOnUpgrade`, `TestDeadTaxInclusiveSeedRemovedOnUpgrade`)
  — they physically drop migration 024's column when rewinding but hadn't
  been told about 025 yet. Fixed by also dropping `categories.color` in
  both tests' rewind step, following the exact pattern already established
  there for 024. (Caught by running the **full** `go test ./...`, not just
  the directly-touched packages — confirmed genuinely caused by this change
  via `git stash -u`, which unlike a plain `git stash` also stashes new
  untracked files such as the new migration.)
- `go build ./...`, `go vet ./...` clean. `go test ./...` fully green
  except one pre-existing, unrelated failure (`internal/issuereport`
  `TestSaveCleansUpDirectoryOnWriteFailure`, confirmed failing identically
  on `origin/main` via `git stash -u` — not filed as a new card by this
  change since it's out of scope, but worth someone picking up).
- `scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` both
  pass (no SQL outside `internal/data`/`internal/db`; all 4 locales have
  every key, including the new `products.uncategorized` — ar/fa/tr
  translations independently checked as idiomatic, not machine-garbled).
- No real client/shop name or secret-shaped literal anywhere in the diff
  (seed/test data: generic "Drinks"/"Cakes"/"Latte" etc.).

## Explicitly out of scope (deferred)

- No admin UI to set a category's color yet — every category is
  auto-colored today; `categories.color` exists for a future admin form to
  write to. Not filed as a separate card yet (small enough to fold into
  whichever card first asks for category management UI).
- Self-order kiosk's own flat category-chip filter (a different surface,
  `web/ui/partials/self_order_grid.html`) untouched.

## Safe to merge

Yes. All should-fix findings applied and re-verified (build/vet/full
affected-package tests/guards green); the one blocking-class bug (cycle
handling) was caught and fixed pre-merge with regression tests, verified
independently by the reviewer via mutation testing.
