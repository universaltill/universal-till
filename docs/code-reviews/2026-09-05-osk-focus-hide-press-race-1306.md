# Code review: osk.js focusin/focusout hide() press race (ut-docs#1306)

**Branch:** `pipeline/1306-osk-focus-hide-press-race`
**Repo:** `universal-till`
**Reviewer:** Opus subagent, isolated worktree — `complexity:medium` routing
per the `scrum-master` skill's model table.

## What shipped

`web/public/osk.js`'s `focusin`/`focusout` handlers called `hide()` (which
removes `body.osk-padded`, reflowing the tender panel — confirmed live to
shift elements ~200px, ut-docs#1231) via a fixed-duration `setTimeout(fn, 0)`
/ `setTimeout(fn, 50)` scheduled from *inside* the mousedown that starts a
press (focus moves as part of mousedown's default action). This is the exact
race class ut-docs#1262 fixed for the `auto`-mode enable path: a fixed
timeout fires long before `mouseup`/`click` for any real, human-scale press
(confirmed there: a 120ms held press interleaves the timeout BEFORE
mouseup/click), so the reflow can land mid-press and shift the tapped
element out from under a still-in-flight tap — e.g. tapping the sale
screen's "Add" submit button while an OSK field has focus and the OSK is
open. Previously shielded by real touch's implicit pointer capture;
ut-docs#1262 deliberately widened its own enable path to also cover
mouse-shaped sessions (no implicit capture), which is why this ticket
(filed as an accepted, out-of-scope gap in that review) needed fixing too.

**Fix:** replaced the timer-only deferral with a `pointerDown`-flag-aware
`deferHideCheck(settleMs)`. If a press is currently in flight when focus
changes, the hide-check is deferred to fire only once that same press
resolves — `pointerup`, `pointercancel`, or a 1s backstop if neither ever
fires (pointer released outside the window, no capture) — instead of an
immediate timer, closing the race for a press of any duration. If no press
is in flight (keyboard Tab, a programmatic `.focus()` elsewhere), behavior
is unchanged: the original immediate `setTimeout(fn, settleMs)`.

New e2e regression test (`osk-central-guard.spec.ts`): a realistic-duration
press (`.click({ delay: 120 })`) on the sale screen's scan-row Add button
while the OSK is open (mode forced `"on"` to isolate from the unrelated
`auto`-mode enable-fallback path), asserting the scan reaches the basket.

Regenerated `web/help/img/**` + `manifest.json` (`make docs-shots`),
required by `guard-docs-shots.sh` for any `web/public/**` change.

## What the independent review found

**Core correctness confirmed sound.** Deferring to `pointerup` (or
`pointercancel`/the backstop) is genuinely sufficient: `mouseup` inherits
the already-hit-tested target `pointerup` resolved, and `click` targets the
common ancestor of the already-fixed mousedown/mouseup targets — a reflow
landing after `pointerup` cannot redirect the in-flight tap. Verified no
bad interaction with the file's existing `pointerdown`-preventDefault call
sites (the `[data-osk-toggle]` guard, the OSK's own keys in `build()`) —
neither calls `stopPropagation()`, and both suppress the focus change that
would otherwise reach `deferHideCheck` in the same dispatch. Non-pointer
focus changes (keyboard Tab, programmatic `.focus()`) verified identical
before/after the fix.

**F1 (medium, fixed before merge):** the original draft cleared the
`pointerDown` *flag* on `pointercancel` (protecting the *next* focus
change from wedging), but did nothing for a hide-check *already enqueued*
on the current press's `pointerup` once that press instead ends in
`pointercancel` — routine on real touch hardware (a scroll gesture taking
over mid-press) — or never resolves at all (pointer released outside the
window, no capture). Measured: post-fix-draft-1, `{oskOpen:true,
padded:true}` after a canceled press, where pre-fix code correctly hid.
Self-heals on the next tap anywhere, but the residual state is worse than
cosmetic — the `position:fixed` OSK sheet keeps covering the bottom of the
screen, which this file's own `show()` comment already documents as
something that "swallows the tap silently."

**Fix (applied):** `deferHideCheck`'s pending check now also listens for
`pointercancel` (not just `pointerup`), plus a 1s backstop timeout, all
sharing a `done` guard so only the first to fire runs the check. 1s is far
longer than any real tap/hold this file already reasons about (the
review's own 120ms probe), so it never races a still-genuinely-in-flight
press — only ever catches one that's already gone silent. This also closes
**F2** (a `pointerdown` with no matching up/cancel at all — e.g. mouse
released outside the window) via the same backstop.

**F3 (low, comment-only):** `pointerDown` is a single flag, not tracked per
`pointerId` — under multi-touch it can read `false` while a *different*
pointer is still down, briefly falling back to the immediate timer for
that pointer's own focus changes. Accepted as-is: multi-touch is real
touch, already shielded by implicit pointer capture. Comment amended to
state this scoping explicitly rather than imply full multi-touch coverage.

**F4 (informational, no code change):** the `[data-osk-toggle]` handler
calls `target.focus()` from inside its own `pointerup` dispatch, which (per
DOM semantics) means a `deferHideCheck` listener registered *during* that
same dispatch does not fire for that event — the check is silently
postponed to the next, unrelated press. Confirmed benign:
`hideIfFocusLeftOSK()` re-reads `document.activeElement` at fire time, so
a late fire can only ever hide when focus is genuinely off an OSK field by
then. Documented inline so a future reader doesn't "fix" this into
something racier.

## Non-blocking, accepted (carried over / re-confirmed)

- Same accepted native-keyboard first-tap gap from ut-docs#1262 (unrelated
  to this fix, unchanged).
- Screenshot pixel deltas (`ar/multitill.png`, `fa/till-designer.png`)
  traced to the same class of cosmetic, residual cursor/hover state
  ut-docs#1262's own review documented — not a content regression.
- No real client/shop name (fixtures reuse the established demo seed,
  `Coca-Cola 330ml` / `2000010000012`, already used by three other specs).
- No secret-shaped literal anywhere in the diff.
- No i18n/money/repository-pattern/offline-first impact — `osk.js` is a
  vendored local asset with no network use and no new strings.
- Manual (`web/help/en/display.md`) needs no update: it describes only
  that the OSK "pops up automatically on touch screens," nothing documents
  the old hide timing as intentional, and this adds no new user-facing
  surface or workflow.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `go test ./...` | pass (no Go files touched by this diff) |
| `golangci-lint run ./...` | 0 issues |
| All CI-blocking guards in `.github/workflows/ci.yml`'s `build` job | pass, incl. `guard-docs-shots.sh`, `guard-i18n.sh`, `guard-help-topics.sh`, `guard-htmx-loaded.sh`, `guard-e2e-fixtures-import.sh`, `guard-compliance-claims.sh` |
| OSK e2e suite (`osk-central-guard`, `settings-osk`, `sale-screen-osk-scan-submit-1177`, `payment-overlay-osk-1385`, plus the full suite run earlier) | all green, including the new ut-docs#1306 test |
| `make docs-shots` (100 screenshots × locales) | 100/100 pass |
| TDD re-verification (original bug, done independently by the reviewer in an isolated worktree) | revert `osk.js` to pre-fix (`HEAD~1`/`main`'s tip) → new test fails: `page.waitForResponse: Test timeout of 30000ms exceeded` waiting on `/api/pos/scan` (the tap was silently dropped, no request ever issued); restore → passes |
| F1/F2 fix re-verification | measured `{oskOpen, padded}` state after a canceled press before/after the `pointercancel`+backstop fix: stuck open → correctly hidden |

## Verdict

Safe to merge. One medium-severity finding (F1, sharing its fix with F2)
was fixed and re-verified before this commit; two low/informational
findings (F3, F4) were addressed with clarifying comments only, no
behavior change needed. Full gate green.
