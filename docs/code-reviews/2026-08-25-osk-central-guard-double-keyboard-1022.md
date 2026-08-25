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

New tests: `e2e/tests/osk-central-guard.spec.ts` (7 Playwright cases on
`/catalog`, a page with zero prior template guards — exactly the class of
page the fix targets): up-front `inputmode="none"` before any interaction;
a numeric-inputmode text field still gets the numeric OSK layout; a
second tap after visiting another field doesn't let the native keyboard
race back in (the retarget-restore-removal regression); OSK mode off
never forces `inputmode` and leaves real values alone; a field added after
load — both as a bare top-level node and nested inside a wrapper — gets
the same guard; a field added while OSK is disabled is NOT guarded; and a
non-numeric field on a locale `LAYOUTS` has no entry for is left alone
while a numeric field on that same locale still is.

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

Independent Opus subagent, full read of the diff, `autofill.js` (the
sibling pattern the fix follows), the new and pre-existing e2e specs, and
every other page/JS file touching `data-osk`/`oskPrevInputmode`/
`wantsOSK`/`oskmode`. Traced JS execution/hoisting order by hand, traced
`isNumeric()` against `#item-barcode`, traced the MutationObserver against
arbitrary non-input nodes, and manually walked the old (pre-fix) code
against the new regression test to confirm which assertion actually
discriminates old from new.

### Findings — should-fix, all four fixed in this same round

1. **`guardField()` wasn't gated on `enabled`.** The MutationObserver calls
   `guardField(node)` directly on every added node, bypassing
   `guardSweep()`'s own `if (!enabled) return`. On a non-touch till
   (`auto` mode, the default — `enabled` stays `false` until a real touch
   happens), a field arriving as a *top-level* swapped-in node (an htmx
   `outerHTML` swap whose response root is the field itself, an oob swap,
   an Alpine clone) would get `inputmode="none"` forced onto it anyway,
   contradicting the "OSK off/disabled never forces inputmode" invariant.
   **Fix:** `guardField()` now starts with the same `if (!enabled) …
   return` as `guardSweep()`. **New regression test**: "a field added
   directly is NOT guarded while OSK is disabled".
2. **The retarget-restore-removal regression test was a tautology** — it
   passed against the *old*, buggy `osk.js` too, because it never asserted
   the one state that actually differs: the field just left behind must
   *keep* its `inputmode="none"` after focus moves elsewhere. Added that
   assertion. Verified directly: checked out the pre-fix `osk.js` (commit
   `af36f79`) and ran this test against it — it now fails there
   (`Received: ""`, i.e. the attribute really was removed) and passes
   against the fix, confirming it's a real, load-bearing regression guard
   now, not a tautology.
