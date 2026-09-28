# Review — All-grid edit mode under category chips (ut-docs#2534)

Date: 2026-09-28 · Lane: cloud-24 · Built by Opus 5.5 · Reviewed by Fable (independent, different model)

## What shipped

In the **All items with category filters** sell-screen layout (`all_filter_chips`), press-and-hold on a tile now enters edit (jiggle) mode:

- **With a category chip selected**, the grid shows that category in its quick-button order, the same order the category layouts use. Tiles show the pencil only, and can be dragged or moved with the arrow keys within the category. Done/Escape/a tap outside saves once through `POST /api/buttons/reorder` with `scope=subset`. `ButtonStore.UpdateOrderSubset` re-deals the posted codes into the global slots they already hold, so other categories and hidden items keep their positions.
- **Plain All** (alphabetical) arms edit mode with the pencil only. Nothing moves and nothing is saved.
- Switching chips while editing stays in edit mode, and saves any unsaved move first.
- **Cashier** (no `catalog_management`): the grid has no `data-edit-allowed`, so press-and-hold is an ordinary tap. The server gate is the existing `checkOrElevate`: without a manager PIN nothing is saved, and the PIN retry carries `scope`.
- The edit bar reads `buttons.edit_mode.title_all_grid` in this layout. This is a new key in en/ar/fa/tr; the de/es language packs follow.
- The help page `web/help/*/sell.md` is updated in en/de/fa/ar/tr.

## Findings (Fable review, round 1) and outcome

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | After Done under a chip, the live-refresh watcher (`sell-screen-watch.js`) saw the version bump from this till's own save, re-rendered the grid and dropped the chip back to plain All. | **Fixed.** The reorder response carries `X-UT-Sell-Version`. `utSellScreenWatch.ownWrite()` holds refreshes while the save is in flight and adopts the returned version. Go test + e2e (waits past two real polls). |
| 2 | minor | `data-pos` on filtered tiles skipped implicit hidden items, so it didn't match `Load()`'s index; the comment claimed it did. | **Fixed.** `Pos` is computed from the hidden-kept load, and the docs are corrected. Unit test with a hidden item, including its slot surviving a subset save. |
| 3 | minor | Help said a cashier's hold "does nothing" (it rings the item up), implied plain All doesn't wobble, and left the strip bullet saying edit mode is strip-only. | **Fixed** in all five locales. |
| 4 | minor | A subset save loaded the catalog twice. | **Fixed** with `updateOrderWith`. Behaviour is unchanged and the existing tests are green. |
| 5 | minor | Test gaps (hidden item, watcher refresh). | **Fixed** by the tests under 1 and 2. |
| 6 | nit | Plain-All touch panning relied on CSS `:has()`. | **Fixed**: a JS-toggled `all-grid-movable` class, with pan-able as the safe default. |
| 7 | nit | A double chip tap during an in-flight save can re-select the first chip. | **Accepted.** It is cosmetic and converges. |

Tester/UX finding: the edit bar said "Editing quick buttons — drag a tile to reorder" in plain All. It is fixed by the new title key above.

## Verified beyond unit tests

- The reviewer re-verified TDD for every new Go test: with the production code reverted to a naive stub, each failed for real reasons, then passed once restored.
- Driven run (headless Chromium, real till binary + demo seed):
  - screenshots viewed at 1024×600 and 360×740, in en/de/fa (RTL)/ar (RTL), in light and dark;
  - the pencil sits at the leading corner (mirrored in RTL), Done is never clipped, and the chips stay reachable.
- Emulated touch (not real hardware or WebKitGTK):
  - plain All still scrolls by touch while in edit mode;
  - a touch drag under a chip reorders and saves with `scope=subset` → 204.
- e2e: `sell-all-grid-jiggle-2534` (4), `sell-tile-jiggle-mode-2339`, `sell-screen-browsing-mode-2499`, `sell-screen-live-refresh-2765` and `sell-stale-tile-2525` all pass; the auth project `sell-all-grid-jiggle-locked-cashier-2534` passes.
- Gate: gofmt, build, vet, `go test ./...`, golangci-lint (0), shellcheck, and every `build`-job guard.

## Not verified / deferred

- Real touch hardware / the WebKitGTK kiosk.
- A category with more than 200 items: only the loaded page is posted, and the rest keep their slots by design.
- At 360 px the sticky edit bar can cover the first chip row while scrolled. This is pre-existing panel height behaviour.
- #3089 (a refresh from elsewhere resets the chip) stays its own card.

**Verdict:** safe to merge.
