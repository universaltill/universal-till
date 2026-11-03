# Review: manager-PIN dialog stays above the on-screen keyboard (ut-docs#5304)

Date: 2026-11-03 · Branch: `fix/5304-pin-dialog-above-osk` · Lane: `lane:cloud-41`
Author: Sonnet (Dev subagent) · Reviewer: Opus 5.5 (independent subagent, isolated worktree)

## What shipped

- On a 1024×600 till, the OSK used to cover the Cancel/Confirm row of `#elevation-modal`.
  `web/public/app.css` now has two rules that apply only under `body.osk-padded`:
  - `#elevation-modal[open]` is anchored at the top (`.5rem`) and capped to
    `100dvh - var(--osk-reserved-height, 17rem) - 1rem`, with `overflow-y: auto`.
  - `.modifier-actions` is sticky at the dialog's bottom edge, on `background: Canvas`.
  These follow the `--osk-reserved-height` precedent of `.item-form-modal`, `.payment-overlay`
  and `.record-dialog` (ut-docs#1998). They use logical properties only, and nothing changes
  while the OSK is closed.
- The fix covers both the htmx prompt and the `utPostWithElevation` fetch path. Both render
  the same dialog, so no JS change was needed.
- New e2e spec `e2e/tests/pin-dialog-above-osk-5304.spec.ts` has 4 tests at 1024×600 with touch:
  the fetch path, the htmx path, the wrong-PIN re-prompt, and OSK close/reopen.
  - Each test checks that Cancel and Confirm sit above the live `#osk` top.
  - `elementFromPoint` must hit each button, and Cancel gets a real tap (no `force`).
  - The spec is registered in `AUTH_ONLY_SPECS`.
- `web/help/img/manifest.json`: only the docs-shots surface hash changed. The harness never
  opens the OSK, so no screenshot changes (`Docs-Shots-Unchanged: true`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | low (cosmetic) | Sticky row used `var(--surface, Canvas)`. The dialog paints the browser's own `Canvas`, and in `dark.css` `--surface` (#111827) differs from it, so a bluish strip showed. Found by the Tester and confirmed by the Reviewer. | **Fixed:** `background: Canvas`. |
| F2 | process | The surface-hash refresh needs a `Docs-Shots-Unchanged: true` trailer and a recompute after F1. | **Fixed:** recomputed, and the trailer is on the commit. |
| F3 | nit | While the OSK is open, the OSK toggle and the "Invalid PIN" line can sit under the sticky row until you scroll. The PIN input stays reachable, which the spec asserts. | Accepted (sticky-footer behaviour). |
| — | out of scope | Other 8vh fixed-frame dialogs (`#hold-modal`, `#pfand-modal`, `#table-add-modal`, `#parked-orders-modal`, …) may share the bug. | Deferred → ut-docs#5309 |
| — | out of scope | `.gift-voucher-popup .modifier-actions` has the same `--surface`-vs-`Canvas` dark-theme strip. | Deferred → ut-docs#5310 |

## Verification

- **TDD (Dev):** with the CSS hunk reverted, all 4 tests fail. With it applied, all 4 pass.
- **Tester:** repeated the revert check independently. Driven run at 1024×600 in en (fetch,
  htmx, wrong-PIN), tr, ar, fa (RTL mirrors correctly) and dark, plus 360×740. Screenshots
  were looked at.
  - With the OSK open, the dialog bottom is at 303px and the OSK top at 311.8px.
  - With the OSK closed, the dialog is unchanged at 48px.
  - 118 neighbouring specs passed (prompt-inflight-*, osk-*, payment-overlay-osk, …).
  - Not covered: German, because no de pack is installed in this environment.
- **Reviewer:** repeated the revert check and passed. With only the sticky rule removed,
  the wrong-PIN test fails, so the spec catches that half of the fix too.
- **After F1:** `pin-dialog-above-osk-5304` and `prompt-inflight-*` gave 11 passed.
  guard-rtl-logical-css, guard-no-showmodal, guard-i18n and guard-docs-shots all pass.
  No Go change was made, so the Go gate does not apply.
- **Limits:** Chromium touch emulation only. This was not tried on real Pi touch hardware.

Pre-flight: till user-visible behaviour → `web/help/` n/a (no manual prose describes the keyboard/dialog overlap); docs-shots updated (surface hash only, no pixel change); `reference/` n/a; README n/a (not stale); en.json keys n/a (no new strings); review record updated (this file); follow-ups deferred → ut-docs#5309, ut-docs#5310.

## Verdict

Approve. Merge once CI is green on the rebased head.
