# Code review — iOS on-screen keyboard closes after every letter (ut-docs#3274)

**Date:** 2026-09-30 · **Branch:** `fix/3274-ios-osk-keeps-focus` · **Reviewer:** independent subagent (Fable 5.1; code written by Opus 5.5)

## What shipped
- `web/public/osk.js`: the till's own keyboard kept focus in the field only by cancelling each key's `pointerdown`. Chromium (Android WebView) and WebKitGTK (Pi kiosk) honour that. iOS WebKit moves focus from its tap gesture *after* `pointerup` (synthetic mousedown), so the field blurred, `focusout` hid the keyboard, and the operator had to re-tap the field for every character.
  1. `#osk` also cancels `mousedown`.
  2. `focusout` keep-focus path: a blur of the field being typed into, landing nowhere (or on `#osk`), within 800 ms of a key tap, re-focuses the field (`preventScroll`) on the next tick instead of letting the keyboard close.
  3. (review fix) any `pointerdown` outside `#osk` disarms the path, so only a blur whose most recent press was a key is reclaimed.
- `e2e/tests/osk-ios-keeps-focus-3274.spec.ts`: drives iOS's event order by hand (no iOS engine in CI): cancelled mousedown; bare blur after a key across a whole word; a tap outside the keyboard right after a key still closes it.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Time window was the only discriminator: a tap on Save/background within 800 ms of a key kept the keyboard open on every engine (probed in Chromium). | **Fixed**: `pointerdown` outside `#osk` disarms; new test 3 fails without it (verified: 1 failed / 2 passed), passes with it. |
| 2 | Low | Old test 3 tested a timeout, not a tap. | **Fixed**: replaced by the iOS-order outside-tap test. |
| 3 | Low | Recovery relies on programmatic `focus()` from `setTimeout(0)` in WKWebView. Expected to work (the field is `inputmode=none`; only the native keyboard is gesture-gated). | **Accepted**, needs device evidence: owner's TestFlight build. |

Checked with no problem: ↵ key (hide() clears `current` before its blur), tapping another field (relatedTarget set), base.html/record-dialog focus traps (`#osk` allowed), htmx removal (`isConnected`), number/email fields, window blur, Android/WebKitGTK activation still on `pointerup` (#1219).

## Verification
- TDD: new spec red on main (2 failed: `#osk` hidden after the first key), green with the fix; independently re-run by the reviewer (3 passed / 2 failed / 3 passed).
- Full `default` e2e project: 837 passed (before the review fix); all OSK specs after it: 67 passed. `guard-i18n.sh` clean. JS only, no Go change.
- **Not verified on real iOS hardware** (no Xcode on the dev Mac). The card stays open until the owner confirms on iPhone + iPad (TestFlight).

## Verdict
Safe to merge. Device confirmation pending.
