# Review: sale-screen grid keeps its scroll position across a buttons-changed refresh (ut-docs#2876)

Date: 2026-11-02 · Lane: lane:cloud-41 · Complexity: easy
Built by Sonnet (subagent); reviewed by Opus 5.5 in a fresh context (different model from the author).

## What shipped
- `web/ui/partials/buttons.html` `restoreGridState()`: the grid root
  (`.pos-container > .products`, the element that actually scrolls; the card's
  `#buttons-grid` is not a scroller) records `{tab, top, at}` into
  `window.utSaleGridState.scroll` on its own `htmx:beforeSwap` (only when
  `elt` and `target` are the root and `shouldSwap` is true). The re-rendered root
  consumes it once and restores `scrollTop` after `$nextTick` +
  `requestAnimationFrame`. It restores only for the same tab, within 10 s,
  and not while a sale-screen search is showing. A fresh visit after boosted
  navigation, a tab change or a stale capture never jumps.
- `e2e/tests/sell-grid-scroll-refresh-2876.spec.ts` (new, 1024x480).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nice-to-have | The offset was captured at `htmx:beforeRequest`. Scrolling during the request was lost, and a failed or unswapped refresh left a stale capture behind for up to 10 s. | **Fixed**: capture at `htmx:beforeSwap` with `shouldSwap`/`target` checks. |
| 2 | nice-to-have | A comment claimed the Designer (edit mode) restores too, but `.products--edit` is not a scroller. | **Fixed**: claim removed. |
| 3–6 | — | Request filter, listener lifetime, restore timing vs layout (fixed-height thumbs, settle delay 0), the sell-screen-watch.js interplay and the template/JS escaping were all checked and are correct. | Accepted |
| 7 | info | The other-tab, stale and back-to-top checks cannot fail against the old code. They only guard against over-eager restoring in future. The main assertions do fail without the fix. | Accepted |

## Verified
- TDD re-run by the reviewer: the spec passes with the fix. Without it, both tests fail:
  `scrollTop kept after refresh … Received: 150` (reset to 0) and
  `Expected: > 40, Received: 0`. Both pass again after the fix is restored.
- After fixes 1 and 2: the new spec, sell-screen-live-refresh-2765 and designer-wysiwyg-2174
  pass (6 passed). Before the fixes the dev also ran sell-chip-refresh-3089,
  products-scroll-affordance-1313, sale-screen-category-tabs-search-418 and
  sell-stale-tile-2525 (12 passed).
- `go build ./...`, `go test ./internal/pages/...`, guard-i18n, guard-rtl-logical-css and
  guard-no-showmodal are clean. No new strings and no CSS.
- Looked at: 1024x600 light and 360x740 light, before and after a refresh scrolled ~120 px.
  The grid stays mid-list, with nothing blank or clipped.
  Not looked at: dark theme, RTL, the Designer and the all-items layout. On the all-items tab an
  offset below the first page clamps, because only page 1 re-renders (accepted).
- No help-topic change: this restores expected behaviour and has no operator-facing step.

## Verdict
Safe to merge.
