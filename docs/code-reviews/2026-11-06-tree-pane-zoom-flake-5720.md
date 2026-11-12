# Review: tree-pane zoom "exactly once" e2e flake (ut-docs#5720)

**Date:** 2026-11-06 · **Complexity:** easy · **Branch:** `fix/5720-tree-pane-zoom-flake`

Pipeline: BA and Architect were folded into the card's own spec (easy
investigation card). Dev: Sonnet. Tester and Reviewer: Opus (independent;
neither wrote the change).

## What shipped
Test-only. `e2e/tests/tree-pane-zoom-2943.spec.ts`, test "every rail row
zooms exactly once, from its own on-screen visible centre". Before the tap
under test, the loop now:
1. clears `__zooms`, taps the `other` row and waits for its URL (as before);
2. polls until `other`'s own zoom is recorded (`__zooms.length > 0`);
3. polls until `#items-panel.getAnimations()` is empty (that zoom finished);
4. expects the target rail row to be visible;
5. clears `__zooms` again, then taps the row under test.

The assertions on the tapped row (exactly one zoom; dx/dy within 1.5 px;
scale) are unchanged. No product code changed.

## Root cause
A race in the test, not a double zoom in the product. htmx 1.9.12 pushes the
URL before the swap (`tr(d.path)` before `je(...)` in the vendored
`htmx.min.js`), and the settle step runs `defaultSettleDelay` (20 ms) later.
`zoomPane()` runs in `htmx:afterSettle` (`web/public/app.js`, guarded by
`evt.target === t`). So `toHaveURL(other)` could resolve before `other`'s own
zoom was recorded. The old spec cleared `__zooms` right then, the late zoom
landed after the reset, and the tapped row counted 2 (`Expected 1, Received 2`).
The second symptom (`rail row has a box … Received null`) was the rail row
being queried while that swap was still settling. The Dev's instrumented run
(each zoom tagged with its pathname) and the Tester's 80/80 instrumented check
both show the extra record was `other`'s zoom, one zoom per swap.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | From the second row on, the "its own zoom is recorded" poll passed at once, because `__zooms` still held the previous row's record. Only the `getAnimations()` poll and the visibility check did the waiting, and those could pass before `other`'s zoom had even started (the 20 ms settle gap), so the race could in theory come back. Raised by the Tester. | **Fixed** by the Reviewer: `__zooms` is cleared before tapping `other` as well, so the first poll really waits for `other`'s zoom. Comment updated to match. |
| 2 | info | Could the new waits hide a real bug? They all run before the final reset and before the tap under test, so the tapped row's own count and origin checks are unchanged. A double zoom on the `other` navigation is discarded by the reset, but every destination is also tapped as `href` in its own row, so it is still covered. A duplicate landing more than 100 ms after the tapped swap was already missed before this change. | Accepted (no change). |
| 3 | info | Can a poll hang? Each `expect.poll` has the default expect timeout, so a missing zoom fails with a named message instead of hanging. A vacuous pass is ruled out for poll 1 by finding 1. Poll 2 starts only after a recorded `animate()` call (the instrument records just before calling the real `animate`), so it does not pass before the animation exists. | Accepted. |
| 4 | info | Comment accuracy (URL pushed before settle; settleDelay 20 ms) checked against the vendored htmx. | Correct. |

## Verification
- **Dev:** main's spec `-g "exactly once" --repeat-each=6`: 4/6 failed. Fix:
  12/12 passed. Removing the `evt.target === t` guard in `app.js` makes it fail.
- **Tester:** HEAD, whole spec `--repeat-each=12`: 120/120; `-g "exactly once"
  --repeat-each=24`: 24/24. Main's spec in a worktree, `--repeat-each=8`: 7/8
  failed (both card symptoms). Three `app.js` mutations all caught: guard
  removed (4/4 fail, Received 2), origin +8 px (4/4 fail, dx 8), old
  box-centre anchor (3/3 fail, dy 184.8). Guards (fixed-dates, i18n, rtl,
  mail-addresses, e2e fixtures, and others) exit 0.
- **Reviewer** (separate worktree, `UT_E2E_WORKER_PORT_BASE=9191`, all
  `--project=default --retries=0 --trace off`):
  - main's spec, `-g "exactly once" --repeat-each=6`: **3/6 failed**
    (`/categories` x2, `/inventory` x1: `Expected 1, Received 2`).
  - final spec (with finding 1 fix), same command: **6/6 passed**.
  - final spec with the `evt.target === t` guard removed from `app.js`:
    **3/3 failed** (`/catalog: exactly one zoom per tap`). Restored.
  - shared checkout, final spec, whole file `--repeat-each=12`: **120/120
    passed** (2.6 min).
  - `bash scripts/ci/guard-fixed-dates.sh`: pass (1978 files). The change adds
    no dates.
  - Worktree removed with `git worktree remove`; no till or browser processes left.

## Not verified
Not run under CPU stress or with CI's worker count; WebKit not run (the spec
is not phone-named). No Go changes, so no Go build or test.

Pre-flight: help topic — n/a (test-only, no user-visible behaviour); `reference/` / README — n/a (no behaviour change); `en.json` / meta — n/a (no keys); mirrors / ADR / taxonomy — n/a; plan/price, platform claim, my./admin UI — n/a; review record — this file; follow-up — none.

## Deferred
None.

**Verdict:** approve.
