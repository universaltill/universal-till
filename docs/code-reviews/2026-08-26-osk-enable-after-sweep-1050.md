# Code review: osk.js — re-guard a field flipped enabled after the up-front sweep (ut-docs#1050)

**Card:** ut-docs#1050, follow-up from the independent review of ut-docs#1022
(`2026-08-25-osk-central-guard-double-keyboard-1022.md`).
**Complexity:** easy. **Review model:** fresh-context Sonnet subagent
(per "Model routing by complexity" — easy tier relaxes "different model"
to "different instance").

## What shipped

`web/public/osk.js`'s `guardField()`/`guardSweep()` sweep every OSK-able
field up front and set `inputmode="none"` so the native OS keyboard never
races the custom on-screen one. `wantsOSK()` correctly skips a field that
is `disabled`/`readOnly`/`type="hidden"` at the moment it's swept — but
nothing re-swept a field if its eligibility changed **in place** later
(no further DOM mutation for the existing childList/subtree
`MutationObserver` to catch), leaving it silently unguarded for the life
of the page and reopening the pre-#1022 double-keyboard race for that one
field.

Fix: the same `MutationObserver` now also watches `disabled`/`readonly`/
`type` attribute changes (via `attributeFilter`, not a bare
`attributes: true`) and re-runs the idempotent `guardField()` on the
mutated element whenever one of those flips.

New Playwright regression test (`e2e/tests/osk-central-guard.spec.ts`):
creates a disabled input, confirms it's unguarded, flips `.disabled =
false` with no other DOM mutation, confirms it then gets
`inputmode="none"`.

## Acceptance criteria (ut-docs#1050)

- **Confirm/deny whether any current template/JS pattern exercises the
  gap today.** Grepped `web/` for `.disabled = false`, `.readOnly =
  false`, `removeAttribute('disabled'|'readonly')`,
  `setAttribute('disabled'|'readonly', ...)`, and a type-flip to any
  OSK-able type. Every `.disabled` write found (settings.html,
  plugins_store.html, login.html, tills.html, setup.html, base.html,
  bugreport_panel.html, reports_tab_eod.html, app.js) targets a
  `<button>` variable, never an input/textarea. Zero hits for the
  `readonly`/type-flip patterns. **Answer: no** — the fix is preventive,
  same as the code comment it replaces already acknowledged for its own
  narrower scope.
- **Regression test covering the enable-after-sweep case.** Added (see
  above); confirmed failing pre-fix, passing post-fix (below).

## TDD verification

Confirmed by Dev, then independently re-verified by the review subagent
via revert-then-restore (isolated worktree, per ut-docs#386):

- Reverted only `osk.js` (kept the new test), ran it: failed with
  `Error: expect(locator).toHaveAttribute(expected) failed — Expected:
  "none" — Received: "" (unexpected value "null")` — a real assertion
  failure on the exact behavior under test, not a timeout/crash.
- Restored the fix, re-ran: 1 passed. Full `osk-central-guard.spec.ts`
  (8/8), `settings-osk.spec.ts` (7/7), and `index-keyboard-1023.spec.ts`
  (2/2) all pass with no regressions.

## Independent review findings

One real-but-minor gap, fixed before merge (not a second review round —
scoped, cheap, no blocker-class issue to earn one): the original fix's
`attributeFilter` only covered `disabled`/`readonly`, not `type` — so a
`type="hidden"` field later flipped to an OSK-able type in place would
still have silently stayed unguarded, the same race via a third,
uncovered trigger. Added `'type'` to `attributeFilter` and updated the
adjacent comment to say so explicitly, so a future reader doesn't have to
re-derive it from git history. Re-ran the full OSK e2e suite after the
change — still 15/15 green.

Other checks, all clean: `guardField(mutation.target)` no-ops safely for
a non-input/textarea target (e.g. a `<button disabled>` inside the
observed subtree) since `wantsOSK()` gates on tag first; the narrower
`attributeFilter` (vs. a bare `attributes: true`) avoids re-firing on
unrelated `hx-swap-oob` attribute tweaks (class/style/hx-*/data-*), the
exact cost the original code comment opted out of paying; no hardcoded
secrets or real client/shop names; no new user-facing strings (pure JS
logic + comments, no i18n key needed); no visible surface changed
(`inputmode` has no visual effect) so no UX-guideline or help-manual
check applies.

## Gate run before commit

`gofmt -l .` clean; `go build ./...` clean; every CI-blocking guard in
`.github/workflows/ci.yml`'s `build` job passed, including
`guard-osk-loaded.sh` and `guard-i18n.sh`; full `go test ./...` — all Go
packages pass except `internal/plugins`, which timed out under `-race`
in both the full-suite run and an isolated re-run (a real hang, not
contention noise — goroutines stuck in `chan receive`/`IO wait` in
`TestEventBusSubscriberBookkeeping`'s DB setup). **Not caused by this
change** — this diff touches only `web/public/osk.js` and an e2e spec,
no Go code, and `internal/plugins` has no dependency on either. Logged as
a separate finding (new Backlog card) rather than blocking this PR on an
unrelated pre-existing issue.

## Verdict

**Safe to merge.** No blocker-class findings; the one real-but-minor gap
found was cheap to fix and is fixed in this same diff.
