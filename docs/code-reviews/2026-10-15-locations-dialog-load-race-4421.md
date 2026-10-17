# Review: locations record-dialog e2e flake (ut-docs#4421)

Branch `test/4421-locations-dialog-load-race`. Author: Opus 5.5
(lane:cloud-41, complexity:easy). Independent review: Sonnet, fresh
context. Fable was routed first but refused (out of usage credits), so
the review fell back to Sonnet per MODEL-ROUTING.md.

## What happened

- `e2e/tests/locations-record-dialog-2124.spec.ts` "kiosk floor 1024x600"
  failed about 1 in 61 under parallel load: `#location-dialog` stayed
  hidden after a row click. It failed on `main` too (found during #4078).

## Root cause

- Every successful Locations mutation answers `HX-Redirect`
  (`internal/pages/locations_page.go`), which is a real navigation back to
  `/locations`.
- The spec awaited `page.waitForURL(/\/locations$/)`. The page is already
  at `/locations`, so this resolved at once against the OLD document and
  guarded nothing.
- The next row click could then land in the NEW document before the
  deferred `web/public/record-dialog.js` had bound its document click
  listener. The click was silently dropped. This is the same race as
  ut-docs#2345 in `categories-record-dialog-2010.spec.ts`.

## What shipped (test-only)

- The `clickThenReload` helper from the #2345 precedent: await the new
  document's `load`, registered before the click. Deferred scripts run
  before `load`.
- All four navigating clicks in the spec use it: create, rename,
  deactivate and activate.
- The refused-mutation test is left as is, because a 400 does not
  navigate.

## Verification

- **Forced reproduction.** A throwaway copy of the spec routed
  `**/record-dialog.js*` through a 1.5 s delay.
  - Old spec: 3 of 5 failed with the card's exact symptom (`toBeVisible`
    after the row click). That covers kiosk 1024x600, phone 360px and the
    rename round-trip.
  - Fixed spec: 5 of 5 passed.
  - The copies were deleted afterwards and are not committed.
- **Natural parallel load.**
  - Baseline on unchanged `main`: the five `*record-dialog*` specs,
    `--workers=4 --repeat-each=6`, 264 passed. The 1-in-61 flake did not
    show in that sample, which is why the forced reproduction is the
    proof.
  - After the fix: the same set with `--repeat-each=4`. Result recorded on
    the card.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| minor | If a deactivate is refused (`in_use`/`last_location`) or the confirm is not accepted, `waitForEvent('load')` waits until the test timeout. The old code would have failed one line later. | Accepted: the test fails either way, and this matches the precedent. |
| nit | The single-line lambda calls run past 120 characters. | Accepted: same shape as the 2010 spec. |

The reviewer confirmed the following:
- `load` is main-frame only.
- The old document's `load` had already fired before the click.
- The listener is registered before the click.
- No other navigating click in the spec was missed.

## Deferred

These sibling specs use the same `waitForURL`-after-`HX-Redirect`
pattern: `registers-record-dialog-2185`,
`country-settings-record-dialog-2187`,
`fiscal-register-record-dialog-2186`,
`fiscal-register-address-form-overflow-2420`,
`locations-registers-auth-901` and `categories-editor-2284`. They are
filed as a follow-up Backlog card.

## Verdict

Safe to merge.
