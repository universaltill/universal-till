# Code review: catalog category/brand select (ut-docs#1430)

- **Date**: 2026-09-02
- **Branch**: `fix/1430-catalog-category-brand-select`
- **Card**: universaltill/ut-docs#1430 — "Catalog admin: the category (and
  brand) field shows the raw UUID — datalist options use the id as the
  value and the edit form loads the id into a free-text input."
- **Reviewer**: independent pass, fresh context, no visibility into the
  Dev/Tester reasoning that produced the diff.

## What shipped

- `internal/data/catalog_repo.go`: new `GetLookup`/`ErrLookupNotFound`,
  a single-row id→name lookup for categories/brands (mirrors the existing
  `GetTaxCode`/`ErrTaxCodeNotFound` pair from ut-docs#1178/#1363), used by
  the per-row OOB re-render so it doesn't pay a whole-table read.
- `internal/pages/catalog/handlers.go`: `lookupNameFunc`, a generalized
  version of `taxCodeNameFunc`, registered as `categoryName`/`brandName`
  template funcs built from the already-fetched category/brand lists (no
  extra query on the full `/catalog` page load).
- `internal/pages/catalog/row_oob.go`: the row-level OOB fragment now
  resolves `categoryName` via `GetLookup` (single row) the same way it
  already resolved `taxCodeName`.
- `internal/httpx/httpx.go`: added no-op `categoryName`/`brandName`
  defaults to `baseFuncs`, same rationale as the existing `taxCodeName`
  no-op — templates that pull in `catalog_row.html` without the catalog
  package's real funcs still parse.
- Templates: `web/ui/pages/catalog.html`'s category/brand fields changed
  from free-text `<input list="…">` (datalist, **value = id**) to real
  `<select>`s populated by name; `web/ui/partials/catalog_row.html` grew a
  category column; `catalog_table.html`'s header grew a matching `<th>`;
  `web/ui/partials/catalog_lookups.html` (the old datalist partial)
  deleted, with all its registration/reference sites (`handlers.go`,
  `sync_banner_test.go`) updated to match.
- The barcode-autofill JS in `catalog.html`, which used to match a
  looked-up product's brand text against `#brands-list option`s, was
  retargeted at `#item-brand option` (found while adding e2e coverage —
  no existing spec drove that path at all before this card).
- Locale keys: `catalog.brand.none`, `catalog.category.none`,
  `catalog.col.category` added to all four `web/locales/*.json` files.
- `web/help/img/*/catalog.png` regenerated (the edit form and items table
  both look different now), `web/help/img/manifest.json`'s surface hash
  updated to match.
- New tests: `internal/data/catalog_repo_crud_test.go` (`TestGetLookup`),
  `internal/pages/catalog/catalog_lookup_display_test.go`
  (`TestCatalogPage_CategoryBrandShowNameNotID`,
  `TestCatalogTablePartial_CategoryShowsNameNotID`),
  `e2e/tests/catalog-category-brand-select-1430.spec.ts` (3 cases: create
  shows name not id + round-trips through edit, no-category placeholder,
  barcode-autofill brand match against the retargeted select).

## Independent review process note (worth recording)

This review's isolated worktree was initially checked out on a stale
branch tip (`c2f31a1`, the merge-base) rather than the WIP snapshot under
review (`4436e57`) — a setup issue in how the worktree's own branch had
been left, not anything in the diff itself. The first pass of
build/test/guard commands therefore silently verified the *pre-fix* tree
and would have been a false pass. Caught by noticing `git rev-parse HEAD`
after the fact didn't match the commit the diff was read from; fixed with
`git reset --hard 4436e57` on this worktree's own branch (no effect on
the actual `fix/1430-…` branch or any other checkout), then every gate
below was re-run for real. Flagging this so a future review double-checks
`git rev-parse HEAD` against the commit it intends to review as step one,
not just trusts the branch name.

## Verification performed (after the above fix, on the correct tree)

