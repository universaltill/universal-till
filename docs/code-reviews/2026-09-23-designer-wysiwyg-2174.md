# 2026-09-23: Quick Buttons Designer as a live sale-screen replica (ut-docs#2174)

## What shipped

`/designer` used to be a flat admin page: a list of buttons, each with
▲/▼/✕ controls. It is now a live replica of the sale screen's real product
panel, edited in place, and it can manage categories in place too.

- **Replica.** `designer.html` renders the same self-refreshing `.products`
  placeholder that `index.html` renders. It fetches the same `buttons.html`
  fragment, with `hx-vals='{"mode":"edit"}'`. `hx-get` stays exactly
  `/ui/buttons`, because app.js's `utTileJiggle` unsaved-drag guard matches
  that literal.
  - `buttons_api.go` returns 403 for `?mode=edit` without
    `catalog_management`.
  - `ui.ButtonsHTTP.EditMode` / `ButtonVM.Editing` (`stampEditing`) render
    the tiles inert: no `/api/pos/scan`, no modifier-picker wiring. They keep
    `data-code` and `data-pos`, so jiggle-mode reorder works unchanged.
  - Edit mode drops the sale-screen search, the link to `/designer`, the
    All tab, the Categories tab and the plugin action strip.
  - The edit badge on a tile returns to `/designer`.
- **Category CRUD in place.** New `designer_categories_api.go` has four
  routes:
  - `POST /api/designer/categories` (create);
  - `POST /api/designer/categories/{id}` (rename and recolour);
  - `POST /api/designer/categories/{id}/active`;
  - `POST /api/designer/categories/reorder`.

  They are thin wrappers over the existing `CatalogRepo` methods and contain
  no SQL. Each is gated on `catalog_management` and on this till being the
  primary. On success they return 204 with `HX-Trigger: buttons-changed`. On
  refusal they return a localized, escaped `text/html` fragment that is
  swapped into the row's aria-live message. The management list
  (`buttons.html` "designer-categories") shows every category, active or
  inactive, with or without buttons.
- **Retired grid removed.** The flat admin grid is gone: the
  `buttons_admin_grid` template, its reorder script and
  `#buttons-grid-wrap`. `/api/buttons/{add,remove}` now return an empty 200
  plus `HX-Trigger`. Their elevation retry target is `#buttons-add-error`.
- **Other changes in the branch.**
  - i18n: 5 new keys and 2 reworded keys in en/ar/fa/tr.
  - Help: `till-designer.md` rewritten in en/ar/de/fa/tr, so the four
    help-drift baseline entries for that topic are removed.
  - Screenshots regenerated.
  - Two obsolete e2e specs (1221, 1354) replaced by
    `designer-wysiwyg-2174` and `designer-narrow-category-list-2174`.

