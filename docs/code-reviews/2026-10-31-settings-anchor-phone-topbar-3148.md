# Settings deep links hidden under the phone top bar (ut-docs#3148)

Author model: Opus 5.5, inline (easy card; the lane runs on Opus). The
review went to Sonnet so the reviewer is a different model from the author.

## What changed
- `web/public/app.css`: inside the `@media (max-width: 480px)` block that
  defines `--phone-top-h`, `html:has(.nav)` gets
  `scroll-padding-block-start: calc(var(--phone-top-h) + .5rem)`. At phone
  width the 56px `.nav` bar is `position: sticky`, so an anchor jump
  (`/settings#settings-about`) put the card's top edge at y=0, under the
  bar, which hid the heading. The rule is on the root scroller, so it covers
  every phone page with the bar, not just Settings (UX skill: fix it in the
  shared place). Above 480px the nav is a side rail, so nothing changes there.
- `e2e/tests/settings-anchor-phone-topbar-3148.spec.ts`: new spec.
  - At 360x740, `/settings#settings-about` and `#settings-backup`: the
    heading's top is at or below the bar's bottom, after the page has
    really scrolled.
  - A same-page hash change gets the same check.
  - At 1024x600 no padding applies and the card still lands at the top.

## TDD
- Red before the CSS: the heading top was 15.3 against a bar bottom of 56,
  on all three 360px tests.
- Green after: the heading lands at 79.3.
- The reviewer ran a mutation test (CSS line removed) and the spec failed
  again with the same error.

## Review findings (Sonnet)
1. **Minor, fixed.** The rule first applied to every page at <=480px,
   including the self-order pages. Those pages have no `.nav`, so the
   padding would have reserved space for a bar that isn't there. It is now
   scoped to `html:has(.nav)`. A browser without `:has()` drops the rule
   and keeps the old behaviour, so nothing gets worse than before.
2. **Nit, accepted.** The spec samples two deep sections and does not walk
   every `#settings-*` anchor. The rule is global and sits on the root, so
   every card behaves the same way, and two samples plus the hashchange
   path are enough.

The reviewer also confirmed the following:
- `scroll-padding` affects only the root scroller.
- Fixed sheets and dialogs are unaffected.
- Every `scrollIntoView` caller is the same or better: `nearest` and
  `start` targets now clear the bar.
- `osk.js` does not interact with the rule.
- The rule uses a logical property, so RTL works.
- No strings were added.

## Verified beyond automated tests
- Screenshots of `/settings#settings-about`, looked at:
  - 360x740 in en: the "About" heading is fully under the bar.
  - 360x740 in fa (RTL): same result.
  - 1024x600: same landing as before (heading y=13.7).
- Ran the phone, settings and narrow e2e specs (37 files, 4 workers):
  - 282 passed.
  - 2 failed at viewports wider than 480px, where the rule cannot apply:
    `landscape-phone-sale-2918` 667x375 and `settings-two-pane-1960`
    nested anchor at 1280.
  - Both passed 3 of 3 on the branch and 3 of 3 on `main`'s CSS, so they
    are load flakes, not regressions.
- Not checked: real touch hardware. This is a scroll-offset change, not a
  touch handler.

## Manual
No help topic describes this landing position, so there is no `web/help/`
change.

## Verdict
Safe to merge.
