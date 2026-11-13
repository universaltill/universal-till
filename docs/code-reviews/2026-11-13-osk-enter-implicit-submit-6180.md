# Code review: OSK ↵ follows HTML implicit submission (ut-docs#6180)

Date: 2026-11-13
Repo: universal-till, branch `fix/6180-osk-enter-implicit-submit` (off `origin/main` 8a8dfb9e4)
Author: Sonnet (Dev + Tester). Reviewer: Opus 5.5, independent subagent in a detached review worktree.

## What shipped

- `web/public/osk.js`: the on-screen keyboard's ↵ key no longer calls
  `form.requestSubmit()` on any form. A new helper `implicitSubmit(form)`
  follows the HTML implicit-submission rule a physical keyboard's Enter uses:
  1. The form has a default button (the first submit button in `form.elements`,
     which includes buttons tied to the form with a `form=` attribute): click it,
     or do nothing if it is disabled.
  2. Otherwise, if the form has more than one field that blocks implicit
     submission (text, search, url, tel, email, password, date, month, week,
     time, datetime-local, number): do nothing. The keyboard has already closed.
  3. Otherwise: `requestSubmit()`, falling back to `submit()` on old WebKit.
  The keyboard still hides and the field still blurs first. The textarea path
  and the header comment are left alone (sibling card #6179).
- `e2e/tests/osk-enter-implicit-submit-6180.spec.ts` (new): covers the receipt
  designer (no reload, value kept, keyboard closed), the scan form, a record
  dialog submitting through its external Save, a disabled Save (both the
  `disabled` attribute and an ancestor `fieldset[data-main-till-lock]`), and the
  split-tender form (no reload). The reviewer added one more test (below).

The bug: Settings → Receipt designer `#rd-form` uses `hx-post` with a custom
`hx-trigger`, has only `type=button` buttons and several text fields. Pressing
↵ ran a native GET to the same URL, which reloaded the page and lost unsaved
Header/Footer edits. The sale screen's `#split-tender-form` had the same
hidden bug: it reloaded and dropped the payment overlay.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | Low (test gap) | Branch 3 (`requestSubmit()` with no default button and one blocking field) had no test. The test named "single-field form still submits" uses the scan form, which has a `type=submit` Add button **and** two blocking fields (`code` text and `qty` number), so it only exercises branch 1. The Dev/Tester notes describe the scan form as "single field", which is wrong. A regression such as `blockers > 0` would have passed the whole suite. | **Fixed**: renamed that test to "…still submits through its submit button". Added "OSK enter in a single-field form with no submit button still submits": it removes the scan form's submit button, changes `qty` to hidden, then presses ↵, and the POST `/api/pos/scan` must happen (10 s wait). With the mutation `blockers > 0` it fails, and with the real code it passes. |
| 2 | Nit | `input[type=image]` branch is dead code: `form.elements` excludes image buttons (HTML spec), so an image button is never found as the default button. `type === ''` for `<button>` is also unreachable (`button.type` reflects as `submit` for a missing or invalid attribute). No `type="image"` exists in `web/ui` or `web/public`. | Accepted. It is harmless and matches the spec's wording. |
| 3 | Nit | The disabled check is `def.disabled`, which reads `false` when only an ancestor `<fieldset disabled>` disables the button. This is still correct because `HTMLElement.click()` does nothing on an actually-disabled form control, and the Tester's fieldset case pins it. `def.matches(':disabled')` would say the same thing more explicitly. | Accepted. The behaviour is correct and covered by a test. |
| 4 | Info | A typeless `<button>` inside a form is now the default button, so ↵ clicks it, just as a physical Enter does. I scanned every `<form>` in `web/ui` with comments stripped: no form has a typeless button, and no form's first submit button is destructive (translate panel → Save; import → Preview, `set-value:import-commit,0`, which is safer than the old submitter-less submit; record dialogs → Save). No JS-built `<form>` exists in `web/public`. | No action. |
| 5 | Info | When ↵ is pressed in a textarea, it still goes through `implicitSubmit` (before this change: `requestSubmit`). A physical Enter in a textarea inserts a newline. | Out of scope: #6179 owns the textarea ↵ path. |

The Dev audit (AC4) checks out: the receipt designer and split-tender forms now
only close the keyboard. Record dialogs submit through their external Save
(`form="{{ .formID }}"`). Forms with submit listeners all have a real submit
button.

CLAUDE.md rules: no user-facing strings added (i18n n/a). No `showModal`, no
native confirm. Offline-first is unaffected (client-only change, no network
dependence). No CSS, so RTL is n/a. `guard-fixed-dates.sh` exits 0 with the
amended spec.

## TDD re-verification (reviewer, in worktree)

I reverted only `web/public/osk.js` to 8a8dfb9e4 and ran the spec (`--project=default --trace off --workers=1`): **4 failed, 2 passed**, as claimed.
- receipt designer: `expect(__marker6180).toBe('alive')`, received `undefined` (the page reloaded).
- disabled Save (attribute) and disabled Save (fieldset): `a disabled default button must block ↵ entirely`, expected `false`, received `true` (the form POSTed anyway).
- split-tender: marker `undefined` (native GET reload).
- The scan form and record-dialog tests passed before the fix. Both are non-regression guards by design.

After restoring `osk.js`: 6/6 passed. With the reviewer's added test, the 6180
spec has 7 tests, and the new one fails under the `blockers > 0` mutation
(no `/api/pos/scan` response). The final gate is the 6180 spec plus all
`tests/osk-*` specs plus `sale-screen-osk-scan-submit-1177`
(`--project=default --trace off --workers=2`): **62 passed, 0 failed, 0 flaky**.

## Extra verification (Tester, from notes)

The Tester drove a real till: a throwaway till on 127.0.0.1:8195 with a fresh
data dir, auth off and OSK forced on, viewport 1280x800 at DPR 1, in the
default (light) and dark themes.
- On `/receipt-designer`, they typed Header 1 and Footer with OSK keys and
  pressed ↵. The marker survived, there were 0 main-frame navigations, the
  keyboard closed and the values stayed. Save design returned 200 "Design
  saved", and after a reload both values were still there. No page errors.
- Screenshots `rd-6180-default.png` and `rd-6180-dark.png`: keyboard closed,
  nothing overlapping or clipped. The page stays scrolled down from the OSK's
  scroll-into-view, which happened before this change too.
- Settings were reset and the till killed afterwards. The Tester did not
  check RTL, phone or tablet viewports, the WebKit lane, or record dialogs
  other than categories (the others share the same partial).

## Verdict

**Approve**, with the reviewer's spec change (finding 1) ported to the branch.
The `osk.js` change is correct, small, follows the HTML spec, and leaves
textareas alone so it does not conflict with #6179.

## Deferred

- None needing a card. Findings 2 and 3 are accepted nits. Finding 5 is owned by #6179.

Pre-flight: help topic n/a (OSK ↵ now behaves like a physical Enter; I grepped `web/help` for on-screen keyboard / Enter / ↵ and found no topic documenting the OSK ↵ behaviour; `display.md` and `inventory.md` mention the OSK but not ↵); docs-shots n/a (no visual change); reference/ page n/a (no documented OSK ↵ contract); README n/a; en.json / en.meta.json n/a (no keys added); ADR n/a; plugin/manifest mirrors n/a; review record: this file; follow-up cards: none.