Out of scope (separate cards, not flagged): cross-category button move
(ut-docs#2465), in-place catalog-item creation (#2466), and real-tablet
hardware verification (#2467). Reorder and tap were only emulated, never
touch-hardware-verified.

## Independent review

Fable built the change and a Sonnet Tester tested it. This review was done by
Opus in an isolated worktree, reset to the WIP snapshot `4875a54` on
`fix/2174-designer-wysiwyg-replica`.

### Gate (re-run by the reviewer on the final tree, after the reviewer's fixes)

- `gofmt -l .`: empty. `go build ./...`: clean. `go vet ./...`: clean.
- `golangci-lint run ./...`: 0 issues.
- `go test -count=1 ./...`: all 60 packages with tests pass, 0 FAIL.
  - The reviewer's first full run, on the unmodified snapshot, reported
    `FAIL internal/pages`. The output filter hid the test name.
  - A second run of `internal/pages` on its own, and then the final full run
    above, were both green. The failure did not reproduce.
  - Playwright was running at the same time as that first run. The reviewer
    records it as an unreproduced flake, not a defect of this diff.
- Every `bash scripts/ci/*.sh` step in `ci.yml`'s `build` job (59 scripts)
  passes except one: `guard-shellcheck-version.sh`. It fails only because the
  `shellcheck` binary is not installed in the review container, and the diff
  touches no `.sh` file. The passing scripts include data-access, i18n (and
  all 6 of its self-tests), kiosk-engine, help-topics, help-drift,
  compliance-claims, competitor-naming, docs-shots and page-http-error.

### TDD claims re-derived by the reviewer

For each row, the reviewer broke only that code, ran the named test, then
restored the code and ran it again.

| Broken | Test | Result while broken |
|---|---|---|
| `canPerform(…"catalog_management")` in `designer_categories_api.go`'s `gate` | `TestDesignerCategoriesAPI_CatalogManagementGate` | FAIL on all 4 subtests: "cashier create/update/active/reorder = 204, want 403" |
| `editMode && !granted` 403 in `buttons_api.go` | `TestButtonsPartial_EditMode` | FAIL: "cashier /ui/buttons?mode=edit = 200, want 403" |
| `@media (max-width: 480px)` `.designer-cat` block in `app.css` | `e2e designer-narrow-category-list-2174` | FAIL: "category name column width, Expected > 100, Received 26.53125" |
| the reviewer's own F1/F2 fix (pre-fix `buttons.html`) | `TestButtonsPartial_EditMode` (new assertions) | FAIL: "edit-mode fragment must not contain `id=\"search-results\"`" |

Every test passed again once its code was restored.

### E2E (run by the reviewer in Chromium)

- `designer-wysiwyg-2174`, `designer-narrow-category-list-2174`,
  `designer-search` (4 tests), `codeless-item-shortcut-1459` and
  `sell-tile-jiggle-mode-2339`: **11 passed**, after the fixes below. Before
  them, 3 tests failed (see F1 and F3).
- Every other spec that touches `/designer` or `/api/buttons/{add,remove}`:
  `tab-bar-overflow-aria-424`, `sell-tile-jiggle-done-focus-2417`,
  `sell-tile-jiggle-mode-locked-cashier-2312`,
  `sale-screen-category-strip-overflow-2307`,
  `sale-screen-search-strip-2173`, `sell-screen-categories-tab-2283`,
  `order-type-prompt-placement-2282` and
  `category-switch-stale-tile-add-1433`: **37 passed**.

### F1: duplicate `#search-results` on /designer. Fixed (Medium: broke 2 existing e2e specs)

Edit mode dropped the sale-screen search input but still rendered its results
container, `<div id="search-results" x-show="q">`. `/designer` already has its
own `#search-results`, the add-a-button dropdown in `buttons_admin.html`, so
the page had two elements with the same id.

`designer-search.spec.ts`'s two "returns results" tests failed with a
Playwright strict-mode violation ("resolved to 2 elements"). The Dev/Tester
hand-off did not run these specs. The app itself mostly still worked, because
htmx resolves the first match, but the page was invalid HTML and fragile.

**Fix:** `buttons.html` renders that container only when `not .EditMode`.

### F2: a sale-screen search leaked into the replica. Fixed (Medium: edit-mode state leak)

`restoreGridState()` restores `window.utSaleGridState.q`. That object is a
window global, and it survives the shell's boosted navigation. Two things
happened after a cashier or manager left a search open on the sale screen and
then navigated to `/designer`:

- the replica came up with `q` set, `#buttons-grid` hidden (`x-show="!q"`) and
  the tab bar hidden;
- there was no search input to clear it. Only the back arrow was left.

This is the "edit-mode state leaks between surfaces" class the review brief
asked about, in the other direction from the one it named.

**Fix:** edit mode never restores `q`. The tab is still restored, which is
harmless and useful. Both F1 and F2 now have regression assertions in
`TestButtonsPartial_EditMode`:

- the edit fragment must not contain `id="search-results"` or
  `this.q = st.q`;
- the plain sale-screen fragment still must contain both.

### F3: two existing e2e specs still targeted the retired grid. Fixed (Medium: broken tests)

`designer-search.spec.ts` ("tap-to-add from a touch context") and
`codeless-item-shortcut-1459.spec.ts` still located tiles by
`#buttons-grid-admin .tile-name` / `.reorderable-tile`. 1459 failed:
"toHaveCount … resolved to 0 elements".

**Fix:** both specs now use `[data-testid="designer-tile"]`. Each first waits
for the replica's `[data-testid="designer-categories"]` to render, so the
"before" count is taken after the `hx-trigger="load"` fetch.

### F4: dead CSS for the retired grid. Fixed (Low)

`app.css` still had the `.reorderable-tile .btn-actions` rules (#1221) and the
`#buttons-grid-admin` column floor (#1354). Nothing matches either selector
now, so both were removed with their comments.

### F5: UX-spec deviation. Accepted, flagged to the product owner (not a merge blocker)

The UX spec on the card (comment of 2026-09-23 13:59) asked for category
editing **on the strip itself**:

- an "Edit categories" toggle;
- a pencil on each tab;
- a `+` tab;
- an inline popover anchored to the tab;
- Arrow-key reorder of tabs.

What shipped is a management **list below the replica**, with move-up/down
buttons, an inline form and a radio swatch picker.

The list does have a real justification. Categories with no quick buttons
and inactive categories never appear on the strip, because the sale screen
prunes them, so a strip-only UI could never create or reactivate them. The
list is also fully keyboard-reachable and labelled. The cost is that at
1024×600 the category controls sit below the fold, under the tile grid, and
are less discoverable than the spec intended.

This is a product/UX call, not a correctness defect. Proposed follow-up card:
*"Designer: add strip-anchored category edit affordances (pencil/`+` tab,
popover) per the ut-docs#2174 UX spec, keeping the list for
empty/inactive categories."*

### F6: `/categories` still gates on `settings`. Accepted, documented

The designer routes gate on `catalog_management`, deliberately, and say so in
their file comment. The reviewer agrees with the choice:

- categories are catalog data;
- every mutating `/api/catalog/*` route and `/designer` itself already use
  `catalog_management`;
- "one page, one permission model" is right.

Both actions are seeded to the same three roles (migration 033 and
`001_init`), so today nobody gains or loses access. The inconsistency only
bites if an operator edits role permissions so that the two actions diverge.
Proposed follow-up card: *"Align /categories (categories_page.go) to
catalog_management so both category surfaces share one gate."*

### Other findings (not fixed, low)

- **Reorder validation.** `/api/designer/categories/reorder` does not check
  that the posted ids are complete or exist. Unknown ids are silent no-ops,
  and a partial list re-numbers only the listed rows. This is identical to the
  pre-existing `/api/categories/reorder`, so it is not a regression.
  Parity with the sibling handlers was otherwise checked:
  - blank or whitespace name is refused;
  - colour is checked against the palette allowlist (`ValidItemColor`);
  - an unknown id returns 404 (`ErrCategoryNotFound`);
  - the primary-till check runs first;
  - audit rows are written.

  Neither sibling nor new handler has a name-length cap. There was no
  copy-paste-without-adapting: the dialog-only group/station link handling is
  correctly absent, and `UpdateCategory` does not touch the links.
- **Stale comments.** Comments still name the retired grid in
  `app.js:1668`, `basket.html:264` and `pos_api.go:1130`. The reviewer left
  them alone to keep `app.js` byte-identical ("jiggle mode reused
  unchanged") and to avoid another surface-hash churn.
- **Elevation retry target on the sale screen.** From the sale-screen jiggle
  badge, the elevation retry target `#buttons-add-error` does not exist. That
  was already true of the old `#buttons-grid-wrap`, it is documented
  in-code, and the `HX-Trigger` still refreshes the grid.

## Confirmed clean

- **Defense in depth for `?mode=edit`.** The route returns 403 without
  `catalog_management`. Even if the fragment somehow reached a
  non-manager, it is harmless:
  - its tiles carry no sale wiring;
  - every control posts to a route that re-checks `catalog_management`
    server-side;
  - the jiggle badges hit `/api/buttons/*`, which uses
    `checkOrElevate(catalog_management)`.
- **No leak from the non-edit path.** `EditMode` defaults to false, so
  `stampEditing(false)` leaves `Editing` false on every button, and
  `AdminCategories`/`ItemColors` stay nil. The management list is loaded only
  in edit mode. `TestButtonsPartial_EditMode` pins that the plain fragment
  keeps `/api/pos/scan`, `href="/designer"`, `return=/`, and has no
  `designer-categories` or edit `hx-vals`.
- **`ErrCategoryHasItems` is surfaced on the new route.** It returns 409 with
  the localized `categories.error.deactivate_blocked` text and the count,
  swapped into the row's aria-live message. The row is unchanged and there is
  no `HX-Trigger`. `TestDesignerCategoriesAPI_Active` covers this. The row
  also warns up front (`designer-cat-blocked-<id>`).
- **Colour reaches CSS only after allowlisting.** It is checked on write and
  goes through html/template's CSS-context escaping on read.
- **Repository pattern respected.** There is no SQL outside `internal/data`;
  only the tests seed with raw SQL, as elsewhere. `guard-data-access` passes.
- **File paths.** The diff adds no file writes (so no missing `os.MkdirAll`)
  and no new cwd-relative data paths. The only `filepath.Join("web", …)` uses
  are the pre-existing template-renderer pattern, and the diff removes two of
  them.
- **i18n.** Every new or changed key is present in en/ar/fa/tr, and the
  ar/fa/tr strings are real translations, not English copies. de/es are
  language-pack repos under CLAUDE.md, so follow-up PRs are needed in
  `ut-plugin-language-{de,es}` for the 5 new keys.
- **RTL.** The new CSS has no physical `left`/`right`/`margin-*`/`padding-*`;
  it uses logical properties only.
- **Help and screenshots.** `till-designer.md` is coherent with the new
  behaviour in all five locales. The screenshots and `manifest.json` topic
  hashes were regenerated, and the new en screenshot, viewed at 1024×600,
  shows the replica. The reviewer's pixel-neutral fixes refreshed only
  `surface_sha256` (`update-docs-shots-surface-hash.sh`).
- **No real shop names or secrets in the diff.** Test data uses generic
  names: "Drinks", "Snacks", "Wysiwyg2174 Item …".

## Deferred / not this card

- F5 (strip-anchored category editing per the UX spec): new Backlog card.
- F6 (`/categories` → `catalog_management`): new Backlog card.
- de/es lang-pack PRs for the 5 new `designer.*` keys, per the standing
  CLAUDE.md rule.
- `web/ui/partials/tile_sheet.html`, orphaned since #2339 per the BA
  comment: not touched here.
- ut-docs#2465 / #2466 / #2467, as scoped.

## Verdict

**Safe to merge after the reviewer's fixes (F1–F4).** No blocking findings
remain. F5 is a product-owner-visible UX deviation to acknowledge in the PR
description, and a follow-up card is proposed for it rather than holding this
merge.
