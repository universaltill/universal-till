# Review: Import / Tax codes dialogs scroll on touch (ut-docs#6553)

Reviewer: Opus 5.5 subagent (Dev wrote with Sonnet), plus orchestrating Reviewer. Tester verdict PASS.

Pre-flight: till user-visible behaviour: web/help topic n/a (pure scroll bug, no documented behaviour; no screenshot of these dialogs changes), reference/ n/a, README n/a; en.json keys n/a (no new strings); review record updated (this file); CHANGELOG updated.

## What shipped
CSS in `web/public/app.css`: `#import-modal` and `#tax-codes-modal` become the scroller (display:block, overflow-y:auto, padding, sticky `.page-head`, explicit closed re-hide), scoped by id so `#item-form-modal` keeps its pinned head. New Playwright spec `catalog-import-dialog-scroll-6553.spec.ts`; CHANGELOG entry.

## Findings
1. Low, fixed: spec wrote screenshots to a hard-coded `/tmp/tabletfix/...` path (leftover debug). Removed.
2. Low, fixed: `touch-action: pan-y` blocked pinch-zoom (accessibility). Now `pan-y pinch-zoom`, matching the precedent at the reorder rows.
3. Tester cosmetic note (content peeking above the sticky head): verified resolved by the `-1rem` offset plus padding; head top sits at the dialog top before and after scrolling.

Notes, no action: `networkidle` wait harmless; padding assertion measures left only (RTL uses logical/symmetric padding).

## Verification
- Red check (Tester): old CSS gives 4 failed, 1 passed (`Expected "auto" Received "hidden"`); fix gives 5 passed. Re-run after review fixes: 5 passed.
- Neighbour specs, 63 passed (Tester). Real CDP touch scroll probe at 360x800, 1280x800, 1024x600: both dialogs scroll to the end.
- Cascade checked: id specificity vs `.item-form-modal(:not([open]))`, osk-padded, close animation, RTL, dark theme.
- Not covered: physical tablet (hasTouch is emulation); real-device check stays with the tablet-fixes run.

## Verdict
Approve. Deferred: none.
