# Code review: OSK central guard — double-keyboard fix (ut-docs#1022)

**Branch:** `fix/1022-osk-double-keyboard`
**Reviewer:** independent Opus subagent (complexity:medium → Opus review,
per scrum-master's model routing) · **Author:** Sonnet (this pipeline
cycle, inline)

## What shipped

`web/public/osk.js` (the custom on-screen keyboard for touch kiosk tills)
used to suppress the native OS keyboard reactively, inside `show(el)` —
which runs from a `click`, by which point the browser has already decided,
at focus time, whether to open its own IME. On the 28 of 30 pages with no
per-template guard, the first tap on any text field opened **both**
keyboards.

The fix (`web/public/osk.js`):

- A new `guardField(el)`/`guardSweep(root)` pair sweeps every OSK-able
  field and sets `inputmode="none"` on it **up front**, before any
  interaction — at parse time, on every `htmx:afterSwap`, and via a
  `MutationObserver` for any other DOM mutation (Alpine, plugin content).
  Same belt-and-braces pattern as the existing `web/public/autofill.js`'s
  `suppress()`/`sweep()`, for the same reason: a per-template opt-in
  silently regresses the moment a 29th page forgets it. The field's real
  original `inputmode` is saved to `el.dataset.oskPrevInputmode` first.
- `isNumeric(el)` now reads the saved original inputmode
  (`dataset.oskPrevInputmode`) instead of the live attribute, which the
  guard has usually already overwritten to `"none"` — otherwise every
  `inputmode="numeric"` text field (e.g. `catalog.html`'s barcode field)
  would silently fall back to the letter layout.
- `restoreInputmode()` and its two call sites (`show()`'s retarget path,
  `hide()`) are removed. The old code released a field's `inputmode`
  override when focus moved elsewhere, to avoid leaving it permanently
  "none"; with the new design that override is *meant* to be permanent
  for the life of the page while OSK is enabled, so restoring it on every
  blur would silently reopen the exact race this fix closes, on a field's
  **second** tap. The only way OSK mode changes at all is `settings.html`'s
  full page reload, which never applies the override when the new mode is
  `"off"` — nothing left to restore.
- `index.html`'s scan-row field keeps its own static
  `{{ if ne (oskmode) "off" }}inputmode="none"{{ end }}` template guard —
  its `autofocus` fires before any deferred script, including this one,
  can run at all, so it's the one field the new sweep structurally cannot
  reach in time.

New tests: `e2e/tests/osk-central-guard.spec.ts` (5 Playwright cases on
`/catalog`, a page with zero prior template guards — exactly the class of
page the fix targets): up-front `inputmode="none"` before any interaction;
a numeric-inputmode text field still gets the numeric OSK layout; a
second tap after visiting another field doesn't let the native keyboard
race back in (the retarget-restore-removal regression); OSK mode off
never forces `inputmode` and leaves real values alone; a field added after
load (simulated DOM mutation) gets the same guard.

Pre-existing `e2e/tests/settings-osk.spec.ts` (5 cases) and the two Go
tests (`TestIndexScanRowKeyboardIsOnDemand`,
`TestOSKModeReachesThePage`) all still pass unmodified — no template or
Go-side change in this diff.

## Verification

- `gofmt -l .` empty, `go build ./...` clean, `go vet ./...` clean, full
  `go test ./...` green (no Go source touched by this fix at all — pure
  `web/public/osk.js` + a new e2e spec).
- All 16 CI-blocking guards pass, including `guard-docs-shots.sh`
  (`web/public/**` changed, so `make docs-shots` was re-run — the surface
  hash moved but no screenshot PNG changed, confirming the fix has no
  visible rendering effect) and `guard-i18n.sh` (no new user-facing
  strings — this is pure behavior, no template/copy change).
- Full e2e suite run locally (182 tests, single worker): 174 passed, 6
  skipped (an unrelated `auth`-project Chromium executable-path mismatch
  specific to this sandbox — `login.spec.ts`, not exercised by this
  change), 1 pre-existing failure confirmed **unrelated** below.
- `osk-central-guard.spec.ts` (all 5) and `settings-osk.spec.ts` (all 5)
  pass together in the same run, confirming no regression to the existing
  OSK behavior this fix builds on top of.

### Pre-existing, unrelated failure ruled out

`catalog-image-to-till.spec.ts` (an image-upload/thumbnail-loading test,
no OSK/keyboard/inputmode interaction anywhere in it) fails in this
sandbox both **with** and **without** this fix applied — confirmed by
`git stash`-ing `web/public/osk.js` back to its pre-fix state and
re-running the spec against a fresh server: identical failure
(`img.thumb.complete` stays `false`). Sandbox-specific (likely headless
Chromium image-decode timing in this environment), not caused by this
change; out of scope for #1022.

## Independent review

<!-- filled in after the Opus subagent's pass -->
