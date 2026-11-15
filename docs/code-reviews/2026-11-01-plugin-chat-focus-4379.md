# Review: plugin chat — focus back to the question box, shorter phone transcript (ut-docs#4379)

Date: 2026-11-01 · Lane: `lane:cloud-41` · Branch: `fix/4379-plugin-chat-focus`

## What shipped

- `web/public/app.js`: a chat question the operator sends marks its htmx
  target (`data-ut-chat-refocus`). When the swap that ends the thinking
  bubble lands, focus goes back to the question box
  (`focus({ preventScroll: true })`), unless the operator has moved to
  another control meanwhile. A first load, or a poll that nobody asked a
  question for, never takes focus. A failed question (non-2xx, including
  the #3879 `HX-Reswap: none` path) or a request that never swapped
  clears the mark and leaves focus alone.
- `web/public/app.css`: under the 480px phone breakpoint the transcript
  cap is `min(40vh, 18rem)` instead of `min(60vh, 32rem)`, so the
  question row stays on screen at 360×800. Above the breakpoint nothing
  changes (1024×600 stays at 360px).
- `e2e/tests/plugin-chat-focus-4379.spec.ts`: 5 Playwright tests. They
  mount the component's markup on a real till page (real htmx, `app.js`
  and `app.css`) and answer with `page.route`, because no e2e plugin has a
  chat view yet (#4378).
- `web/help/img/manifest.json`: docs-shots surface hash only. No
  screenshotted page shows the chat.

Item 3 of the card (the thinking bubble re-announces the transcript to
screen readers) is out of scope. The card defers it to #2840 (streaming).

## Independent review (Fable, different model from the builder)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A failed question (#3879 `HX-Reswap: none` + non-2xx, force-swapped by app.js) still fires `afterSwap`. The refocus then scrolled the question box back into view and pushed the "action failed" alert off screen again. | **Fixed:** bail out and clear the mark on `xhr.status >= 400`; `preventScroll: true`. New test "a failed question clears the mark" (red without the fix, green with it). |
| 2 | minor | The mark leaked on a request that never swaps (network drop, plain 5xx) and could take focus on a later unrelated swap. | **Fixed:** `htmx:afterRequest` with `!successful` clears it. |
| 3 | minor | The "moved to another control" test raced the 1s poll and could false-pass. | **Fixed:** the answer is held until the test has moved focus. |
| 4 | nit | The surface-hash commit needs the `Docs-Shots-Unchanged: true` trailer. | **Fixed** in the commit message. |
| 5 | nit | On OSK tills a programmatic `focus()` never opens the keyboard (osk.js, #155). The gain is for physical keyboards and phones. | Accepted. This is by design and is noted on the card. |

## Verified

- TDD: with the listeners and CSS reverted, "focus returns" and the
  "360px phone" test fail. With the fix, all pass. With only the
  failure-path guards reverted, "a failed question" fails. Restored, and
  5/5 pass.
- Guards: `guard-docs-shots`, `guard-i18n`, `guard-rtl-logical-css`,
  `guard-no-showmodal` all pass; `node --check web/public/app.js` passes.
  The full `ci.yml` guard sweep passed except `guard-deadcode-baseline`
  (the container's Go 1.26 can't load the go1.27 module) and
  `guard-shellcheck-version` (no shellcheck binary). Both are tooling gaps
  in this container, not caused by the diff; CI runs them.
- No Go code changed.

## Verdict

Safe to merge once CI is green.
