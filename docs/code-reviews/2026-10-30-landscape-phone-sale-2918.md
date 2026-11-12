# Review: sale screen on a landscape phone (ut-docs#2918)

**Date:** 2026-10-30 · **Branch:** `fix/2918-landscape-phone-sale` · **Lane:** `lane:cloud-41`
**Author model:** Opus (inline) · **Reviewer:** Sonnet (independent, a different model from the author)

## What shipped

- **`web/public/app.css`:** two new blocks after the `@media (max-width: 480px)` `.pos-container` rule, for windows 481–900px wide and at most 480px tall (a phone held sideways):
  - **768–900px wide:** the desktop arrangement. The basket is a full-height column, with products above the tender beside it. The divider stays hidden, as in the rest of the 900px tier (#2308), so its track is dropped.
  - **481–767px wide:** the screen stays stacked. The basket is no longer capped at 45dvh, the sale screen scrolls, and the tender is `position: sticky; bottom: 0`. This is the #3059 phone sheet's pattern, including its `:has(dialog[open])` unpin. Pay stays whole and in reach however long the basket gets.
  - Ranges: these blocks never overlap the ≤480px phone tier (#3059) or the `min-height: 481px` tablet blocks (#3050).
  - **Review fix:** `:root --ui-scale-min-width: calc(100vw / 46)` for the 768–900 × ≤480 window. This mirrors the `min-width: 901px` rule (#4625), so a manual UI scale shrinks to fit the side-by-side grid.
- **`e2e/tests/landscape-phone-sale-2918.spec.ts` (new), 14 cases:**
  - **Side by side** (844×390, 896×414, 800×360, 768×400, 900×414, 768×480):
    - the first line's name, the whole totals box (inside the basket) and Pay are on screen and hit-testable;
    - a tile row shows;
    - the tender sits beside the basket;
    - there is no sideways scroll.
  - **Pinned** (667×375, 740×360): Pay is whole and hit-testable unscrolled, the first line shows, one scroll brings the totals clear of the tender, and Pay is still whole after it.
  - **UI scale 1.3 and 1.5** at 768×480, 844×390 and 900×414: no sideways scroll, the basket stays clear of the nav rail, and Pay is whole.
- **Manual:** one sentence in the intro of "Selling on a phone" in `web/help/en/sell.md` and `web/help/fa/sell.md`. The other locales have no phone section yet. `make docs-shots` was regenerated with no PNG changed, because docs-shots run only at 1024×600, which these queries never match. The surface hash was refreshed after the review fix.

## Findings (Sonnet review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | high | `--ui-scale-min-width` switches to the side-by-side budget (`100vw/46`) only at `min-width: 901px`. In the new 768–900 two-column window, a manual UI scale of 115% or more scrolled the page sideways. At 130% or more the basket slid under or left of the nav rail, for example −101px at 768×480 and 130%. This was a regression at those scales. | **Fixed:** the same budget now applies to that window. The new scale cases fail 6/6 without the fix and pass with it. |
| R2 | medium | The spec ran only at scale 1, and the 900×414 and 768×480 edges were missing. | **Fixed:** scale 1.3/1.5 cases and both edges were added. |
| R3 | low | Not measured: `body.kiosk`, `body.osk-padded`, and the hold-sale dialog under the sticky tender in the 481–767 block. | **Accepted, stated:** the sticky tender uses the #3059 unpin. The reviewer checked that the payment overlay covers the viewport and is hit-testable at 667×375 and 844×390. Kiosk and OSK were not checked. |

## Verified beyond the automated tests

- **Revert, fail, restore, pass:**
  - With origin/main's `app.css`, the 6 original cases all fail. The side-by-side cases fail on "first basket line must be visible" and the pinned ones on "Pay must be hit-testable". All 6 pass with the fix.
  - Removing the R1 rule fails the 6 UI-scale cases on "no horizontal page overflow". They pass with it restored.
- **Screenshots looked at, at scale 1:**
  - 844×390, 896×414, 800×360, 768×400, 667×375, 740×360 and 600×400 (en).
  - 844×390 in fa (RTL mirrors correctly, no overflow).
  - 667×375 with `?lang=de` (the core has no de strings; packs are plugins).
  - Before shots of 844×390 and 1024×600 confirm the bug and the desktop reference.
- **Not looked at:** the dark theme, kiosk mode, real touch hardware, or a real phone. Everything was emulated in Chromium.
- **Gates:**
  - e2e: 100 tests pass across the new spec, tablet-tier-totals-416, phone-width-layout-413, tender-panel-reachable, compact-tender-panel-2702, portrait-tablet-sale-3050, phone-sell-3059 and basket-no-horizontal-scroll-391.
  - Full `go test ./...` passes, except `internal/plugins`, which hit go test's default 10-minute timeout under load from the concurrent e2e and docs-shots runs. It passes with CI's own `-timeout 20m` (643s). No Go file changed.
  - The `ci.yml` build-job guards that apply to this diff pass, including rtl-logical-css, i18n, the help guards, docs-shots and e2e-fixtures-import.

## Deferred

- ut-docs#5212: the card's addendum. At 900×600, 768×600 and 600×600 the grand Total line is clipped about 15–25px by the 45dvh basket cap. This is a different mechanism with a real trade-off against the products' 8rem floor, so it was split rather than widened here.

## Verdict

Safe to merge.
