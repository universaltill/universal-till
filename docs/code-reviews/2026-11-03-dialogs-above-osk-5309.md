# Review: hold / pfand / table-add dialogs stay above the on-screen keyboard (ut-docs#5309)

Date: 2026-11-03 · Card: ut-docs#5309 (complexity:easy) · Follow-up of ut-docs#5304
Author: Sonnet (Dev subagent) · Reviewer: Opus 5.5 (independent subagent, isolated worktree)

## What shipped

- The bug is the same class as #5304. On a 1024×600 till with the OSK open, the OSK
  (z 1000, fixed to the bottom) could cover the action row of three non-modal dialogs that
  share the 8vh fixed frame: `#hold-modal`, `#pfand-modal` and `#table-add-modal`.
- `web/public/app.css` widens the two `body.osk-padded` rules from #5304. They used to
  apply only to `#elevation-modal`; they now apply to all four dialogs. There are no new
  properties:
  - `[open]` anchors the dialog at `.5rem` from the top, caps it at
    `100dvh - var(--osk-reserved-height, 17rem) - 1rem`, and lets it scroll inside itself
    (`overflow-y: auto`, `overscroll-behavior: contain`).
  - `.modifier-actions` sticks to the dialog's bottom edge on `background: Canvas`.
  - Only logical properties are used, and nothing changes while the OSK is closed.
- New spec `e2e/tests/dialogs-above-osk-5309.spec.ts` (`default` project) has 11 tests at
  1024×600 with `hasTouch`:
  - Per dialog: Cancel and submit sit above the live `#osk` top and pass an
    `elementFromPoint` check, and Cancel gets a real `.tap()`.
  - Per dialog: the focused field is in view.
  - Per dialog: closing the OSK restores 8vh, and reopening it is clear again.
  - Separately: `#table-add-modal` scrolls inside itself, plus a hold-modal measurement.
  - OSK mode is forced `on` in `beforeEach` and set back to `auto` in `afterEach`.