3. **A genuine, fix-introduced capability regression for unsupported
   locales.** `LAYOUTS` only has `en`/`tr`/`fa`/`ar`; `de` (the German
   pilot's own locale, CLAUDE.md) and `es` ship via language plugins with
   no OSK layout at all. Before this fix, an unguarded field's *native*
   keyboard opened at focus time regardless — buggy (two keyboards, or a
   broken WebView layout) but functional: an operator could still type
   "Käse". After the original version of this fix, the native keyboard is
   suppressed everywhere, permanently — for a non-numeric field on an
   unsupported locale that leaves literally no way to type at all,
   strictly worse than the bug being fixed, and squarely in the pilot
   market this repo prioritizes. **Fix:** `guardField()` now skips
   suppression for a non-numeric field when `localeSupported()` is false
   (a new helper checking `LAYOUTS[lang]`), leaving the pre-existing
   (buggy but functional) native-keyboard behavior for exactly those
   fields. Numeric fields are exempt from this check — the `num` layer is
   just digits, usable regardless of locale. `show()` was changed to call
   `guardField(el)` instead of duplicating its body inline, so this check
   applies uniformly whether a field was pre-swept or first touched via a
   direct tap — the inline duplicate would have silently bypassed it.
   **New regression test**: "a non-numeric field is left alone on a locale
   osk.js has no layout for; a numeric field is still guarded" (using
   `?lang=de`, which `httpx.ResolveLocale` accepts unvalidated, so this
   doesn't need the `ut-plugin-language-de` plugin actually installed).
   **Not fixed in this branch, filed as follow-up** (real feature work,
   not a defensive mitigation — a wrong keyboard layout for the German
   pilot is its own kind of user-facing defect and deserves proper design/
   native-speaker review, not a bolt-on to a bug-fix branch):
   ut-docs#1047 "Add de/es OSK layouts".
4. Related, **not fixed in this branch — filed for a product decision**:
   three dialogs autofocus an OSK-able field with no visible
   `data-osk-toggle` affordance (`index.html`'s hold-sale
   `#hold-label-input`, `elevation_prompt.html`'s override-PIN field,
   `pin.html`'s change-PIN fields). Before this fix their *native*
   keyboard opened on that autofocus (buggy, but present); after it,
   nothing shows until the operator taps the field a second time, with no
   visual cue that a tap is needed — a real, if recoverable, discoverability
   regression on a manager-PIN-entry flow. The reviewer's own words: "it
   needs an explicit product call before merge, because it changes the
   manager-override PIN flow on kiosk hardware." Two live options (add a
   toggle to each dialog vs. reconsider ut-docs#155's no-auto-open rule
   for genuinely modal PIN entry) with real security/UX tradeoffs —
   routed to Admin Review rather than guessed: ut-docs#1048.

### Nits — fixed

- `show()` duplicated `guardField()`'s body instead of calling it (also
  now load-bearing for finding 3, see above, not just a style nit).
- The comment justifying `index.html`'s static template guard overstated
  autofocus-vs-`defer` ordering (per HTML, a deferred script usually runs
  *before* autofocus is applied, not after) — reworded to the real reason:
  `osk.js` is a separate, arbitrarily-late network fetch
  (`settings-osk.spec.ts` deliberately delays it 700ms in one spec).
- `hide()`'s comment claimed OSK mode "only changes via a full reload" —
  true for `enabled` true→false, not for the `touchstart`-driven
  false→true path; reworded to cover both directions.
- Added a short comment on the MutationObserver's `childList`-only scope
  (a field that becomes OSK-eligible via an in-place attribute change,
  never actually reached in this codebase today, stays unguarded) —
  documented rather than fixed, matching `autofill.js`'s own identical,
  pre-existing gap.

### Confirmed clean (no changes needed)

JS hoisting/execution order (the `oskGuarded`/`LAYOUTS` var-vs-function
distinction is sound, and both are now declared together at the top of the
file for the same reason); `isNumeric()`'s correctness for
`#item-barcode` and for a field with no original `inputmode`;
`wantsOSK()`'s safety when called on arbitrary non-input nodes from the
observer; no infinite loops or realistic performance concern (same shape
as the already-shipped `autofill.js`); `WeakSet` lifecycle across
htmx-swapped and preserved nodes (only a risk under htmx morph/`hx-boost`,
neither used anywhere in this repo); no new user-facing string, so
`guard-i18n.sh`'s `web/ui/**`-only scope isn't an evasion — there's
nothing in scope for it to check; `web/help/en/display.md`'s existing OSK
description remains accurate. Fidelity to `autofill.js`'s
`suppress()`/`sweep()` idiom confirmed structurally faithful — the one
divergence (finding 1) is exactly where the copy went subtly wrong.

### Verdict

Ship. All should-fix items addressed in this same round (three by code
fix + new regression test, one by filing a scoped follow-up for real
feature work rather than rushing a keyboard layout). Findings 3 and 4 are
genuine consequences of an otherwise-correct fix, not fixed silently:
3 is defensively mitigated now (no regression shipped) with a tracked
follow-up for the real fix; 4 is routed to Admin Review rather than
decided unilaterally, per this pipeline's standing hard-stop rule for
anything touching a manager/security-adjacent kiosk flow.
