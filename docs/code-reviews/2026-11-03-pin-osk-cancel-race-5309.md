# Review — PIN dialog spec: poll onCancel, the close event is async (ut-docs#5309 follow-up)

**Date:** 2026-11-03 · **Branch:** `fix/5309-pin-osk-cancel-race` · **Author:** orchestrator (`[no-subagent: one-line test-only fix]`) · **Reviewer:** Opus 5.5, fresh context

## What shipped

Test-only. `e2e/tests/pin-dialog-above-osk-5304.spec.ts` "fetch path: Cancel/Confirm clear of the OSK, Cancel is tappable" now polls `window.__c` after `open` turns false, instead of reading it once.

## Why

After universal-till#2308 merged, `main`'s UI E2E failed in the `auth` project with `__c` 0 (expected 1) on all 3 tries. The same failure hit help-only #2306 earlier, before #2308 existed. #2308 only widened a selector list, so `#elevation-modal`'s computed rules are identical.

Root cause: `utPostWithElevation` (`web/public/app.js` ~2973) runs `onCancel` from the dialog's `close` listener. Per the HTML spec, `close()` clears `open` synchronously and then queues a task to fire `close`. The test asserted `open === false` and read the counter in a separate `page.evaluate`, which can run before that task.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | info | Root cause confirmed. The code path was checked: `inline-actions.js` `closeDialog` → `d.close()`. The `finished`, `htmx-request`, `ended` and capture-listener guards cannot leave `__c` at 0 in this test. A live Chromium probe shows the order `open=false` → microtask → `setTimeout(0)` → `close` → rAF. `show(); close()` followed by a separate evaluate read 0 on most of 20 iterations. | Confirmed |
| R2 | info | Polling does not mask a bug: `expect.poll(...).toBe(1)` still fails after the 10 s expect timeout if `onCancel` never fires. This follows the `prompt-inflight-fetch-5291.spec.ts` precedent. | Accepted |
| R3 | low (optional) | The poll stops at the first 1, so a later double-fire would go unnoticed. The `ended` guard rules that out in code, and the old single read could not catch it either. | Accepted, no change |
| R4 | nit | Comment should say when Chromium fires the event, so nobody "fixes" this with a microtask wait. | Fixed |
| R5 | info | No other read-once-after-close race in this spec or in `e2e/tests`. 5291:124 is safe because `ended` and `finished` are already set there. | Checked |

## Verification

- `npx playwright test tests/pin-dialog-above-osk-5304.spec.ts --project=auth --workers=1 --repeat-each=5 --retries=0`: 20/20 passed locally (orchestrator). The reviewer's run passed 4/4.
- No red run locally: the race depends on timing under CI load. The probe in R1 stands in as the evidence.

Pre-flight: test-only change → `web/help/` n/a (no behaviour change); docs-shots n/a (no `web/` change); `reference/` and README n/a; en.json n/a; ADR, plan, platform and plugin rows n/a; review record updated (this file); follow-ups none.

**Verdict:** approve.