- `web/help/img/manifest.json`: only the docs-shots surface hash changed. The harness never
  opens the OSK, so no screenshot changes (`Docs-Shots-Unchanged: true`, as in #5304).
- Out of scope by BA/Architect analysis: `#parked-orders-modal`, `#confirm-modal` and the
  category dialogs. None of them has a field that can open the OSK.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | none (checked) | **Specificity and order.** `body.osk-padded #x[open]` is (1,2,1), which beats the base `#hold-modal`/`#pfand-modal`/`#table-add-modal` (1,0,0) rules no matter where they sit in the file. Those base rules set no `max-block-size` or `overflow`, so nothing is fought. No theme or plugin CSS targets these ids or `.modifier-modal`/`.modifier-actions`. | No change |
| R2 | none (checked) | **z-index.** It is untouched: hold stays 550, so it is still over `#payment-overlay` (#1542), and pfand/table-add stay 500. All are below `#osk` (1000). The new top anchor only moves the dialogs up, away from the OSK. | No change |
| R3 | none (checked) | **Sticky offsets.** `inset-block-end: -1.25rem` and `margin-block-end: -1.25rem` match `.modifier-modal`'s `padding: 1.25rem`. No media query changes that padding. The `@media (max-width: 480px)` `.modifier-actions { flex-wrap: wrap }` works with sticky. The Tester checked a wrapped row at 360px: both buttons can be hit. | No change |
| R4 | low (test value) | The three `#hold-modal` tests and the hold measurement **pass on old CSS too**. At 1024×600 in en, the hold row clears the OSK by about 10px. They are regression guards, not red tests. The Dev and Tester notes say so honestly. | Accepted |
| R5 | low (test value) | "Focused field is in view above the OSK" calls `scrollIntoViewIfNeeded()` before it measures, so it is close to tautological. It would not catch a field hidden under the sticky row. | Accepted (guard only; see deferred D1) |
| R6 | nit | `createTable(page, 'E2E 5309')` is not cleaned up after the spec. The next open, and other table specs, call `deactivateAllTables` first, so it is harmless. | Accepted |
| R7 | none (checked) | **Flakiness and global state.** OSK mode goes back to `auto` in `afterEach` through `setOskMode`, which is the race-hardened helper from #1375. The POS basket is reset. Geometry checks use `expect.poll`. `48px` is exactly 8vh of the pinned 600px viewport. Two full runs on this machine were green. | No change |

There are no blocking findings and the reviewer changed no code.

## Verification (Reviewer, personally, in worktree at 48b7e2a)

- **TDD revert check.** I reverted only `web/public/app.css` to `origin/main` and ran
  `npx playwright test tests/dialogs-above-osk-5309.spec.ts --project=default --workers=1`.
  - **5 failed:** pfand Cancel/submit, pfand close/reopen, table-add Cancel/submit,
    table-add close/reopen, and table-add scrolls inside (`overflow-y` was `visible`, not
    `auto`).
  - **6 passed:** the 3 hold tests, the hold measurement, and the two "focused field"
    tests for pfand and table-add (R4, R5).
  - I then restored the file (`git diff HEAD -- web/public/app.css` was empty) and got
    **11/11 passed** (59s).
- **Precedent spec:** `pin-dialog-above-osk-5304.spec.ts --project=auth`: 4 passed.
- **Guards:** `guard-rtl-logical-css.sh` passed, `guard-no-showmodal.sh` passed, and
  `guard-docs-shots.sh` passed (32 topics × 4 locales fresh, surface `cbf21444cca3…`).
- No Go change was made, so the Go gate does not apply.

## Tester attestation (summary)

- **Verdict:** PASS.
- **Specs run:** the new spec plus 8 neighbour specs, 31 passed:
  - hold-modal-duplicate-accessible-name-1628, index-keyboard-1023, osk-reserved-height-1998
  - deposit-refund-osk-1248, deposit-refund-payout-osk-1249
  - tables-tap-to-add-1025, tables-keyboard-reposition-826, held-table-move-2702
- **Driven walk:** 1024×600 with the OSK open, light/dark and en/ar (RTL mirrored),
  windowed and kiosk-class. The Tester read the screenshots for table-add (light-en),
  pfand (dark-en), hold (dark-en, light-ar), table-add (dark-ar, scrolled to the end),
  pfand (kiosk dark-ar), and table-add at 360px with a wrapped row.
  - With the OSK open, the dialog top is at 9px, its bottom at 271 or 303px, and the OSK
    top at 312px.
  - No coloured strip shows behind the sticky row.
  - Every text field, once tapped, ends up above the OSK and clear of the row.
  - The native `<select>` closes the OSK, and the dialog goes back to 8vh.
- **Not done:**
  - Real German: no de pack ships in core, so fa and an injected long string were used
    instead.
  - Hold at the 360px phone layout.
  - Touch-drag scrolling (only programmatic `scrollTop` was used).
  - The tr locale and other themes.
  - Tapping the OSK's own close key (`blur()` was used instead).
- **Chromium touch emulation only, not real Pi hardware.**

Pre-flight: till user-visible behaviour → `web/help/` n/a (no manual prose describes the keyboard/dialog overlap); docs-shots updated (surface hash only, no pixel change, `Docs-Shots-Unchanged: true`); `reference/` n/a (no reference page covers dialog geometry); README n/a (not stale); en.json keys n/a (no new strings); plan/platform/plugin/ADR rows n/a; review record updated (this file); follow-up D1 (+ D2) → deferred → ut-docs#5334.

## Verdict

**Approve.** There are no blocking findings and no reviewer fixes. Commit with the
`Docs-Shots-Unchanged: true` trailer, rebase onto `origin/main`, and merge once CI is green
on the rebased head.

## Deferred

- **D1 (follow-up, UX; filed as ut-docs#5334):** with the OSK open, the sticky action row can hide lower
  fields until you scroll.
  - On table-add, the "Seats" label shows but its input is covered, and Shape is hidden.
    The dangling label gives no hint that there is more to scroll to.
  - On pfand, the Manager PIN input is cut in half.
  - Every field can still be reached by scrolling, or by tapping it, which scrolls it clear
    of the row. This comes from the cap + sticky design shared with #5304 (accepted there
    as F3).
  - Options: a scroll-shadow cue, or a test that a tapped field is not under the row.
    Those would also fix R5.
- **D2:** hold-modal on a tall locale or a larger UI scale, where this change actually
  matters for hold, has not been measured (no de pack in core).
- **D3:** checks on real Pi touch hardware.
