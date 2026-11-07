# Review: keyboard focus kept after an hx-disabled-elt request (ut-docs#5749)

Date: 2026-11-07 · Branch: `fix/5749-htmx-disabled-focus` · Complexity: easy
Author: Sonnet (Dev subagent) · Reviewer: Opus (fresh-context subagent) ·
Tester: Sonnet (separate subagent)

## What shipped

- `web/public/app.js`: one shared hook. On `htmx:beforeRequest` it records the
  focused control (per xhr, `WeakMap`) when the request comes from inside an
  `hx-disabled-elt` scope and that element contains the focus. On
  `htmx:afterRequest` — htmx 1.9.12 re-enables disabled elements just before
  it, on load, error, abort and timeout alike (checked in the vendored
  source) — and only when focus fell to `<body>`, it focuses the control
  again; if the control is gone or can't take focus (e.g. inside a dialog
  that just closed), it focuses the result region instead (the target's
  live region, else the target; an outerHTML swap that replaced the target is
  found again by id), with a temporary `tabindex="-1"` removed on blur.
- `e2e/tests/htmx-disabled-focus-5749.spec.ts`: voucher adjust (400) and
  top-up (400) keep focus on the submit after Enter; moved-on guard (focus
  moved mid-request is not stolen); swapped-away control → live region;
  outerHTML self-replacing target → the new region, tabindex removed on blur.
  Covers the voucher "Print slip" button generically once ut-docs#5740 lands.

## Findings (Opus review)

1. Minor — fallback missed an outerHTML swap that replaces its own target
   (agent confirm/cancel in `pluginview/document.html`): both the trigger and
   the stored target were detached. **Fixed** (re-lookup by id) + test; the
   new test fails without the fix (`Expected: focused / Received: inactive`).
2. Minor — `focus()` on a control inside a just-closed dialog silently does
   nothing, so the fallback never ran. **Fixed** (check `activeElement`
   after focusing, fall through to the region).
3. Minor — `tabindex=-1` left on the target permanently, and the target is
   not always the live region. **Fixed** (prefer the closest
   `[aria-live]`/`status`/`alert`; remove the added tabindex on blur).
4. Spec used `describe.serial`, so one failure hid the rest. **Fixed**: each
   test seeds the voucher itself when its till lacks it.
5. Noted, accepted — a swap delayed past `afterRequest` (`swap:Nms`, view
   transitions) would undo the restore; neither is used in the repo
   (grep), stated in the code comment.
6. No conflict with the ut-docs#4379 chat refocus (runs first in
   `afterSwap`; this hook then sees a non-body focus and does nothing).
   OSK: programmatic focus never opens it (`osk.js` opens on click only).

## Verification

- TDD: adjust/top-up/swapped-away tests red on unmodified `app.js`
  (focus on body), green after; Tester repeated the stash false-pass check
  independently.
- Driven run (Tester, Chromium 1024×700): Tab to "Add to sale", Enter with
  amount 0 → 400 inline message, `document.activeElement` still the button,
  focus ring visible. Touch-emulated (hasTouch) tap: no OSK, viewport
  unchanged. Emulated only — no real touch hardware or screen reader.
- Regression: all voucher specs + `plugin-chat-focus-4379` + this spec, 74
  passed (Tester); after the review fixes this spec + 4252 + 4379, 15 passed.
- Guards: every `ci.yml` build-job guard run locally passes except two
  environment-only ones (`guard-deadcode-baseline.sh`: local deadcode built
  with Go 1.26 vs module go1.27; `guard-shellcheck-version.sh`: no
  shellcheck binary) — neither touches a JS-only change; CI runs both.
  `node --check web/public/app.js` clean. No Go change.

Pre-flight: help topic n/a (no help topic describes focus after a form
submit; no visible change beyond keeping keyboard focus); docs-shots n/a
(nothing visual changes); `reference/` n/a; README n/a; `en.json` n/a (no
strings); review record updated (this file); follow-ups: none.

Verdict: approve.
