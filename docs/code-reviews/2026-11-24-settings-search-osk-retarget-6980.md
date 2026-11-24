# Review: Settings search takes one char on the phone OSK (ut-docs#6980)

Pre-flight: till user-visible behaviour: `web/help/img/manifest.json` surface hash updated (osk.js changes no pixel; docs-shots guard green); help topic n/a (bug fix, no documented behaviour changes); reference/README n/a; en.json keys n/a (none); my./admin. UI n/a; review record added; no follow-up work.

## What shipped
`web/public/osk.js` focusin handler: when the keyboard is open and focus moves to a different OSK-able field (script `.focus()`, Tab), `show()` retargets `current`, so keys follow the focus. Before, the Settings grid search handed its query to the sidebar search and focused it, but keys kept going to the hidden grid field, so each letter replaced the last. New spec `e2e/tests/settings-search-osk-phone-6980.spec.ts` (grid search and in-category search).

## Review (independent, by the reviewer agent; Tester notes in temp/pair-fix-1123/tester-search.md)
Paths walked:
- Retarget guard: requires `wantsOSK(target)`, `target !== current`, `current` set and `osk-open`. A closed keyboard or a `hide()`-cleared `current` (the Enter key path) never retargets, so opening stays click-only (#155).
- Key-tap refocus (#3274): the refocus targets `current` itself, so `ev.target === current` and nothing retargets. No loop.
- Re-tap on the same field: unchanged early return in `show()`.
- User tap from field A to field B: focusin now calls `show(B)`, then the click calls it again and hits the early return. Risk was the numeric select-all (#2815) happening before caret placement. Probed (temporary spec, removed) in chromium: text field then tap on filled `#item-price` leaves selection 0..4 and one key press gives "6", not "4.006". Not probed on real iOS (owner devices untouched).
- Tab between fields: keyboard now follows the focus (was a latent bug of the same class), layer resets per field type.
- Scroll: `show()` schedules `scrollIntoView`/`clearKeyboard` for the new field, guarded by `current !== el`.

## Findings
None blocking.
- Info (accepted): the in-category spec passes without the fix (that path was not broken); it stays as a regression guard.
- Info (accepted): on a real iOS tap, focus lands at click time so select-all ordering differs slightly from desktop; the click's `show()` is the same early return, and webkit-phone lane is green. Worth a glance on a device by the owner, not a blocker.

## Extra verification
- Red without fix: osk.js reverted to HEAD, `npx playwright test -c playwright.webkit.config.ts settings-search-osk`: grid test fails `Expected "tap", Received "p"`; restored: 2 passed.
- `scripts/ci/run-docs-shots-guard.sh` green with the manifest hash.
- Tester: build/vet/test, chromium osk|settings|search 241 passed, webkit-phone 264 passed. Unrelated pre-existing failure: settings-tablet-portrait-4628 (600x960 de/fa, `diagnostics-log-folder` overflow), reproduced on pristine HEAD; deferred, needs its own card if none exists.

## Verdict
Approve. No code changes made by the review. Nothing committed.
