# Review: OSK toggle on the fetch-path manager-PIN dialog (ut-docs#5300)

Branch `fix/5300-osk-elevation-toggle`. Follow-up to ut-docs#5291
(`2026-11-03-elevation-fetch-inflight-5291.md`). Design notes (BA, Architect,
UX) came from a fresh agent. A Sonnet dev agent built the change. An
independent Opus reviewer reviewed the uncommitted diff.

## What shipped

- `web/public/osk.js`: `updateToggles(root)` takes an optional root, which it
  also matches itself. The existing MutationObserver calls it for each added
  element subtree when `enabled` is true. `utPostWithElevation` inserts its
  `#elevation-modal` by hand (`innerHTML` + `replaceWith`/`appendChild`,
  `hx-disable`), so htmx never fires `htmx:load` on it. Before this change its
  `[data-osk-toggle]` stayed `hidden` and the operator had no way to open the
  keyboard for the autofocused PIN field. The `htmx:load` listener is now
  wrapped (`function () { updateToggles(); }`) so the Event is not passed in
  as `root`.
- `e2e/tests/prompt-inflight-fetch-5291.spec.ts`: a new `describe` with
  `hasTouch: true` (osk.js `auto` mode → enabled at parse time). It covers:
  the toggle is visible on the fetch-path dialog (`hx-disable` asserted), it
  opens `#osk`, the PIN field takes focus, an OSK key types `1`, a second tap
  hides it, and Cancel reports `onCancel`. A second test checks that the
  wrong-PIN re-prompt (a second `showDialog`) shows the toggle too.

Design choice: reveal from the central observer (option C) instead of a
synthetic `htmx:load` (it would also run four unrelated listeners and plugin
`htmx.onLoad` callbacks) or a per-caller `utOskInit` (opt-in, the
ut-docs#1022 failure class). This review agrees with that choice.

## Points checked

- **Observer cost.** The observer already runs `guardField` + `guardSweep`
  (a `querySelectorAll`) per added root. The change adds one more
  attribute-selector `querySelectorAll` per added root, and only when
  `enabled` is true. With the OSK off or disabled it costs nothing (one
  boolean check). On the sale screen's frequent swaps, the old `htmx:load`
  path already ran a document-wide query on every swap. The new cost is
  scoped to the subtree and has the same order of magnitude. Setting `hidden`
  is not in `attributeFilter` (`disabled`/`readonly`/`type`), so it cannot
  loop. Acceptable.
- **Can `enabled` go stale?** `mode` is read once from `<body data-osk>` at
  parse time. `enabled` only goes false→true (touchstart, and the ut-docs#1262
  click fallback), and both paths already call `updateToggles()` for the whole
  document. A mode change in Settings takes effect on the next page load. No
  path flips it back to false, so no toggle is left visible while the OSK is
  off.
- **Mode `off`.** The IIFE returns before the observer is registered, so the
  toggles stay `hidden` as shipped. Auto mode on a desktop without touch:
  `enabled` is false and the observer skips the call. No regression.
- **Ordering with `htmx:load`.** For htmx swaps both paths write the same value
  (`hidden = !enabled`). They are idempotent, so the order does not matter.
  For the fetch path the observer runs as a microtask after the synchronous
  insert + `.show()`, so the toggle is revealed before the first paint.
- **Toggle wiring on a `hx-disable` dialog.** Document-level `pointerdown`
  and `pointerup` listeners have no htmx dependency. `t.form.elements` finds
  `override_pin` (hidden inputs are skipped). `setBusy` (#5291) leaves the
  toggle enabled. The test exercises all of this.
- **Repo rules.** No new strings (no i18n or lang-pack impact; `osk.js` is
  scanned by the i18n guard and adds no literals). No `showModal`. No CSS, so
  RTL is not affected. No template change.
- **Manual.** `web/help/*/elevation.md` does not describe the keyboard toggle,
  so no prose is stale. The fix makes the fetch path match the htmx path. No
  screenshot change.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | **The red proof depends on timing.** Before the fix, any htmx swap that adds elements, anywhere on `/`, fires `htmx:load`. The old document-wide `updateToggles()` then reveals the toggle. `toBeVisible()` polls for the 10 s expect timeout (`playwright.config.ts:163`). A status-bar poll that lands inside that window could make the test pass on the unfixed code: `#sb-main-till-mount` runs every 5 s (`base.html:1642`), and others run every 30/60 s. On a standalone till the main-till chip is probably empty, which fires no `htmx:load`, so this is unlikely today. If a poll later starts returning markup, the test would no longer catch a regression. Suggestion: assert right away, e.g. `expect(toggle).toBeVisible({ timeout: 1000 })`, or `expect.poll` on `el.hidden` within a short timeout, so the test checks the observer path and not a later swap. | Fixed: both new toggle assertions use `{ timeout: 1000 }` |
| 2 | nit | `toHaveAttribute('data-osk', /auto|on/)` checks the server mode, not that `hasTouch` actually enabled osk.js. This is harmless: if `hasTouch` did not enable it, `toBeVisible` would fail, so the result can only be a false fail, not a false pass. A tighter precondition would be `page.evaluate(() => navigator.maxTouchPoints > 0)`. | Accepted |
| 3 | nit | Each new test creates a cashier (`cashier5291<RUN>`) and never deletes it on the shared `auth` till. This is the same pattern as the existing four tests in the file (#5291 finding 6, accepted). Nothing in the file asserts on user counts. | Accepted (pre-existing pattern) |
| 4 | nit | The design's optional comment in app.js `showDialog` ("osk.js reveals the toggle via its observer") was not added. A future reader of `showDialog` may wonder why nothing calls the OSK. | Optional |

No blocking findings. No product-code bug was found.

## Verification

- `node --check web/public/osk.js`: ok.
- No other caller passes `updateToggles` as a callback. The only one was the
  `htmx:load` listener, which is now wrapped. Checked by grepping
  `web/public/*.js`.
- The reviewer did not run the e2e suite or the TDD revert. The dev and
  orchestrator should confirm that the new tests fail with the old `osk.js`
  (see finding 1) and pass with the fix, and run `settings-osk.spec.ts` plus
  the `prompt-inflight*` and `elevation*` specs.

Pre-flight: gofmt clean, `go build ./...` ok (JS + e2e only, no Go change); Tester ran 13 ci.yml guards touching web/public (i18n, showmodal, rtl, inline-handlers, osk-loaded …), all exit 0; e2e `--project=auth` 5291+5288 specs 7/7 after the finding-1 fix, `settings-osk` + `osk-central-guard` 23/23; web/help n/a (elevation topic does not describe the toggle; behaviour now matches the htmx path, no screenshot change); reference/ n/a; README n/a; en.json n/a (no new key); review record this file; follow-up: open OSK covers the PIN dialog's Cancel/Confirm row at 1024×600 (pre-existing, filed as a Backlog card).

## Tester (separate Opus agent)

PASS. Red→green re-verified independently (osk.js stashed: both 5300 tests fail with "Received: hidden"; restored: pass). Driven run on a real server, OSK on, touch, 1024×600: the toggle (62×51) shows under the PIN field, opens the number pad with the field focused, PIN typed on the OSK and submitted with ↵, onDone once, dialog closed. Mode `off`, and `auto` on a non-touch desktop: toggle stays hidden. Seen pre-existing: the open OSK covers the dialog's Cancel/Confirm row at 1024×600 (↵ still submits).

## Verdict

Approve. Reviewer: Claude Opus 5.5 (independent; author Sonnet). Before
merging, consider tightening the visibility timeout (finding 1) so the red
proof does not depend on timing.
