# Sale screen: grand Total line clipped on short stacked tablets (ut-docs#5212)

Author model: Opus 5.5, inline (the lane runs on Opus; MODEL-ROUTING says
Sonnet builds medium cards, so the review went to Sonnet to keep the
reviewer on a different model from the author).

## What changed
- `web/public/app.css`: the two short-window layouts from ut-docs#2918 now
  apply up to 700px tall (was 480px):
  - 768-900px wide: the side-by-side grid (basket column | products over
    tender), with its `--ui-scale-min-width` budget (line ~120).
  - 481-767px wide: the stacked, scrolling screen with an uncapped basket and
    the tender pinned to the bottom.
  - Below ~700px tall the stacked tier's `max-height: 45dvh` cap is smaller
    than the basket's min-content (~301px at scale 1), which is what clipped
    the Total line. Taller windows keep the stacked tier unchanged.
- New block for 768-900 x 541-700px: the barcode field takes its own row. In
  the ~280px tender column at 768px it was squeezed to a ~10px sliver. Below
  541px the extra row pushed the first tile row out of the products panel
  (measured at 768x481), so the scan row stays one row there.
- `e2e/tests/tablet-tier-totals-416.spec.ts`: a new #5212 block for 900x600,
  768x600, 880x640, 768x541, 768x540, 900x700, 600x600 and 600x690 (2 lines),
  plus 768x600 and 600x600 with 8 lines. Each case checks:
  - the totals row ends inside the basket and inside the window;
  - the grand Total line is whole and uncovered;
  - Pay is whole and hit-testable;
  - a tile row is whole: unscrolled side by side, one scroll away in the
    pinned layout, with Pay still whole after that scroll;
  - the basket never scrolls sideways;
  - with 8 lines in the pinned layout, the Total line is one scroll away and
    not under the pinned tender.
- The #416 block's four short viewports now land in these layouts. They are
  relabelled (`short`, and `pinned` for 600x600, where the sticky tender may
  sit over the basket's bottom padding by design). The header now says
  honestly that reverting the #416 rows to `auto` no longer fails them.
  Re-measured with `auto` rows at 600x720, 768x720, 900x720, 880x760 and
  768x800: all pass, so the #416 mechanism shows nowhere in the stacked tier
  any more.
- Comment touch-ups: the #3050 container note in app.css, the
  landscape-phone-sale-2918 spec header.

## Rejected approach
A `minmax(min-content, 45dvh)` basket row (the <=480px tier's track) does
not work at this tier. The basket's min-content includes every line, because
`.basket-scroll`'s content counts, not its 6rem floor. At 600x600 the basket
grew to 434px and the tender's `auto` row shrank to 26px.

## TDD
The new block was run against the original CSS (`git show HEAD:web/public/app.css`):
- fail on "the totals row must end inside the basket": 600x600, 768x540,
  768x541, 768x600, 880x640, 900x600;
- pass: 900x700 and 600x690. The old cap just fits there; these two check
  the new layouts at their height edge.

With the fix, 76/76 pass across tablet-tier-totals-416,
portrait-tablet-sale-3050, tender-panel-reachable, compact-tender-panel-2702,
landscape-phone-sale-2918 and basket-no-horizontal-scroll-391. The two
8-line cases were added after review and were not run against the old CSS.

## Looked at (screenshots, read)
- 900x600, 768x600, 768x541: the whole Total line, Pay and a full tile row,
  unscrolled.
- 600x600: the whole Total line and Pay. Tiles are one scroll away.
- fa (RTL) at 768x600 and 600x600: mirrored correctly, no sideways scroll.
- 1024x600 (kiosk floor): unchanged, not in any changed range.
- Not looked at: dark theme (no colour changed), UI scale > 1 at these sizes,
  real touch hardware.

## Gate
- `go build ./...`, gofmt clean, `go test ./internal/pages/ ./internal/httpx/`
  (the app.css readers): ok.
- 54 of the 56 guards in ci.yml pass locally. The other two fail on tools in
  this container, not on this change: `guard-deadcode-baseline` (deadcode
  built with Go 1.26, the repo needs 1.27) and `guard-shellcheck-version`
  (no shellcheck binary). CI runs both.

## Findings
Independent review: Sonnet 5.5 subagent (author was Opus). It ran the
three layout specs (45/45) and the RTL guard, and checked every matchMedia
call in JS: all are width-only and unaffected.

1. MEDIUM: the 700px threshold and the ~301px min-content hold at UI
   scale 1 only. A larger scale may clip the Total line again in the
   stacked tier above 700px. This is not new: the 45dvh / 38dvh caps have
   always been in dvh against a rem min-content. **Deferred** to
   ut-docs#5220, and the app.css comment now says the fix is for scale 1.
2. MEDIUM: the pinned layout now also covers 540-767px tablets up to 700px
   tall. With a long basket it pushes the tiles below the fold. Before, the
   stacked tier kept them partly visible but clipped the Total line.
   **Accepted** as #2918's own choice for these windows: the Total line and
   Pay are never hidden, and the tiles are reachable by scrolling. **Added**
   an 8-line pinned case (600x600) that proves it: the Total line is
   reachable and uncovered, a tile row is reachable, Pay is whole. The
   suggested `55dvh` cap would bring back an inner scroll.
3. LOW: the #3050 container line shapes now meet a ~26rem basket column.
   **Fixed**: every #5212 case asserts the basket doesn't scroll sideways,
   with an 8-line side-by-side case too. Not covered: a scale above 1
   (ut-docs#5220).
4. LOW: the #416 `auto`-rows regression no longer has a stacked-tier case
   that would catch it. **Accepted and recorded**: re-measured with `auto`
   rows at five taller stacked heights, all pass, so the mechanism no
   longer shows anywhere in the stacked tier. The spec header says so.
5. LOW: `totals.bottom <= basket.bottom` is close to a tautology in the new
   layouts. **Kept**, with a comment: the hit-test on `.total` is the check
   that tells the layouts apart, and the bottom check failed on the old
   CSS.

## Known and deferred
- Pre-existing: the document is taller than the window in the pinned layout
  (648px at 667x375 on the unchanged #2918 rule; 611px at 600x600 now).
  Nothing visible comes of it.
- At 768px the tender's action row wraps its last icon (Open orders) onto a
  second line. The <=540px side-by-side windows also keep the squeezed
  barcode field. Both were already true at 768-900 x <=480 (#2918):
  ut-docs#5221.
- UI scale above 1: ut-docs#5220.

## Verdict
Safe to merge. The change is CSS only and scoped to 481-900px-wide windows up to 700px tall. Wider, taller and <=480px layouts are untouched.

## Addendum: docs-shots surface hash
CI's `guard-docs-shots_test.sh` flagged the manual's screenshots as stale,
because `app.css` changed. Every docs-shot is taken at 1024x600
(`e2e/playwright.docs.config.ts`). Every changed rule is scoped to
`max-width: 900px`, so no screenshotted pixel can change. Only the surface
hash was refreshed (`scripts/ci/update-docs-shots-surface-hash.sh`, commit
trailer `Docs-Shots-Unchanged: true`).
