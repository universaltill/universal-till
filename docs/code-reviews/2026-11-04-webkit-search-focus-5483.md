# Review: WebKit search-field focus on the phone sale screen (ut-docs#5483)

Branch `fix/5483-webkit-search-focus`, PR universaltill/universal-till#2357,
base `origin/main`. Author: Dev (Sonnet); independent review: Opus 5.5.

## What shipped

- `web/ui/partials/buttons.html`, `openSearch()`: still sets `searching` and
  focuses `$refs.search` in `$nextTick`. If `document.activeElement` is not
  the field afterwards, it retries once in `requestAnimationFrame`, and only
  while `this.searching` is still true. The `webkit-phone` lane saw WebKit
  26.5 leave the field visible but unfocused (the field was shown, so
  `openSearch()` did run). A Go-template comment `{{/* */}}` explains the
  retry. It renders to nothing inside the Alpine object literal, the same
  pattern the `init()` block above uses (line 311).
- `web/help/img/manifest.json`: a docs-shots surface-hash refresh. The
  change is focus-only and changes no pixels, which is what the guard asks
  for in that case.
- The TEMP `e2e.yml` repeat-loop commit (18c2bc7) and its revert (80d64df)
  cancel out.

The net diff no longer touches `e2e/tests/phone-sell-3059.spec.ts`. See
finding 1.

Pre-flight: till user-visible behaviour is unchanged. The search field
focuses on tap as before, now reliably in WebKit. So the `web/help/` topic is
n/a, with no prose change. Docs-shots are updated through the surface hash
only, and `guard-docs-shots.sh` passes. The `reference/` page and `README.md`
are n/a. There is no `en.json` key, so i18n meta and lang packs are n/a. No
plan, platform, ADR or plugin surface changed. This review record is updated.

## Findings

1. MINOR, **fixed** (Tester finding). The spec's new
   `await expect(input).not.toHaveAttribute('x-cloak', /.*/)` wait did
   nothing, and its comment claimed a race that cannot happen.
   - Alpine loads with plain `<script defer>` (`web/ui/layouts/base.html:1399`)
     and auto-starts. No `Alpine.start` or `deferLoadingAlpine` exists in
     `web/public`. So Alpine initialises before `DOMContentLoaded`, and
     `page.goto('/')` waits for `load`.
   - The Tester's observer also saw x-cloak already gone at DCL.
   - A wait that is always met, next to a comment that names a "tap lost
     before Alpine binds" gap, would mislead the next reader into thinking
     that gap is real and covered.
   - Fix: the spec is restored to `origin/main`. The real regression
     assertion is the existing `await expect(input).toBeFocused()`.
2. NIT, **accepted**. Could the rAF retry steal focus? It cannot after
   `closeSearch()`, because the retry is gated on `this.searching`, which
   `closeSearch()` clears synchronously. The retry also only runs when the
   first `focus()` did not take. That leaves the user moving focus to
   something else in the one frame between the two calls, which is not a
   realistic touch or keyboard sequence. A further `activeElement` guard
   would add code for no observable case.
3. CLAUDE.md checks: there are no user-facing strings, so i18n is n/a. No
   CSS changed, so RTL is n/a. There is no `showModal()` and no network
   dependency, so the change is offline-safe. There are no inline handlers
   beyond the existing Alpine component. No findings.

## Verification (reviewer, on the working tree after finding 1's fix)

- `gofmt -l .` produced no output, and `go build ./...` passed.
- `bash scripts/ci/guard-docs-shots.sh` reported 32 topics fresh, rc=0.
  `buttons.html` was not changed by the reviewer, so the surface hash
  still matches.
- Chromium: `npx playwright test tests/phone-sell-3059.spec.ts
  --repeat-each 5 --workers 2` ran **95 passed** (19 tests × 5). It renders
  the real template, so the Alpine `x-data` with the template comment
  evaluates cleanly.
- TDD re-check (inline, then restored): I removed both `focus()` calls from
  `openSearch()` and ran `search and edit … --retries 0`. It **failed** with
  `toBeFocused(): Expected focused / Received inactive`. I then restored the
  file, and the earlier run passed. The Tester's mutation B (first `focus()`
  removed, rAF retry kept, still passes) shows the retry path works on its
  own.
- WebKit cannot be installed locally (proxy). CI evidence on 18c2bc7, the
  TEMP loop: the target test ran 20× with `--retries 0` and **20 passed**,
  and the full `webkit-phone` suite had **237 passed**. The fix to finding 1
  only removes a wait that was always met, so that evidence still holds.

## Tester visual attestation (summary)

- Driven at 390×844 and 1280×800, with OSK `auto` and `on`. A tap focuses
  the field, typing filters the tiles, and Escape closes it and returns
  focus to the search button.
- With `inputmode="none"` (osk.js), the OSK opens when the field itself is
  tapped, not on programmatic focus. The parent commit behaves the same, so
  this is no regression.
- 11 other search specs passed in Chromium, and all CLAUDE.md guards had
  rc=0.
- Not looked at: dark, RTL and long translations. The change is behaviour
  only, with no layout change.

## Verdict

**Approve**, with finding 1 fixed in the working tree (spec reverted to
main). Nothing is deferred.

Reviewer model: Opus 5.5.
