# 2026-09-24: Category reorder can no longer tie an omitted category (ut-docs#2482)

## What shipped

A review of #2174 flagged that `POST /api/designer/categories/reorder`
renumbers only the ids its caller posts, from 0. If a caller ever posted a
partial list (for example, only the active categories), an omitted category
would keep its old `sort_order` and could tie with a renumbered one.

- **Fix, in one shared place.** `CatalogRepo.SetCategorySortOrder`
  (`internal/data/catalog_repo.go`) now reads every category id, inside the
  same transaction, ordered by `sort_order, name`. It appends any id the
  caller left out after the posted ids, keeping the omitted ids in their
  existing relative order, and then runs the same renumbering `UPDATE` loop.
  Both reorder routes use this method (`designer_categories_api.go` and
  `categories_page.go`), so any caller, current or future, is covered.
- **Test.** New test
  `TestSetCategorySortOrder_OmittedRowGetsAppendedNotTied` in
  `internal/data/catalog_repo_categories_test.go`. The reviewer rewrote it
  (see F1).

No handler, template, JavaScript, i18n or help change.

## Independent review

Dev implemented this change. Opus reviewed it in an isolated clone of
`fix/2482-designer-category-reorder-tie` at the WIP snapshot `a2d5d2c`, on a
separate branch, `review/2482-designer-category-reorder-tie`.

### Claim 1: the shipped clients already post every category. Confirmed

The reviewer re-derived this from the code instead of taking Dev's summary.

- **Designer route** (`/api/designer/categories/reorder`):
  - `internal/ui/buttons.go:981` loads the list with
    `LoadCategoriesForAdmin`, which calls `CatalogRepo.ListCategoriesForAdmin`
    (`catalog_repo.go:1387`). That query has no `WHERE` on `is_active` and no
    `LIMIT`.
  - `buttons.go:989-998` copies every row into `AdminCategories`, with no
    filtering.
  - `web/ui/partials/buttons.html:1046-1047` renders one
    `<li data-cat-id>` per `AdminCategories` entry, active or inactive
    (inactive rows only get the `designer-cat-inactive` class). Every row has
    both move buttons (`:1057`, `:1060`).
  - `web/ui/pages/designer.html:95-98` builds `ids` from
    `btn.closest('.designer-categories').querySelectorAll('[data-cat-id]')`.
    That is every row in the section. There is no search, filter or
    pagination on this list.
- **Categories page route** (`/api/categories/reorder`):
  - `internal/pages/categories_page.go:210` renders from
    `ListCategoriesForAdmin` too, so the list includes every category.
  - `web/ui/pages/categories.html:123-125,142-143`: `persistOrder()` posts
    `rows()`, which is every `.category-row` in the `tbody`.
  - This page does have a search box (`list_header`,
    `filterTarget "#categories-table .category-row"`). But its filter,
    `web/public/record-dialog.js:542-556` `applyFilter`, only sets
    `r.hidden`. Filtered-out rows stay in the DOM, so `persistOrder()` still
    posts them. No gap.

So the tie cannot be reached through the shipped UI. The repo-level fix is
defence in depth, which the new doc comment on `SetCategorySortOrder` says
accurately.

### Diff correctness

- The `SELECT` runs on `tx` before the `UPDATE` loop. It fully reads `rows`
  and then closes them (explicit `Close()`, plus a close on the `Scan`-error
  path) before `tx.PrepareContext`, so the transaction's single connection is
  free again. The `rows.Err()` early return comes after `Next()` has returned
  false, and `database/sql` has already auto-closed the rows at that point.
  No leak.
- **Full-list callers are unchanged.** `missing` is empty, so `full` holds
  the same ids in the same order as `orderedIDs`. The existing
  `TestSetCategorySortOrder` pins the exact integers 0..2 for a full post, and
  it still passes.
- **Unknown posted ids** still hit a no-op `UPDATE`, as they did before. The
  worst result is a gap in the numbering, never a tie.

### F1: the new test was a false pass. Fixed (Medium: the TDD claim did not hold)

