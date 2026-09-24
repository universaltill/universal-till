# 2026-09-24 — Sell-screen browsing mode (ut-docs#2499)

Reviewer: independent different-model review (Opus). Code built by Fable
(Dev) and verified by Tester in the same worktree. Branch
`feat/2499-sale-browsing-mode`.

## What shipped

- A new setting, `sale.browsing_mode`, with three values: `category_tabs`
  (the default), `all_filter_chips` and `strip_overflow`. It lives in
  `common.RuntimeState.BrowsingMode`. `LoadState`/`SaveState` pass it
  through `ClampBrowsingMode`. Three things reject a value outside the enum
  with a 400 or an error: the dedicated handler
  (`POST /api/settings/browsing-mode`, manager-gated and elevation-wired),
  the generic settings upsert, and the cloud `set_till_setting` hook.
- Two legacy booleans are fully removed: `sale.show_all_tab` (ut-docs#2294)
  and `sell_screen_categories_tab_enabled` (ut-docs#2283). This covers:
  - their handlers (`/api/settings/show-all-tab`, `/api/settings/categories-tab`)
  - `data.SellScreenCategoriesTabKey` and `internal/data/sell_screen_settings.go`
  - `ButtonStore.CategoriesTabEnabled`
  - the settings cards and the `settings-categories-tab` nav entry
  - 7 locale keys in each of en/ar/fa/tr
  - their tests

  A leftover DB row under either key is ignored.
- The three sell-screen shapes are in `web/ui/partials/buttons.html`:
  - **Category tiles.** Each tile opens `#category-items-modal`, whose body
    comes from the new `GET /ui/buttons/category?id=` (`ButtonsHTTP.CategoryItems`).
    The popup lists every active item in the category's subtree: quick buttons
    first in Designer order, then the rest A–Z. It has its own client-side
    search. This absorbs ut-docs#2372.
  - **All grid with filter chips.** Chips call `/ui/buttons/all/more?category=`
    to filter on the server, and paging stays inside the chosen filter.
  - **Strip with "…" overflow.** Unchanged.
- The Designer replica (EditMode) always uses the strip.
- The help topic `sell` has a new section, "Choosing how items are browsed"
  (en/de/ar/fa/tr), and the screenshots were regenerated.
- New e2e spec `sell-screen-browsing-mode-2499.spec.ts`. Worker tills now
  boot in `strip_overflow`, so the existing sell-screen specs keep testing
  the shape they were written for.

Two design deviations were accepted upstream and not re-litigated here. I
confirmed the code matches both:

1. "Category tabs" are tiles that open a popup, not a persistent tab bar.
2. The new default changes how an existing till looks after upgrade. The
   default is `DefaultBrowsingMode = category_tabs`, and a missing row
   clamps to it.

## Findings

| # | Severity | Finding | Resolution |
|---|---|---|---|
| 1 | **High** | The branch was one merge behind `origin/main`. It did not include ut-docs#2498 (PR #1333), which rewrote the same code: `BuildCategoryGroups` gained an `itemCounts` parameter and a `HasButtons` flag, the Categories-tab picker got an empty state, and `buttons_categories_tab_test.go` changed. The PR would have conflicted in `buttons.go`, `buttons.html`, the 4 `sell.png` files and `manifest.json`, plus a modify/delete conflict on the test file. | **Fixed.** Merged `origin/main` into the branch. Kept #2498's strip-mode behaviour: a category with items but no quick buttons still gets a tab with an empty-state message, and the default tab prefers a category that has buttons. Dropped #2498's changes to the retired Categories-tab picker. `BuildCategoryTiles` now calls `BuildCategoryGroups(allActive, cats, nil)`. That is correct, because the "buttons" passed in there are already every active item. |
| 2 | Medium | The merge would have silently lost #2498's strip-mode tests, because this branch deletes the file they lived in. | **Fixed.** Ported them to `internal/ui/buttons_category_item_only_test.go` (items-only category still gets a tab and an empty state, nested empty subcategory shows one message, default tab prefers a category with buttons) and dropped only the retired Categories-tab assertions. |
| 3 | Medium | After the merge, `TestButtonsHTTPList_StripOverflowModeIsTodaysStrip` failed. It asserted that an items-only category (Household) gets **no** strip tab, which is the opposite of #2498's intended behaviour on `main`. | **Fixed.** Flipped the assertion to require `cat-tab-cat_house`, with a comment citing #2498. |
| 4 | Medium | After the merge, the new help text was wrong in all 5 locales. The strip-mode bullet said "one tab per category that has a quick button", but since #2498 a category with items and no quick buttons also gets a tab, with a note. | **Fixed** in en/de/tr/ar/fa. Help-drift structure is unchanged and the guard passes. |
| 5 | Low | The popup body was cleared on open and then filled by htmx. If the fetch was slow or failed, the popup stayed blank with no message, which breaks the ux-guidelines rule on error/loading states. | **Fixed.** The tile now carries `data-loading-text` (`common.loading`) and `data-error-text` (`common.error.server`), both existing keys, so no new locale key. `openCategoryPopup` shows the loading line, and `hx-on::response-error`/`send-error` replace it with the error line. Added `TestButtonsHTTPList_CategoryTilePopupHasLoadingAndErrorStates`: it fails with the markup reverted and passes with it restored. |
| 6 | Low | Some comments still described retired code: the `openCategoryPicker()` clone in the jiggle comment, `settings.sale.show_all_tab` as a live toggle, and a test comment pointing at the deleted test file. | **Fixed.** Remaining history-only mentions (e.g. "replaced #2283's openCategoryPicker") are accurate and were left alone. |
| 7 | Medium (cross-repo) | ut-cloud still allowlists and labels `sell_screen_categories_tab_enabled` and does not know `sale.browsing_mode`. This fails closed: the POS refuses the old key and the portal filters out the new one. | **Deferred.** Filed **ut-docs#2553**. Out of scope for this repo. |
| 8 | Info | The new `/ui/buttons/category` handler builds template paths with `filepath.Join("web", "ui", …)`, relative to the release tree. | **Accepted.** These are read-only shipped templates, and every existing handler in `buttons_api.go` uses the same pattern. This is not the `paths.Data(...)` bug class (that concerns mutable data). The diff adds no file writes, so the `os.MkdirAll` class does not apply. |
| 9 | Info | In tile and chip modes, long-press reorder is not available, although the card title says "jiggle edit … in every mode". | **Accepted**, per the upstream BA decision (AC 6: category/All-grid reorder is a separate card). The help text says the press-and-hold edit mode applies to the strip. |

Checked and clean:
- Raw SQL: the only change under `internal/data` is a deleted file. `guard-data-access` passes.
- Money: none touched.
- Hardcoded strings: none. Every new string goes through `T`, including inline script, which uses `var T` and `data-*` attributes.
- RTL: the new CSS uses only logical properties (`inset-inline`, `margin-inline`, `inline-size`, `margin-block-end`).
- Modal blockers: none on kiosk or checkout. The popup is a non-modal `.show()` dialog on the cashier sell screen, with a Close button.
- Auth exemption: `/ui/buttons/category` is not auth-exempt (`internal/auth/middleware.go` exempts only `/self-order*`).
- Empty states: the chip filter, the popup, and "no tiles" all have one.
- Seed/test data: no real client or shop names, no literal secrets.

## Gate (run by the reviewer, post-merge, post-fix)

- `gofmt -l .`: clean
- `go build ./...`, `go vet ./...`: pass
- `go test ./...`: all packages pass
- `golangci-lint run ./...`: 0 issues
- Guards, all pass: `guard-i18n`, `guard-data-access`, `guard-help-topics`, `guard-help-drift`, `guard-competitor-naming`, `guard-compliance-claims`, `guard-kiosk-engine`, `guard-docs-shots`, `guard-deadcode-baseline`, `guard-page-http-error`, `guard-htmx-loaded`, `guard-osk-loaded`, `guard-autofill-suppression`, `guard-e2e-fixtures-import`, `guard-migration-version-collision`, `guard-plugin-menu-read`, `guard-price-history-sync`, `guard-plugin-settings-bump`, `guard-emoji-font`
- `shellcheck`: not installed in this session. The branch changes no `.sh` files.
- e2e (Chromium, `default` project): the 2499 spec plus 11 regression specs, 56/56 passed. The regression specs cover strip overflow, tabs+search, search strip, jiggle ×3, tab-bar ARIA, categories dialog, shop-wide modifiers, designer WYSIWYG and category colour.

## TDD claims re-verified personally

For each: revert the change, run the test and confirm a real failure, then restore and confirm it passes. Each was done inline in one turn.

1. **Chip filter** (`AllMore ?category=`). Reverted `filterButtonsInCategory`. `TestButtonsHTTPAllMore_CategoryFilter` failed ("did not expect Cola"). Restored: pass.
2. **Popup ordering** (`quickButtonsFirst`). Reverted to alphabetical-only. `TestButtonsHTTPCategoryItems_QuickButtonOrderBeatsAlphabetical` failed (the quick button was not first). Restored: pass.
3. **Generic upsert validation.** Disabled the enum check. `TestBrowsingModeWiredIntoRawUpsert` failed ("= 204, want 400"). Restored: pass.
4. **Tester's CSS fix** (`.category-items-modal` position/z-index). Removed the rule. The e2e test "popup stacks above page content at phone width" failed with the real error: "point (22,799) inside the open popup resolved to `<FOOTER class="statusbar">`". Restored: pass.

## Manual

- `web/help/{en,de,ar,fa,tr}/sell.md` were read against the rendered UI. The setting's labels match the prose ("How items are browsed", "Category tiles", "All items with category filters", "Category strip with quick buttons").
- Screenshots: I ran `make docs-shots` myself after the merge (124 passed). The regenerated `sell.png` files match Dev's byte for byte, which shows the run is deterministic. They differ from `origin/main` and show the new default category-tile grid. `till-designer.png` was also refreshed, picking up #2498's topic change.
- The final `surface_sha256` differs from `origin/main`'s. My later loading/error-state edit changes nothing on screen at rest (the loading line only appears while the popup is fetching), so after it I refreshed only the hash, with `update-docs-shots-surface-hash.sh`.

## Not verified

- Dark or alternate theme.
- Real touch hardware (only Chromium synthetic input was used).
- A driven de/tr long-locale check of the settings select. The labels are short, and Tester drove en/ar/fa.

## Verdict

**Safe to merge**, in this order:

1. This PR adds new `en.json` keys (`settings.sell_screen.browsing_mode*`, `elevation.summary.browsing_mode`) and deletes 7. Merge core first; `lang-pack-drift` on `main` is expected to go red.
2. In the same cycle, land the `ut-plugin-language-{de,es}` pack PRs.
3. Then land the ut-cloud follow-up, ut-docs#2553.
