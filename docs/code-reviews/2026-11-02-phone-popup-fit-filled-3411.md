# 2026-11-02 — e2e: phone popup-fit with server-filled content (ut-docs#3411)

## What shipped
- One new test in `e2e/tests/phone-popup-fit-3351.spec.ts`, per phone width
  (390 / 440 px), reusing the spec's `measure()` / `problems()` and its
  Full-motion `beforeEach`:
  - seeds an item with one modifier group + a long-ish option label as a
    sell-screen tile (same path as `seedModifierItem` in
    `popup-zoom-2944.spec.ts`), taps the tile and fit-checks the **filled**
    `#modifier-modal` (asserts the option text is inside first);
  - seeds one table (`createTable`), puts a dine-in line in the basket, opens
    the phone basket sheet → table picker and fit-checks the filled
    `#table-modal` (asserts the seeded option is visible first);
  - cleans up the tile, the item and the tables in `finally`.
- **Extra check found while writing it:** a CSS mutation forcing the
  option grid to 700 px first *passed*. `measure()`'s `reachable()` waves
  through anything inside a box that scrolls or clips sideways, and the
  popup itself scrolls, so a too-wide grid just opens cut off at the popup
  edge. The new test therefore also fails when anything in either picker
  (popup included) overflows sideways (`scrollWidth > clientWidth + 1` on a
  non-`visible` `overflow-x` box). The sweep's own rule is unchanged; the
  same blind spot for every other popup is filed as a follow-up card.
- Test-only. No product code, no locale keys, no help change.

## Review
Written by Opus; independent review by a Sonnet subagent in a detached
worktree (ran the targeted test, the full file and a mutation).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | Table-picker coverage not mutation-verified. | **Already verified** by the author: forcing `.table-picker-option` to 700 px fails at both widths (`#table-modal … scrollWidth=721`). |
| 2 | low | `closeAll(page)` first in `finally` throws on a dead page and skips the data cleanup. | **Fixed**: `closeAll(...).catch(() => {})`. |
| 3 | info | Empty "Pf3411 Cat" category left behind (same as the 2944 precedent). | Accepted: per-worker throwaway till; nothing reads it. |
| 4 | info | `seedModifierItem` duplicates the 2944 helper. | Accepted for now; hoist into `helpers.ts` when a third spec needs it. |
| 5 | info | A future deliberate ellipsis label would trip the sideways check. | Accepted: whitelist that selector then; comment states the intent. |

## Verified
- `npx playwright test tests/phone-popup-fit-3351.spec.ts --project=default`:
  4 passed (sweep still finds 31 distinct popups at both widths); the new
  test re-run after fix 2: 2 passed.
- Mutations (author and reviewer), each failing at 390 and 440 px, then restored:
  `#modifier-modal label, button { min-width:700px; nowrap }` →
  `dialog#modifier-modal scrollWidth=751, clientWidth=349/394`;
  `.table-picker-option { min-width:700px }` → `dialog#table-modal scrollWidth=721`.
- CI `build`-job guards run locally: all pass except `guard-deadcode-baseline`
  (local deadcode binary built with Go 1.26) and `guard-shellcheck-version`
  (no shellcheck installed) — environment only; no Go or shell file changed.
- No screenshots looked at: the test is geometric; no surface changed.

## Verdict
Safe to merge.

## Sweep refresh (2026-11-17, lane:cloud-41)
The PR's CI run of 2026-11-02 timed out both new tests (120 s, at 390 and 440 px, every retry); the report artifact had expired, so no trace. Merged `main` (clean, no conflict; the diff against `main` is still only this spec and this record) and ran the **whole** default project in CI mode here (`CI=1`, 4 workers, `--retries=0`, shared worker tills): both new tests passed. 1702 passed, 2 failed, both in specs this PR doesn't touch (`money-comma-2925` "3,50" converts to 350; `self-order-popup-zoom-2972` Full Checkout zoom). So the hang came from `main`'s state at the time, not from this test. Fresh CI on the refreshed head decides.

Pre-flight: help n/a (test-only, no surface change); docs-shots n/a; reference/ n/a; README n/a; en.json/meta n/a (no keys); ADR n/a; mirrors n/a; review record this file; follow-up ut-docs#5249 (same blind spot in the main sweep).

## Diagnostics commit (2026-11-17, lane:cloud-41)
The refreshed head `dad9909` timed out again in CI at both widths on every retry, still with the cleanup `post` as the headline error. The trace artifact can't be fetched from this environment (proxy 403 on the blob host). It still passes locally with the German overlay CI uses (`UT_E2E_I18N_OVERLAY_DIR`), and in the full CI-mode run. So this commit makes the CI hang name itself instead of fixing a cause nobody has seen:
- each phase is a `test.step` with its own timeout: seeding 45 s, table seeding 45 s (a login plus one reload per active table), the others 30 s;
- every tap, the import and modifier `waitForResponse`, and the catalogue `getAttribute` have a 15 s timeout;
- the `finally` cleanup gets 30 s more test time, a bounded 25 s step and 5 s request timeouts. Each failure is logged with `console.warn`, so it can no longer replace the original error.

Mutation (table-picker test id renamed): the test fails in 15 s with `› table picker: open and measure` + `TimeoutError: locator.click: Timeout 15000ms exceeded` as its headline. Restored afterwards. Both tests pass locally (CI=1, de overlay).

Independent review (Sonnet, read-only): it found nothing that hides a failure or passes vacuously. It raised 2 medium findings: the cleanup was still unbounded, and seeding was not instrumented. Both are fixed above. Of its 2 low findings, the 30 s table-seed step is now 45 s, and swallowed cleanup errors are now logged. Its info finding was the stale dates in the comment, now removed. Its note on the `^1.48.0` floor was not acted on: the lockfile pins 1.61.1, which supports step timeouts.

Not merged by this lane: the next CI run names the hanging step, and the fix follows on this PR.

## Fix commit (2026-11-17, lane:cloud-41)
The diagnostics run (`98ff949`) named the hang in `table picker: open the basket sheet`. The `.basket-phonebar` tap waited because the bar was never visible: the basket was still empty. The `POST /api/pos/scan` of the seed's fixed barcode `5000000000012` had added no line on CI's shared worker till, and its status was never checked. Tapping "the first plain sell tile" instead was not reliable either: locally, none was visible at phone width.

Fix: the CSV import seeds a second plain item with its own sell tile. The phase taps that tile by name and asserts the bar lost `is-empty` (with a message) before opening the sheet. Cleanup also removes the plain tile and deactivates the plain item. The cleanup step was raised to 40 s (+45 s test time), and the stale "dine-in line" comment was reworded. Both tests pass locally (CI=1, de overlay).

Independent review (Sonnet, read-only, same reviewer): safe to merge. Its 2 low findings are fixed: the cleanup budget and the comment. Its info note: the barcode cause is inferred, not proven, because the scan status was never asserted. Other specs that scan `5000000000012` pass in CI, so this test sidesteps the cause rather than chasing it.
