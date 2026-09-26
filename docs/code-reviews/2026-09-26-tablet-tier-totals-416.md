# Code review — tablet-tier totals row regression test (ut-docs#416)

**Date:** 2026-09-26 · **Lane:** cloud-24 · **Built by:** Sonnet (dev subagent) · **Reviewed by:** Opus 5.5 (independent subagent, fresh context)

## What shipped

- `e2e/tests/tablet-tier-totals-416.spec.ts` (new): 9 viewports in the
  `max-width: 900px` stacked tier. Each adds two items and hit-tests the
  basket's totals row. The probe names whatever covers it, so a failure prints e.g.
  `Received: "tender"`. Viewports come in three kinds:
  - `regression`: 600x600, 768x600, 880x640, 900x600. These fail with the pre-#2702 rows.
  - `smoke`: 540x720, 600x960, 768x1024.
  - `reach`: 896x414, 844x390. These scroll `.basket` first, so they only prove the totals row can be reached.
- `web/public/app.css`: comment-only. The #413 note no longer calls the
  tablet range an out-of-scope issue. It points at #2702's fix and the new spec.

No CSS behaviour change. The root cause (the 900px tier's `grid-template-rows: auto`
had no flexible track, so align-content stretched the rows equally) was already fixed as a side effect of
ut-docs#2702 (`auto auto minmax(8rem, 1fr)`, commit b1520aa). This card adds the missing regression proof.

## TDD evidence

With the 900px tier's rows reverted to `auto`, 4 tests fail and 5 pass:
- 600x600, 768x600 and 900x600 give `Received: "tender"`.
- 880x640 gives `Received: "DIV"` (the probe hits `.pos-container` itself).

With the rows restored, 9/9 pass. The reviewer reproduced this independently
(at that time only 900x600 was a regression viewport). The orchestrator re-ran it after the fixes.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The header claimed totals were fully on-screen unscrolled. At short stacked heights (≤ ~640px, e.g. 900x600) the grand-Total line is still ~15px clipped by `.basket`'s own `max-height: 45dvh`. The probe only checks the top edge. | Header corrected to state the scope honestly. The residual clipping has a different mechanism (the 45dvh cap is smaller than the basket's min-content), so it moved to ut-docs#2918 with the landscape-phone layout. |
| 2 | Medium | The two scrolled landscape viewports pass whether or not the bug is present. The regression proof rested on 900x600 alone. | Fixed: they are relabelled `reach`, and 600x600, 768x600 and 880x640 were added as `regression` viewports (verified failing pre-fix). |
| 3 | Low | 900x600 was called "just above the tier edge", but `max-width: 900px` includes 900. | Fixed: "inclusive top edge". |
| 4 | Low | The hit-test returned a boolean, so a failure didn't name the covering panel. | Fixed: it returns the owner's class. |
| 5 | Low | The spec referenced a review record that didn't exist. | Fixed: this file. The dangling reference was removed. |
| 6 | Low | The app.css comment was re-wrapped and bloated (+36/−24). | Fixed: the original #413 comment is kept verbatim, and only the last sentence changed (+5/−2). |

## Verified beyond automated tests

The Tester took screenshots at 768x1024 and 844x390 with items in the basket.
- 768x1024: the totals row is fully visible, with no overlap.
- 844x390: the basket shows no lines without scrolling, and the Pay button is half-clipped by the tender panel's own box. No panel is drawn over another, so #416's AC holds, but this is a real usability gap. It is filed as ut-docs#2918.

## Gate

- `tablet-tier-totals-416`: 9/9 pass.
- `phone-width-layout-413`, `tender-panel-reachable` and `compact-tender-panel-2702`: 25/25 pass.
- `guard-i18n.sh`: OK.
- No Go changes.

## Verdict

Safe to merge. Test and comment only; no production behaviour change.
