# Review: record-dialog e2e specs, same-URL `waitForURL` load race (ut-docs#4427)

Branch `test/4427-record-dialog-load-race`. Author: Sonnet (Dev subagent,
lane:cloud-41, complexity:easy). Orchestrator and Tester: Opus 5.5.
Independent review: Opus, fresh context.

## What happened

- Record-dialog mutations answer `HX-Redirect` back to the URL the page is
  already at, which is a real navigation.
- Six specs awaited `page.waitForURL(<same url>)`. That resolves at once
  against the OLD document, so the next click could land before the deferred
  `web/public/record-dialog.js` bound its click listener, and was silently
  dropped. This is the race of ut-docs#2345 and ut-docs#4421.
- The #4421 proof run saw two failures in
  `fiscal-register-record-dialog-2186.spec.ts` under parallel load.

## What shipped (test-only)

- `clickThenReload(page, click)` is now one shared helper in
  `e2e/tests/helpers.ts`. It awaits the new document's `load`, with the
  listener registered before the click.
- 27 same-URL `waitForURL` waits now use it:
  - `registers-record-dialog-2185`: 5
  - `country-settings-record-dialog-2187`: 5 (the redirect carries `?all=1`)
  - `fiscal-register-record-dialog-2186`: 5
  - `fiscal-register-address-form-overflow-2420`: 3
  - `locations-registers-auth-901`: 8. Its six follow-up
    `waitForLoadState('load')` lines are removed: they ran against the old,
    already-loaded document, so they guarded nothing.
  - `categories-editor-2284`: 1
- Six local copies of the same pattern now import the helper:
  `categories-record-dialog-2010`, `category-list-drag-2699`,
  `icon-library-2506`, `photo-cutout-3127`, `category-image-2500` and
  `locations-record-dialog-2124`.
- `locations-record-dialog-2124.spec.ts` got its own local copy from
  universal-till#1974 (ut-docs#4421), which merged while this PR was in CI.
  After rebasing, that copy also imports the helper now. With
  `--repeat-each=3`, 15 passed.

## Verification

- **Forced reproduction.** Throwaway copies of the six specs routed
  `**/record-dialog.js*` through a 1.5 s delay, with `--workers=4` and
  `--retries=0`.
  - Specs from `origin/main`: 9 of 23 failed. Every one of the six files
    had at least one failure, and each showed the card's symptom (the
    dialog was not visible after a row click).
  - Fixed specs: 23 of 23 passed.
  - The copies were deleted afterwards and are not committed.
- **Natural parallel load.** The eight changed record-dialog specs ran
  unmodified with `--workers=4 --repeat-each=3 --retries=0`: 174 passed.
- **After the review fixes.** `icon-library-2506`, `category-image-2500`,
  `fiscal-register-record-dialog-2186` and `photo-cutout-3127` (the
  `cutout` project) ran: 27 passed.
- **Guards:** `guard-e2e-fixtures-import`, `guard-no-inline-handlers`,
  `guard-osk-loaded` and `guard-autofill-suppression` all pass. No Go
  code changed.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| minor | The helper's comment said "every record-dialog mutation". A refused (4xx) or confirm-dismissed click never fires `load`, so using the helper there waits out the whole timeout. | Fixed: the comment now says the helper is for successful, navigating clicks only. The review confirmed that no converted call is on a refusal path. |
| nit | Three more inline copies of the pattern: `icon-library-2506`, `photo-cutout-3127` and `category-image-2500`. | Fixed: they now use the helper. |
| nit | A comment in 2186 still blamed `htmx:afterSwap` for the settle time after a full page load. | Fixed: it is reworded to post-load async work. |

The reviewer also confirmed these points:
- Every converted call is a same-URL redirect. It checked each handler's
  `HX-Redirect` target in `internal/pages/*.go`.
- Every `hx-confirm` click registers `page.once('dialog', accept)` before
  the click.
- `record-dialog.js` is `defer`, so it runs before `load`.

## Deferred

- Specs on `/tables` and `/promotions` still await a same-URL `waitForURL`
  after a navigating submit. These pages do not use the record dialog, so
  this is an audit rather than a confirmed flake. Filed as ut-docs#4440.

## Verdict

Safe to merge.