Dev's test created Drinks, Snacks and Bakery. `CreateCategory` assigns
`MAX(sort_order)+1`, so they got `sort_order` 0, 1 and 2. The test then
deactivated Bakery and posted `[Snacks, Drinks]`. The unfixed code writes 0
and 1 and leaves Bakery at 2, so nothing ties, and Bakery still sorts last.
The test's comment ("it would collide with Drinks's new sort_order=2") was
wrong.

The reviewer ran it against the reverted `catalog_repo.go`:

```
=== RUN   TestSetCategorySortOrder_OmittedRowGetsAppendedNotTied
--- PASS: TestSetCategorySortOrder_OmittedRowGetsAppendedNotTied (0.00s)
```

A tie needs the omitted row's old value to fall inside `0..len(posted)-1`.

**Fix:** the test now creates Drinks, Snacks, Bakery and Sweets (0..3),
deactivates Drinks and Snacks (0 and 1), and posts `[Sweets, Bakery]`. It
checks two things:

- no `sort_order` is shared;
- the exact final order and integers are Sweets=0, Bakery=1, Drinks=2,
  Snacks=3. This pins "posted first, then omitted rows appended in their
  existing relative order".

The comment on the test now explains why the omitted rows must be the ones
created first.

### TDD proof (re-run by the reviewer on the corrected test)

With the fix reverted (`git checkout HEAD~1 -- internal/data/catalog_repo.go`):

```
=== RUN   TestSetCategorySortOrder_OmittedRowGetsAppendedNotTied
    catalog_repo_categories_test.go:250: sort_order 0 tied between 804e9d9f-… and db92b4cb-…:
    [{… Name:Drinks … SortOrder:0 … IsActive:false} {… Name:Sweets … SortOrder:0 … IsActive:true}
     {… Name:Bakery … SortOrder:1 … IsActive:true} {… Name:Snacks … SortOrder:1 … IsActive:false}]
--- FAIL: TestSetCategorySortOrder_OmittedRowGetsAppendedNotTied (0.00s)
```

With the fix restored:

```
--- PASS: TestSetCategorySortOrder (0.00s)
--- PASS: TestSetCategorySortOrder_OmittedRowGetsAppendedNotTied (0.00s)
```

### Gate (re-run by the reviewer on the final tree)

- `go build ./...`: clean.
- `go vet ./internal/data/...`: clean.
- `gofmt -l` on both touched files: empty.
- `golangci-lint run ./internal/data/...`: 0 issues.
- `scripts/ci/guard-data-access.sh`: "no inline SQL outside internal/data /
  internal/db".
- `go test -count=1 ./internal/data/... ./internal/pages/...`: all ok
  (`internal/data` 144.6s, `internal/pages` 127.6s, plus `catalog`,
  `common`, `itemsnav` and `settingsnav`).

The full `go test ./...` run was not repeated. Dev reported it green, and the
reviewer's change touches only a `_test.go` file in `internal/data`.

### Beyond automated tests

- **Repository pattern.** The new `SELECT` is inside `catalog_repo.go`, and
  the guard passes.
- **Money.** Not applicable: no money fields are touched.
- **i18n.** No new user-facing strings. The diff adds only Go comments and a
  wrapped error prefix that already existed.
- **Offline-first.** The change is one extra local SQLite read inside the
  existing transaction. Nothing new touches the network.
- **No real shop or client names and no secrets.** Test data is "Drinks",
  "Snacks", "Bakery" and "Sweets".
- **UI surface, help and screenshots.** Both current callers already post
  the full list, so no visible behaviour changes. `web/help/en/categories.md`
  and `till-designer.md` describe reordering with the up/down arrows and are
  still accurate. No help topic update is needed, and there is nothing to
  regenerate in screenshots.

## Deferred / not this card

- Observation only, not a data defect: on `/categories` with a search
  active, a row's move button swaps with its DOM neighbour even when that
  neighbour is hidden by the filter. `refreshEdgeStates()` also ignores
  `hidden`. The posted order stays complete and tie-free, so this is at most a
  "the tap looked like it did nothing" UX quirk. It predates this card.

## Verdict

**Safe to merge after the reviewer's F1 test fix.** The production change is
correct and minimal, and the regression test now demonstrably fails without
it.
