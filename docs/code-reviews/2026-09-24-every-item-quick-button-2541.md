# Review: every item is a quick button by default; tile hide/delete (ut-docs#2541)

Date: 2026-09-24 · Branch `feat/2541-every-item-quick-button` · Built by a Sonnet
dev subagent, reviewed by an independent Opus subagent (lane:cloud-54).

## What shipped

- **Auto quick buttons, computed when read.** `ui.ButtonStore.Load`/`LoadWith`
  returns the explicit `shortcut_buttons` rows (in `sort_order`), then every
  other active, non-hidden item, built from `LoadAllActive`'s existing
  code/price/thumbnail logic. Nothing is backfilled, so items from every
  creation path are covered: till create, CSV/backup import, cloud directives,
  demo seed and admin sync. Deleted or deactivated items drop off on their own.
- **Migration 039** (renumbered from 038 after #2535 took it) adds `items.sell_screen_hidden` (NOT NULL DEFAULT 0), with
  its checksum pinned. Admin sync copies `SELECT *`, so the flag reaches
  satellites. No items upsert path resets it.
- **Tile actions (jiggle edit mode only).**
  - Edit is unchanged.
  - The trash badge now deletes the *item* via `POST /api/buttons/delete-item`
    (`pos.DeactivateItem`), after a confirm that names it.
  - A new eye-off Hide badge sits at the inline-end/bottom corner and calls
    `POST /api/buttons/hide`.
  - `/api/buttons/remove` now means hide.
  - The Designer lists "Hidden from sell screen (N)" with an Unhide button for
    each item (`POST /api/buttons/unhide`). Adding an item as a button also
    unhides it.
  - All the new routes use the same gates as `/remove`: `requirePrimary`,
    `checkOrElevate("catalog_management")`, the audit record and
    `HX-Trigger: buttons-changed`.
- **Reorder** saves an implicit tile as a real row only up to the last position
  the drag changed, in one transaction. Saved rows store an empty label and a
  NULL image, so the tile keeps showing the item's live name and thumbnail
  (`COALESCE(NULLIF(sb.label,''), i.name)`). The scan resolver was fixed to
  match.
- Hidden items still sell: search (`SearchSellable`) and scanning are not
  filtered.
- **Other changes:**
  - New keys in en/ar/fa/tr; the de/es language packs are updated in
    follow-up PRs.
  - Help topics `sell` and `till-designer` updated in all locales, and the
    screenshots regenerated.
  - New e2e `sell-tile-hide-delete-2541.spec.ts`.
  - `codeless-item-shortcut-1459.spec.ts` now covers "an imported item is a
    tile with no manual step" and checks that adding it doesn't duplicate it.

## Review findings (Opus, round 1)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | The first drag saved a row for **every** implicit item: thousands of statements outside a transaction, frozen labels and images, and the whole catalog in the cloud heartbeat | Fixed: only touched tiles are saved, in one transaction, and they read the live name/image. Tests `TestButtonStoreUpdateOrder_OnlyMaterializesTouchedTiles`, `…MaterializedRowShowsLiveItemName` and `TestResolveShortcutLine_MaterializedRowFallsBackToLiveItemName` |
| 2 | major | `List` loaded the whole active catalog twice per render | Fixed: `LoadWith` reuses a single `LoadAllActive`. Test `TestButtonsHTTPList_LoadAllActiveRunsOnceNotTwice` (checked that it fails before the fix) |
| 3 | major | The approval prompt for delete-item said "remove quick button <uuid>" | Fixed: new `elevation.summary.buttons_{hide,unhide,delete_item}` keys, filled with the item name |
| 4 | minor | An unknown or inactive item id returned 200 and was audited | Fixed: `data.ErrItemNotFound` leads to a localized 400. Tests at repo and HTTP level |
| 5 | minor | Hidden items still counted toward sale-screen category pruning | Fixed: new `CategoryAdminRow.VisibleItemCount` (the admin `ItemCount` is unchanged) |
| 6 | minor | Hide and delete on a variant tile act on the whole item | Accepted by design and now documented in the help (`sell`, `till-designer` step 4) |
| 7 | minor | No satellite tests for the new routes | Added `TestButtonsAPI_HideUnhideDeleteItemRefusedOnReplica` |
| 8 | minor | The cloud quick-button panel and heartbeat report see only explicit rows | Deferred to Backlog card ut-docs#2562 |
| nit | | `testsupport` schema was missing the column | Fixed |
| nit | | A locked Hide/Delete on the sale screen targets `#buttons-add-error`, which doesn't exist there | This existed before for `/remove`. Accepted, since the elevation prompt still shows |

The reviewer re-checked the TDD claims by reverting the code in a worktree:
- Taking out the `LoadAllActive` merge fails 4 `TestButtonStoreLoad_*` tests.
- Taking out the dedupe fails 6.
- Replacing `checkOrElevate` on `/hide` fails the gate test.

## Verified beyond unit tests

- Real driven run in Chromium at the default e2e viewport (1280×720):
  - Imported items appear as tiles.
  - The three badges don't overlap, and hide sits at the bottom-end, below
    delete (geometric assertion plus a screenshot, which I read).
  - Hide removes the tile, and the item still sells via search.
  - The Designer's hidden list, then Unhide, brings the tile back.
  - Cancelling delete keeps the item; accepting it removes the tile and the
    catalog row.
- The Designer screenshots were regenerated (`make docs-shots`) and I read
  them for en and fa (RTL).
- The badge CSS uses only logical properties (`inset-inline-end` /
  `inset-block-end`).
- **Not looked at:**
  - the jiggle badges under ar/fa RTL in a live run (only the CSS was
    checked);
  - the 1024×600 sell screen with a category tab open (at that width the
    tabs collapse into the #2307 "…" menu);
  - real touch hardware or WebKitGTK.
- Full e2e `default` project (595 tests), run twice. Each run had 2
  exact-tab-count failures (`sale-screen-category-strip-overflow-2307` (b),
  `sale-screen-search-strip-2173` LTR, `tab-bar-overflow-aria-424`). The
  cause is stray uncategorized "probe" items that many catalog specs leave
  on the shared per-worker till. Those items are now tiles and form a
  synthetic "uncategorized" tab. Fix:
  - New helper `hideUncategorizedStrays` (e2e/tests/helpers.ts), called by
    the tab-count tests.
  - `catalog-row-oob-1363` and `catalog-barcode-backfill-1356` now
    deactivate what they create.
  - Checked on one worker, with polluters `catalog-category-brand-select-1430`
    and `catalog-item-form-1956` running first:
    - without the helper: 1 failed, 32 passed;
    - with the helper: 33 passed.

## Risk / deferred

- **Big catalogs:** quick-button groups now render every item without paging
  (the All tab does page). The page-cache card #2501 is related. A
  lazy-per-category follow-up can be filed if a real shop's render is slow.
- The cloud panel only shows explicit rows (finding 8, ut-docs#2562).

## Verdict

Safe to merge once the full gate is green.

## Merge with main (ut-docs#2499, #2535)

`main` gained two changes while this branch was open:
- **#2499's browsing mode.** `ButtonsHTTP.List` now keeps #2499's
  mode switch. It uses this branch's single `LoadAllActive` (always needed
  for the implicit-tile merge), so every mode still runs one catalog load.
- **#2535's migration 038.** This branch's migration is renumbered to
  **039**, and both checksums are pinned.

#2499 retired `buttons_categories_tab_test.go`. Its strip-mode #2498 tests
now live in `buttons_category_item_only_test.go`, ported to the hidden-item
pruning semantics (review finding 5):
- `…CategoryWithOnlyHiddenItemsIsPruned`
- `…ItemOnlyCategoryShowsImplicitTile`
- `…DefaultTabPrefersGroupWithButtons`, now with three categories so tabs
  exist.

Re-ran after the merge, all green except the two environment-only checks
(shellcheck binary, `retry-with-backoff` usage):
- `go test ./...`, golangci-lint and every CI guard;
- docs-shots regenerated;
- the affected e2e subset: 36 passed, including
  `sell-screen-browsing-mode-2499`.
