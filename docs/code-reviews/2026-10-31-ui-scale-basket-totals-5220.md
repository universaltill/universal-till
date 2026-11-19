# Sale screen: grand Total line clipped on stacked tablets at UI scale > 1 (ut-docs#5220)

Author model: Opus 5.5, inline (the lane runs on Opus; MODEL-ROUTING says
Sonnet builds medium cards, so the review went to Sonnet to keep the
reviewer on a different model from the author).

## Measured first (the card's first criterion)
Basket bottom vs totals bottom, 2 lines, `--ui-scale` set on `<html>` as
`portrait-tablet-sale-3050.spec.ts` does. Before the fix:

| window | 1 | 1.25 | 1.5 | 2 |
|---|---|---|---|---|
| 600x760 | ok | clip (330 < 373) | clip | clip |
| 768x800 | ok | 1px over | clip (283 < 448) | clip |
| 900x720 | ok | clip (317 < 374) | clip | clip (166 < 557) |
| 800x1280 | ok | ok | ok | ok |
| 768x760 | ok | clip | clip, tender row 38px, Pay hidden | clip |
| 880x960 / 700x900 | ok | ok / 1px over | clip | clip |

Two causes:
- The basket's chrome (header, toggle, thead, `.basket-scroll`'s 6rem floor,
  totals: ~17.7rem) grows with the scale, while the cap is 45dvh (38dvh from
  900px tall).
- The basket and tender are scroll containers in `auto` rows. Their
  automatic minimum is 0, so the grid squeezed them below even the cap.
  At 900x701 and scale 2 the basket was 156px tall.

`--ui-scale` is any value from 0.5 to 2 and media queries are in px, so no
media-query threshold can follow the scale.

## What changed
- `web/public/app.css`, a new block for 481-900px wide and 701px tall or
  more:
  - The rows are `max-content max-content minmax(8rem, 1fr)`, so the grid
    never squeezes the basket or the tender. `.pos-container` scrolls.
  - The basket cap is `max(45dvh, 18.5rem)`. 18.5rem is 315px / 17px, what
    45dvh gives at this tier's 701px edge at scale 1. A plain `45vh` comes
    first as the fallback for engines without dvh.
  - The tender is pinned to the bottom of the scroll, as in #2918's pinned
    layout, with its shadow and its unpin while a dialog is open.
  - The 900px-tall rule becomes `max(38dvh, 18.5rem)`.
- At scale 1 nothing changes: `--fluid-fs` is 17px across 481-900px, so
  18.5rem is below 45dvh at every height in the tier. Nothing overflows, so
  the pin does nothing. The basket and totals geometry at scale 1 was
  identical before and after at every viewport measured.
- `e2e/tests/tablet-tier-totals-416.spec.ts`, new #5220 block:
  - 600x760, 768x800 and 800x1280 at 1.25, 1.5 and 2.
  - 900x720 at 1.25, 1.5 and 2.
  - 880x960 at 2.
  - 768x760 at 1.5 and 900x701 at 2, both with 8 lines.
  - Each case checks the totals row ends inside the basket, the Total line
    is whole and uncovered, Pay is whole, nothing scrolls sideways, and a
    tile is reachable with Pay still whole.
  - `scroll` cases, 900x701-720 at scale 2 (~22rem of height): the basket
    and tender can't both show, so the Total line is one scroll away. It is
    uncovered once scrolled to.

## TDD check
With the original `app.css` restored, 12 of the 15 new cases fail. The 3
that pass are 800x1280, which guards the upright 38dvh cap that already
fit. With the fix, all 34 cases in the file pass.

## Also measured (out of the card's scope)
The <=700px-tall windows from #5212 at scales 1.5 and 2:
- Side-by-side (768x600, 900x600) holds.
- Pinned (600x600, 600x690) keeps the Total line inside the basket and Pay
  whole. The Total line can sit under the pinned tender and is one scroll
  away, as #5212 designed for that layout. No follow-up needed.

## Review (Sonnet, read-only, fresh context)
No blockers and no majors; verdict: safe to merge. Findings:
- Minor, fixed: the new cases didn't check the products stay reachable
  under the pinned tender. Added a tile reach check plus Pay after it.
- Minor, fixed: without dvh the cap declaration is invalid, and the
  `max-content` row would let a long basket grow unbounded. Added a `45vh`
  fallback.
- Minor, documented: 18.5rem leaves the same ~0.8rem slack scale 1 has, so
  an extra totals row (discount, customer) is a scroll away, as at scale 1.
  The comment says so.
- Minor, accepted: in the `scroll` cases the Total line is uncovered only
  after scrolling. This is by design, and the comment and spec say so.
- Nits, fixed:
  - "<=900px-tall" became "900px+-tall".
  - "pinned layout below" became "above".
  - Re-wrapped an overlong comment line.
  - Added the pinned block's shadow to the new pinned tender.
- Also checked by the reviewer:
  - Cascade: the block can't overlap the <=700px or <=480px tiers.
    `.basket.receipt-view` still scrolls. No theme or preset sets rows on
    `.pos-container`.
  - The `:has(dialog[open])` unpin keeps the payment overlay free.
  - The OSK still works.
  - WebKit supports `max()` and dvh from Safari 15.4.
  - `bottom` is not a left/right property (RTL guard).

## Gate
- Guards pass: rtl-logical-css, no-showmodal, i18n, e2e-*, docs-shots
  (surface hash refreshed). Docs-shots are 1024x600 and every changed rule
  is scoped to `max-width: 900px`, so no screenshotted pixel changes.
- The full default e2e project and the CSS Go tests ran (see the PR).
- No user-visible text or help topic changed; this is a layout-only fix.

## Verdict
Safe to merge. CSS and e2e only, scoped to 481-900px wide and 701px tall or
more. Rendering at scale 1 is unchanged.