| Gate | Result |
|---|---|
| `gofmt -l .` | clean, no output |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./internal/data/... ./internal/pages/... ./internal/httpx/...` | all packages `ok` |
| `scripts/ci/guard-data-access.sh` | pass — no inline SQL outside `internal/data`/`internal/db` |
| `scripts/ci/guard-i18n.sh` | pass — 1341 keys resolve, all 4 locales match `en.json` |
| `scripts/ci/guard-docs-shots.sh` | pass — surface hash `1eb5dac04a0d…` matches manifest |
| `scripts/ci/guard-help-topics.sh` | pass — route coverage intact |
| `scripts/ci/guard-kiosk-engine.sh` | pass (diff doesn't touch kiosk routes) |
| `scripts/ci/guard-compliance-claims.sh` | pass |
| `scripts/ci/guard-plugin-menu-read.sh` | pass |
| `scripts/ci/guard-autofill-suppression.sh` | pass (relevant: catalog.html's autofill JS was touched) |
| e2e: `npx playwright test --project=default -g "catalog"` (headless-shell, resolved via `e2e/scripts/resolve-chromium.sh`) | **32/32 pass**, including all 3 new `catalog-category-brand-select-1430.spec.ts` cases |

## Own TDD red→green re-verification

Reverted only the production files (`internal/httpx/httpx.go`,
`internal/pages/catalog/handlers.go`, `internal/pages/catalog/row_oob.go`,
`web/ui/pages/catalog.html`, `web/ui/partials/catalog_row.html`,
`web/ui/partials/catalog_table.html`, and restored the deleted
`web/ui/partials/catalog_lookups.html`) to their pre-fix (`c2f31a1`)
content via `git checkout c2f31a1 -- <files>`, leaving the new test files
at HEAD, then ran:

```
go test ./internal/pages/catalog/... -v \
  -run 'TestCatalogPage_CategoryBrandShowNameNotID|TestCatalogTablePartial_CategoryShowsNameNotID'
```

Both failed with real, on-topic assertion errors (not compile errors):

```
--- FAIL: TestCatalogPage_CategoryBrandShowNameNotID (0.07s)
    catalog_lookup_display_test.go:54: expected the item-edit Category
    field to be a <select name="categoryId">; got: ...<input type="text"
    name="categoryId" id="item-category" list="categories-list">...

--- FAIL: TestCatalogTablePartial_CategoryShowsNameNotID (0.01s)
    catalog_lookup_display_test.go:106: expected category name "Snacks"
    in the re-rendered table partial; got: ...<tr class="catalog-row" ...>
    ...only 6 data <td>s, no category column at all...
