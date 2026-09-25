# Review: sell screen strip mode loses its All tab (ut-docs#2613)

- **Date:** 2026-09-24
- **Branch:** `feat/2613-strip-no-all-tab`
- **Author lane:** `lane:cloud-54` (Opus 5.5 build), **reviewer:** Fable (independent, different model)
- **Card:** universaltill/ut-docs#2613 (`complexity:medium`)

## What shipped

With Settings → Sell screen → "How items are browsed" → **Category strip with
quick buttons** (`sale.browsing_mode=strip_overflow`), the sell screen no longer
shows an **All** tab. The strip is category tabs plus the ut-docs#2307 "…"
overflow button.

- **Default tab:** the first category that has quick buttons, else the first
  category. This is the existing #2498 pick.
- **Unchanged:** a flat or single-category till gets no tab bar, as before.
- **Items:** since #2541 every active, visible item is already a tile under its
  own category, so removing All hides no item. Server-side search still finds
  every active item.

The strip's All tab was retired outright, not hidden by wiring, so no dead
production path is left:
- `ButtonsHTTP.HideAllTab` and the `ShowAllTab` view key are removed.
- Strip mode no longer pages the All grid. `allBtns` is still loaded for
  Load's implicit tiles.
- The template's `$showAllTab` branches, `'__all__'` and `showAllGrid()` are
  gone.
- `all_filter_chips` keeps its own `#buttons-grid-all`, chips and load-more.
- `category_tabs` and the Designer replica are unchanged. The replica already
  forced `HideAllTab`, so its render is identical.

Also changed:
- `x-cloak` added on the search-only (`x-show="q"`) category headers. Their
  at-rest state is hidden, and they used to flash visible before Alpine
  hydrated.
- The hint `settings.sell_screen.browsing_mode_strip_overflow_hint` is reworded
  in en/ar/fa/tr, and in the de/es packs through their own PRs with a version
  bump.
- Help `sell.md` and `till-designer.md` are reworded in en/ar/de/fa/tr.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | README hero shots (`docs/images/till-sell*.webp`) show the old All tab | Accepted. The README caption already says "Real screens from v0.21.4"; follow-up card filed to recapture after the next release |
| 2 | minor | `TestButtonsHTTPList_StripOverflowNoQuickButtonsIsEmptyState` also passes on the old code | Accepted. It is a regression guard for the new `$empty`, not TDD evidence, and is not claimed as such |
| 3 | nit | Stale "All tab" comments in `buttons.go` (AncestorName, Hide) and `buttons_search_visibility_test.go` | Fixed |
| 4 | nit | Help says "first one selected"; the precise rule is "first with quick buttons" | Accepted; right level of detail for operators |
| 5 | nit | The fa hint's second clause had no verb | Fixed |

No blockers or majors. The reviewer checked:
- no dangling Alpine references;
- the chip mode is intact;
- the `$defaultTab` index is safe;
- the `$empty` semantics are correct;
- the Designer replica is unchanged;
- `x-cloak` does not hide first paint.

## Verification

- **Gate:**
  - `gofmt -l .` clean; `go build`, `go vet` pass.
  - `go test ./...`: 61 packages ok.
  - `golangci-lint`: 0 issues.
  - Guards in the ci.yml `build` job pass. `guard-shellcheck-version.sh` could
    not run because shellcheck is not installed in the container; no shell
    script changed.
  - Pack `validate.sh` and `check-key-drift.sh` pass.
- **TDD re-verified by the reviewer:** the production files were reverted, the
  new tests compiled and failed on real assertions (`cat-tab-all` present,
  default `tab: 'cat_food'` missing), and all passed after the restore.
- **Playwright:**
  - Touched specs (2499, 418, 2339, 2417, 2312, 1433, 1313, 2173, 424, 423,
    1459, 2314, 2307): all pass.
  - Full suite: 639/640 on the first run. The one failure (2312) was fixed and
    passes, alone and within its project (27/27).
- **Driven run, looked at:** throwaway till with the demo catalogue in
  `strip_overflow`.
  - 1024×600 en: tabs are Food/Drinks/Household/Produce plus "…"; Food is
    active; no All tab.
  - 1024×600 fa: RTL mirrors correctly, and so does the tab order.
  - 360px: layout unchanged.
  - Settings hint: shows the new text.
  - No console errors.
  - Not checked: a real touch device; dark theme (no CSS change beyond
    `x-cloak`).

## Verdict

Safe to merge.

## Deferred

- Recapture the README sell screenshots after the next release (Backlog card).
- A possibly flaky `internal/pages` test reading a truncated locale JSON (seen
  once, passed on re-run). Noted on the card.

## Post-merge fix-up (main moved: #2584 hidden categories)

Merging `main` brought in #1348 (ut-docs#2584), which merged cleanly but broke semantically:
- `internal/ui/category_icon_hidden_test.go` looped over the removed
  `HideAllTab`.
- The comment in `BuildCategoryGroups` and the new Categories help said a
  hidden category's items stay sellable "from the All tab".

The contract (manage-shop catalog §3.2, "sellable by search, scan AND quick
buttons") still holds without the strip's All tab:
- Explicit quick buttons of a hidden category move to the **Uncategorized** tab.
- Implicit tiles leave with their category and still sell by search and scan,
  and still show in the `all_filter_chips` All grid.

What changed:
- The test now pins the strip-only shape. Both hidden-category tests pass.
- The comment is reworded.
- `categories.md` is reworded in en/de/ar/fa/tr, using the UI's own labels.
- The docs-shots manifest is regenerated with `make docs-shots`. Main's PNGs
  are kept: the local Chromium renders different bytes, and the manifest
  hashes only the sources and topic markdown.

Reviewed inline by the orchestrator; the change is small and local.