```

Restored all six files to HEAD (`git checkout HEAD -- <files>`, plus
`git rm` the re-restored old partial since it doesn't exist at HEAD),
confirmed `git diff --stat 4436e57` is empty (working tree exactly
matches the reviewed commit again), and re-ran the two tests: both
`PASS`.

## Findings

### 1. (Deferred, not blocking) `categories`/`brands` active-filter is dead code in production today, but the select-population path doesn't share ut-docs#1178's fix — latent risk if that ever changes

`ReadLookup` (pre-existing, unchanged by this diff) filters
`WHERE (is_active IS NULL OR is_active = 1)`, falling back to an
unfiltered `SELECT` when the column doesn't exist
(`strings.Contains(err.Error(), "no such column: is_active")`). Checked
directly:

- `internal/db/migrations/001_init.sql` — `categories` (lines 49-55) and
  `brands` (lines 59-62) have **no `is_active` column** in the real
  production schema. So in production, `ReadLookup`'s first query always
  fails and falls back to the unfiltered `SELECT` — nothing is ever
  excluded today.
- `internal/testsupport`'s synthetic test-DB schema **does** add
  `is_active INTEGER NOT NULL DEFAULT 1` to both tables (confirmed by
  reading the `CREATE TABLE` statements directly), so `TestGetLookup`'s
  "an inactive category still resolves by name" case exercises a branch
  that can't currently execute against the real schema.

This is genuine, pre-existing prod/test schema drift, not introduced by
this diff — I confirmed `internal/testsupport` wasn't touched here. It
doesn't make this diff incorrect *today*: there's also no
`DeactivateCategory`/`DeleteCategory` repository method at all (checked —
only `EnsureCategory` and `ListCategories` exist for categories; brands
has no mutator beyond insert-on-write), so an item's `CategoryID`/
`BrandID` can never point at a row absent from what `ReadLookup` returns
in production, and the new `<select>`s built from `.Categories`/`.Brands`
therefore always contain every id an item could actually carry.

The reason this is worth flagging rather than dismissing: if a future
change adds an `is_active` column to these tables in a real migration
(making prod match the test schema) or adds a category/brand deactivate
feature, `ReadLookup`'s active-only filter would start doing real work in
production, and the edit-form `<select>`s built from it would start
excluding categories/brands that some existing item still references.
Per HTML forms semantics, setting `<select>.value` to an id with no
matching `<option>` (the edit-load JS at `catalog.html:309-310`,
`row.dataset.category`/`.brand` → `.value`) leaves the control with
**nothing selected**, so it contributes no `categoryId`/`brandId` entry
to the submitted form at all; `parseItemInput` (`strPtr`) then treats a
missing field identically to an explicitly-empty one and writes `nil` —
silently clearing that item's category/brand on the next unrelated save.
This is exactly the regression class ut-docs#1178 fixed for tax codes
(`ListAllTaxCodes`, inactive-inclusive, used for the tax `<select>`'s
options specifically so this can't happen) — but categories/brands here
still use `ReadLookup` (active-only where the column exists), not an
inactive-inclusive equivalent.

**Verdict: real, correctly out of scope for this card** (nothing in
production can trigger it today; fixing it now would mean guessing at an
un-designed deactivation feature). Recommend Scrum Master file this as a
new backlog card: "harden categories/brands edit-select against future
active-only filtering, mirroring ListAllTaxCodes/ut-docs#1178" — tracking
both the schema-drift oddity and the latent select-clears-silently
footgun together, so whoever adds category/brand deactivation later
finds this note first instead of rediscovering ut-docs#1178 from
scratch.

### No other findings

- Repository pattern: `GetLookup`'s `SELECT id, name FROM `+table+` WHERE
  id = ?` interpolates `table`, but its only call site
  (`row_oob.go:154`) passes the literal `"categories"` — not user input —
  same pattern as the pre-existing `ReadLookup`/`ValidateLookup`. No
  injection surface, and the SQL text itself lives only in
  `internal/data`, matching the repository-pattern rule.
- i18n: all three new keys (`catalog.brand.none`, `catalog.category.none`,
  `catalog.col.category`) present with translated (not copy-pasted
  English) values in `ar.json`, `fa.json`, `tr.json`, confirmed by direct
  read, not just trusting the guard.
- RTL/logical CSS: no new inline styles or literal `left`/`right` in the
  touched markup; the new `<select>`/`<td>` elements carry no positioning
  CSS at all.
- Old datalist partial cleanly removed: `grep -rn
  "catalog_lookups\|categories-list\|brands-list"` across the repo turns
  up only a code comment explaining the change and the new test's
  negative assertion that they're gone — no dangling reference.
- Manual: `web/help/en/catalog.md` (and locale counterparts) prose was
  already generic about the edit form and never described the old
  free-text/datalist affordance step-by-step, so there was nothing stale
  to rewrite; the screenshot *was* regenerated (confirmed both by the
  guard passing and by reading the manifest diff — `surface_sha256`
  changed and the four `catalog.png` blobs changed size).
- `README.md`: not affected by this change (grepped for `categor`/`brand`
  mentions — none describe the edit-form widget type).
- No real client/shop names in test/demo data (`Snacks`, `Acme Foods`,
  `Drinks`, `Food`, `Coca-Cola` — the last is pre-existing demo-seed
  data, not introduced here).
- `parseItemInput` itself (acceptance criterion #2 — creating an item
  still persists the id) is unchanged by this diff; confirmed by the diff
  stat and by the e2e test asserting the created row's category cell
  shows "Food" while the select's underlying `value` is the real id.

## Fixes applied by this review

None — no blocking findings. The one deferred item above needs a backlog
card, not a code change against this card's scope.

## Safe-to-merge verdict

**PASS.** All CI-blocking gates green, TDD claim independently
re-verified (genuine red→green, not a false-pass), e2e coverage for the
new UI passes in full including a real barcode-autofill path that had no
prior coverage at all. One finding deferred to a new backlog card
(schema-drift-enabled latent footgun, not currently reachable in
production, not in scope for this card).
